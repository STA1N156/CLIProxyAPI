package dataintegration

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
	log "github.com/sirupsen/logrus"
)

const (
	DefaultRootDir     = "/data"
	statsVersion       = 7
	dayStatsVersion    = 4
	minStatsVersion    = 6
	minDayStatsVersion = 3
	maskCount          = 1 << criterionCount
	queueSize          = 2048
	rawQueueSize       = 2048
	maxRawQueueBytes   = 256 << 20
	maxBatchSize       = 256
	batchInterval      = 100 * time.Millisecond
	statsSyncInterval  = time.Second
)

var (
	ErrInitializing = errors.New("data integration statistics are initializing")
	ErrQueueFull    = errors.New("data integration background queue is full")
	ErrClearing     = errors.New("data integration storage is being cleared")
)

// StoredRecord is one line in an internal minute shard.
type StoredRecord struct {
	CapturedAt time.Time       `json:"captured_at"`
	RequestID  string          `json:"request_id,omitempty"`
	Path       string          `json:"path"`
	Evaluation Evaluation      `json:"evaluation"`
	Payload    json.RawMessage `json:"payload"`
}

type pendingRecord struct {
	capturedAt time.Time
	mask       uint8
	tokenCount uint64
	data       []byte
	clear      chan clearResponse
}

type rawRecord struct {
	capturedAt      time.Time
	path            string
	requestID       string
	sessionID       string
	contentEncoding string
	payload         []byte
}

type clearResponse struct {
	result ClearResult
	err    error
}

type persistedStats struct {
	Version       int       `json:"version"`
	Total         uint64    `json:"total"`
	MaskCounts    []uint64  `json:"mask_counts"`
	TokenCounts   []uint64  `json:"token_counts"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastShard     string    `json:"last_shard,omitempty"`
	LastShardSize int64     `json:"last_shard_size,omitempty"`
}

type persistedDayStats struct {
	Version      int                 `json:"version"`
	Day          string              `json:"day"`
	Minutes      map[string][]uint64 `json:"minutes"`
	MinuteTokens map[string][]uint64 `json:"minute_tokens"`
}

// TimeRange limits statistics and downloads. Boundaries are inclusive.
type TimeRange struct {
	From *time.Time
	To   *time.Time
}

// CriterionStats is one row in the management statistics response.
type CriterionStats struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Selected bool    `json:"selected"`
	Matched  uint64  `json:"matched"`
	Rate     float64 `json:"rate"`
}

// StatsView is the management-friendly statistics snapshot.
type StatsView struct {
	TotalRequests     uint64           `json:"total_requests"`
	MatchedRequests   uint64           `json:"matched_requests"`
	MatchedTokens     uint64           `json:"matched_tokens"`
	MatchRate         float64          `json:"match_rate"`
	SelectedCriteria  []string         `json:"selected_criteria"`
	AvailableDownload uint64           `json:"available_download"`
	StorageDirectory  string           `json:"storage_directory"`
	QueueDepth        int              `json:"queue_depth"`
	DroppedRequests   uint64           `json:"dropped_requests"`
	From              string           `json:"from,omitempty"`
	To                string           `json:"to,omitempty"`
	UpdatedAt         time.Time        `json:"updated_at"`
	Criteria          []CriterionStats `json:"criteria"`
}

// ClearResult describes a completed cleanup.
type ClearResult struct {
	RemovedRequests uint64    `json:"removed_requests"`
	ClearedAt       time.Time `json:"cleared_at"`
}

// RecordRef identifies one JSONL line without keeping its payload in memory.
type RecordRef struct {
	offset int64
	length int
}

// Store persists requests through one bounded writer. If the bounded capture
// queue fills, collection drops data instead of blocking proxy requests.
type Store struct {
	root        string
	sessionsDir string
	statsPath   string
	dayStatsDir string

	statsMu sync.RWMutex
	stats   persistedStats

	dayStatsMu sync.RWMutex
	dayStats   map[string]*persistedDayStats
	dirtyDays  map[string]struct{}

	maintenanceMu sync.RWMutex
	queue         chan pendingRecord
	rawQueue      chan rawRecord
	stop          chan struct{}
	workersStop   chan struct{}
	done          chan struct{}
	workerWG      sync.WaitGroup
	rawPending    sync.WaitGroup
	queuedBytes   atomic.Int64
	dropped       atomic.Uint64

	stateMu   sync.Mutex
	closed    bool
	clearing  bool
	started   bool
	active    sync.WaitGroup
	closeOnce sync.Once
	initOnce  sync.Once
	initErr   error

	warmupOnce sync.Once
	warmupDone chan struct{}
	warming    bool
}

// RootDir resolves the storage path. DATA_INTEGRATION_DIR is intended for
// non-Docker development; production defaults to the mounted /data disk.
func RootDir() string {
	if value := strings.TrimSpace(os.Getenv("DATA_INTEGRATION_DIR")); value != "" {
		return filepath.Clean(value)
	}
	return DefaultRootDir
}

// NewStore opens or creates the persistent data integration store.
func NewStore(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = RootDir()
	}
	root = filepath.Clean(root)
	store := &Store{
		root:        root,
		sessionsDir: filepath.Join(root, "sessions"),
		statsPath:   filepath.Join(root, "stats.json"),
		dayStatsDir: filepath.Join(root, "stats"),
		dayStats:    make(map[string]*persistedDayStats),
		dirtyDays:   make(map[string]struct{}),
		queue:       make(chan pendingRecord, queueSize),
		rawQueue:    make(chan rawRecord, rawQueueSize),
		stop:        make(chan struct{}),
		workersStop: make(chan struct{}),
		done:        make(chan struct{}),
		warmupDone:  make(chan struct{}),
	}
	return store, nil
}

// StartWarmup loads statistics in the background. While it is running, callers
// fail fast instead of delaying proxy requests.
func (s *Store) StartWarmup() {
	if s == nil {
		return
	}
	s.warmupOnce.Do(func() {
		s.stateMu.Lock()
		s.warming = true
		s.stateMu.Unlock()
		go func() {
			_ = s.ensureInitialized()
			close(s.warmupDone)
		}()
	})
}

func (s *Store) warmupStatus() error {
	s.stateMu.Lock()
	warming := s.warming
	s.stateMu.Unlock()
	if !warming {
		return nil
	}
	select {
	case <-s.warmupDone:
		return s.initErr
	default:
		return ErrInitializing
	}
}

// RecordRequest implements the SDK request recorder hook.
func (s *Store) RecordRequest(path, requestID string, payload []byte) error {
	return s.EnqueueCapturedRequest(
		time.Now().UTC(),
		path,
		requestID,
		"",
		"",
		bytes.Clone(payload),
	)
}

// EnqueueCapturedRequest transfers an owned raw request body to the background
// pipeline. It never waits for validation, statistics initialization, or disk.
func (s *Store) EnqueueCapturedRequest(
	capturedAt time.Time,
	path, requestID, sessionID, contentEncoding string,
	payload []byte,
) error {
	if s == nil {
		return fmt.Errorf("data integration store is unavailable")
	}
	if len(payload) == 0 {
		return nil
	}
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}
	record := rawRecord{
		capturedAt:      capturedAt.UTC(),
		path:            strings.TrimSpace(path),
		requestID:       strings.TrimSpace(requestID),
		sessionID:       strings.TrimSpace(sessionID),
		contentEncoding: strings.TrimSpace(contentEncoding),
		payload:         payload,
	}
	payloadBytes := int64(len(payload))

	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.closed {
		return fmt.Errorf("data integration store is closed")
	}
	if s.clearing {
		s.dropped.Add(1)
		return ErrClearing
	}
	if s.queuedBytes.Load()+payloadBytes > maxRawQueueBytes {
		s.dropped.Add(1)
		return ErrQueueFull
	}
	s.startPipelineLocked()
	s.rawPending.Add(1)
	s.queuedBytes.Add(payloadBytes)
	select {
	case s.rawQueue <- record:
		return nil
	default:
		s.queuedBytes.Add(-payloadBytes)
		s.rawPending.Done()
		s.dropped.Add(1)
		return ErrQueueFull
	}
}

// Record evaluates and queues one valid JSON request.
func (s *Store) Record(path, requestID string, payload []byte) (Evaluation, error) {
	return s.RecordNative(path, requestID, "", payload)
}

// RecordNative records metadata that was explicitly present in the request.
func (s *Store) RecordNative(path, requestID, sessionID string, payload []byte) (Evaluation, error) {
	if s == nil {
		return Evaluation{}, fmt.Errorf("data integration store is unavailable")
	}
	if errWarmup := s.warmupStatus(); errWarmup != nil {
		return Evaluation{}, errWarmup
	}
	pending, evaluation, errPrepare := preparePendingRecord(
		time.Now().UTC(),
		path,
		requestID,
		sessionID,
		payload,
	)
	if errPrepare != nil {
		return Evaluation{}, errPrepare
	}
	if len(pending.data) == 0 {
		return evaluation, nil
	}
	if errInit := s.ensureInitialized(); errInit != nil {
		return Evaluation{}, errInit
	}

	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return Evaluation{}, fmt.Errorf("data integration store is closed")
	}
	if s.clearing {
		s.stateMu.Unlock()
		return Evaluation{}, ErrClearing
	}
	s.startPipelineLocked()
	s.rawPending.Add(1)
	s.stateMu.Unlock()
	defer s.rawPending.Done()

	s.queue <- pending
	return evaluation, nil
}

func preparePendingRecord(
	capturedAt time.Time,
	path, requestID, sessionID string,
	payload []byte,
) (pendingRecord, Evaluation, error) {
	enriched, errEnrich := enrichNativeMetadata(payload, path, sessionID)
	if errEnrich != nil {
		return pendingRecord{}, Evaluation{}, fmt.Errorf("add native request metadata: %w", errEnrich)
	}
	evaluation, errEvaluate := Evaluate(enriched)
	if errEvaluate != nil {
		return pendingRecord{}, Evaluation{}, errEvaluate
	}
	if evaluation.Mask&storageRequirementMask != storageRequirementMask {
		return pendingRecord{}, evaluation, nil
	}

	record := StoredRecord{
		CapturedAt: capturedAt.UTC(),
		RequestID:  strings.TrimSpace(requestID),
		Path:       strings.TrimSpace(path),
		Evaluation: evaluation,
		Payload:    json.RawMessage(enriched),
	}
	recordData, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		return pendingRecord{}, Evaluation{}, fmt.Errorf("encode session record: %w", errMarshal)
	}
	recordData = append(recordData, '\n')
	return pendingRecord{
		capturedAt: capturedAt.UTC(),
		mask:       evaluation.Mask,
		tokenCount: evaluation.TokenCount,
		data:       recordData,
	}, evaluation, nil
}

// Close flushes queued records and the final statistics snapshot.
func (s *Store) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.closeOnce.Do(func() {
		s.stateMu.Lock()
		s.closed = true
		started := s.started
		s.stateMu.Unlock()
		if !started {
			close(s.done)
			return
		}
		go func() {
			s.active.Wait()
			s.rawPending.Wait()
			close(s.workersStop)
			s.workerWG.Wait()
			close(s.stop)
		}()
	})

	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns the ratio for any selected combination and optional time range.
// A zero mask matches all sessions admitted by the storage requirements.
func (s *Store) Stats(selectedMask uint8, timeRange TimeRange) (StatsView, error) {
	if s == nil {
		return StatsView{}, fmt.Errorf("data integration store is unavailable")
	}
	if errWarmup := s.warmupStatus(); errWarmup != nil {
		return StatsView{}, errWarmup
	}
	if errInit := s.ensureInitialized(); errInit != nil {
		return StatsView{}, errInit
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	counts, tokens, _, updatedAt, errCounts := s.countsForRange(timeRange)
	if errCounts != nil {
		return StatsView{}, errCounts
	}

	requestedMask := selectedMask &^ storageRequirementMask
	effectiveMask := requestedMask | storageRequirementMask
	total := matchedForMask(counts, storageRequirementMask)
	matched := matchedForMask(counts, effectiveMask)
	view := StatsView{
		TotalRequests:     total,
		MatchedRequests:   matched,
		MatchedTokens:     matchedForMask(tokens, effectiveMask),
		MatchRate:         percentage(matched, total),
		SelectedCriteria:  KeysForMask(requestedMask),
		AvailableDownload: matched,
		StorageDirectory:  filepath.ToSlash(s.root),
		QueueDepth:        len(s.rawQueue) + len(s.queue),
		DroppedRequests:   s.dropped.Load(),
		UpdatedAt:         updatedAt,
		Criteria:          make([]CriterionStats, 0, len(Criteria)),
	}
	if timeRange.From != nil {
		view.From = timeRange.From.UTC().Format(time.RFC3339)
	}
	if timeRange.To != nil {
		view.To = timeRange.To.UTC().Format(time.RFC3339)
	}
	for _, criterion := range Criteria {
		if criterion.Bit&storageRequirementMask != 0 {
			continue
		}
		criterionMatched := matchedForMask(counts, criterion.Bit|storageRequirementMask)
		view.Criteria = append(view.Criteria, CriterionStats{
			Key:      criterion.Key,
			Label:    criterion.Label,
			Selected: requestedMask&criterion.Bit != 0,
			Matched:  criterionMatched,
			Rate:     percentage(criterionMatched, total),
		})
	}
	return view, nil
}

// Clear removes all stored data integration sessions and statistics. The
// command is serialized with writes so requests received after it are kept.
func (s *Store) Clear(ctx context.Context) (ClearResult, error) {
	if s == nil {
		return ClearResult{}, fmt.Errorf("data integration store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errWarmup := s.warmupStatus(); errWarmup != nil {
		return ClearResult{}, errWarmup
	}
	if errInit := s.ensureInitialized(); errInit != nil {
		return ClearResult{}, errInit
	}

	response := make(chan clearResponse, 1)
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return ClearResult{}, fmt.Errorf("data integration store is closed")
	}
	if s.clearing {
		s.stateMu.Unlock()
		return ClearResult{}, ErrClearing
	}
	s.clearing = true
	s.startPipelineLocked()
	s.active.Add(1)
	s.stateMu.Unlock()
	defer func() {
		s.stateMu.Lock()
		s.clearing = false
		s.stateMu.Unlock()
		s.active.Done()
	}()

	s.rawPending.Wait()
	select {
	case s.queue <- pendingRecord{clear: response}:
	case <-ctx.Done():
		return ClearResult{}, ctx.Err()
	}
	select {
	case cleared := <-response:
		return cleared.result, cleared.err
	case <-ctx.Done():
		return ClearResult{}, ctx.Err()
	}
}

// WriteZIP scans newest shards and streams one-file-per-session JSON or JSONL.
// Only one minute shard's references are held in memory at a time.
func (s *Store) WriteZIP(writer io.Writer, count int, selectedMask uint8, timeRange TimeRange, format string) error {
	return s.WriteZIPWithOptions(writer, count, selectedMask, timeRange, ExportOptions{Format: format})
}

// WriteZIPWithOptions streams matching sessions with the selected export layout.
func (s *Store) WriteZIPWithOptions(
	writer io.Writer,
	count int,
	selectedMask uint8,
	timeRange TimeRange,
	options ExportOptions,
) error {
	if s == nil {
		return fmt.Errorf("data integration store is unavailable")
	}
	if errWarmup := s.warmupStatus(); errWarmup != nil {
		return errWarmup
	}
	if errInit := s.ensureInitialized(); errInit != nil {
		return errInit
	}
	requestedMask := selectedMask &^ storageRequirementMask
	selectedMask = requestedMask | storageRequirementMask
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if count <= 0 {
		return fmt.Errorf("count must be greater than zero")
	}
	options, errOptions := normalizeExportOptions(options)
	if errOptions != nil {
		return errOptions
	}
	shards, errShards := s.shardPaths()
	if errShards != nil {
		return errShards
	}

	archive := zip.NewWriter(writer)
	manifest := struct {
		GeneratedAt      time.Time `json:"generated_at"`
		Count            int       `json:"count"`
		Format           string    `json:"format"`
		Layout           string    `json:"layout"`
		MessageField     string    `json:"message_field,omitempty"`
		SelectedCriteria []string  `json:"selected_criteria"`
		From             string    `json:"from,omitempty"`
		To               string    `json:"to,omitempty"`
	}{
		GeneratedAt:      time.Now().UTC(),
		Count:            count,
		Format:           options.Format,
		Layout:           options.Layout,
		SelectedCriteria: KeysForMask(requestedMask),
	}
	if options.Layout == ExportLayoutContract {
		manifest.MessageField = options.MessageField
	}
	if timeRange.From != nil {
		manifest.From = timeRange.From.UTC().Format(time.RFC3339)
	}
	if timeRange.To != nil {
		manifest.To = timeRange.To.UTC().Format(time.RFC3339)
	}
	manifestData, errManifest := json.MarshalIndent(manifest, "", "  ")
	if errManifest != nil {
		_ = archive.Close()
		return fmt.Errorf("encode archive manifest: %w", errManifest)
	}
	manifestWriter, errCreate := archive.Create("manifest.json")
	if errCreate != nil {
		_ = archive.Close()
		return fmt.Errorf("create archive manifest: %w", errCreate)
	}
	if _, errWrite := manifestWriter.Write(manifestData); errWrite != nil {
		_ = archive.Close()
		return fmt.Errorf("write archive manifest: %w", errWrite)
	}

	exported := 0
	for _, path := range shards {
		if exported == count {
			break
		}
		if !shardMayOverlap(path, timeRange) {
			continue
		}
		records, errScan := selectNewestFromShard(path, selectedMask, count-exported, timeRange)
		if errScan != nil {
			_ = archive.Close()
			return errScan
		}
		if len(records) == 0 {
			continue
		}
		shard, errOpen := os.Open(path)
		if errOpen != nil {
			_ = archive.Close()
			return fmt.Errorf("open stored session shard: %w", errOpen)
		}
		for _, record := range records {
			payload, errPayload := readStoredPayload(shard, record)
			if errPayload != nil {
				_ = shard.Close()
				_ = archive.Close()
				return errPayload
			}
			if options.Layout == ExportLayoutContract {
				payload, errPayload = toContractPayload(payload, options.MessageField)
				if errPayload != nil {
					_ = shard.Close()
					_ = archive.Close()
					return errPayload
				}
			}
			if options.Format == "jsonl" {
				payload = append(payload, '\n')
			}
			exported++
			name := fmt.Sprintf("sessions/%06d.%s", exported, options.Format)
			sessionWriter, errSession := archive.Create(name)
			if errSession != nil {
				_ = shard.Close()
				_ = archive.Close()
				return fmt.Errorf("create %s: %w", name, errSession)
			}
			if _, errWrite := sessionWriter.Write(payload); errWrite != nil {
				_ = shard.Close()
				_ = archive.Close()
				return fmt.Errorf("write %s: %w", name, errWrite)
			}
		}
		if errClose := shard.Close(); errClose != nil {
			_ = archive.Close()
			return fmt.Errorf("close stored session shard: %w", errClose)
		}
	}
	if exported < count {
		_ = archive.Close()
		return fmt.Errorf("requested %d sessions but only %d match", count, exported)
	}

	if errClose := archive.Close(); errClose != nil {
		return fmt.Errorf("close zip archive: %w", errClose)
	}
	return nil
}

func (s *Store) startPipelineLocked() {
	if s.started {
		return
	}
	s.started = true
	go s.writer()
	s.workerWG.Add(2)
	go s.rawWorker()
	go s.rawWorker()
}

func (s *Store) rawWorker() {
	defer s.workerWG.Done()
	for {
		select {
		case raw := <-s.rawQueue:
			func() {
				defer s.rawPending.Done()
				defer s.queuedBytes.Add(-int64(len(raw.payload)))

				payload, errDecode := decodeRawPayload(raw.payload, raw.contentEncoding)
				if errDecode != nil {
					log.WithError(errDecode).Debug("skipping undecodable data integration request")
					return
				}
				pending, _, errPrepare := preparePendingRecord(
					raw.capturedAt,
					raw.path,
					raw.requestID,
					raw.sessionID,
					payload,
				)
				if errPrepare != nil {
					log.WithError(errPrepare).Debug("skipping invalid data integration request")
					return
				}
				if len(pending.data) == 0 {
					return
				}
				if errInit := s.ensureInitialized(); errInit != nil {
					log.WithError(errInit).Warn("data integration background initialization failed")
					return
				}
				s.queue <- pending
			}()
		case <-s.workersStop:
			return
		}
	}
}

func decodeRawPayload(payload []byte, contentEncoding string) ([]byte, error) {
	contentEncoding = strings.TrimSpace(contentEncoding)
	if contentEncoding == "" || strings.EqualFold(contentEncoding, "identity") {
		return payload, nil
	}
	body := payload
	parts := strings.Split(contentEncoding, ",")
	for index := len(parts) - 1; index >= 0; index-- {
		switch encoding := strings.ToLower(strings.TrimSpace(parts[index])); encoding {
		case "", "identity":
			continue
		case "zstd":
			decoder, errDecoder := zstd.NewReader(bytes.NewReader(body))
			if errDecoder != nil {
				return nil, fmt.Errorf("create zstd request decoder: %w", errDecoder)
			}
			decoded, errRead := io.ReadAll(decoder)
			decoder.Close()
			if errRead != nil {
				return nil, fmt.Errorf("decode zstd request body: %w", errRead)
			}
			body = decoded
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", encoding)
		}
	}
	return body, nil
}

func (s *Store) writer() {
	defer close(s.done)
	batchTicker := time.NewTicker(batchInterval)
	statsTicker := time.NewTicker(statsSyncInterval)
	defer batchTicker.Stop()
	defer statsTicker.Stop()

	batch := make([]pendingRecord, 0, maxBatchSize)
	statsDirty := false
	flushBatch := func() {
		if len(batch) == 0 {
			return
		}
		s.writeBatch(batch)
		batch = batch[:0]
		statsDirty = true
	}
	flushStats := func() {
		if !statsDirty {
			return
		}
		if errStats := s.writeAllStats(); errStats != nil {
			log.WithError(errStats).Warn("failed to persist data integration stats")
			return
		}
		statsDirty = false
	}
	handleRecord := func(record pendingRecord) {
		if record.clear != nil {
			flushBatch()
			result, errClear := s.clearStoredData()
			if errClear == nil {
				statsDirty = false
			}
			record.clear <- clearResponse{result: result, err: errClear}
			return
		}
		batch = append(batch, record)
		if len(batch) >= maxBatchSize {
			flushBatch()
		}
	}

	for {
		select {
		case record := <-s.queue:
			handleRecord(record)
		case <-batchTicker.C:
			flushBatch()
		case <-statsTicker.C:
			flushStats()
		case <-s.stop:
			for {
				select {
				case record := <-s.queue:
					handleRecord(record)
				default:
					flushBatch()
					flushStats()
					return
				}
			}
		}
	}
}

func (s *Store) clearStoredData() (ClearResult, error) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()

	s.statsMu.RLock()
	removed := s.stats.Total
	s.statsMu.RUnlock()

	for _, path := range []string{s.sessionsDir, s.dayStatsDir} {
		if errRemove := os.RemoveAll(path); errRemove != nil {
			return ClearResult{}, fmt.Errorf("remove data integration files: %w", errRemove)
		}
		if errMkdir := os.MkdirAll(path, 0o700); errMkdir != nil {
			return ClearResult{}, fmt.Errorf("recreate data integration directory: %w", errMkdir)
		}
	}
	if errRemove := os.Remove(s.statsPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		return ClearResult{}, fmt.Errorf("remove data integration statistics: %w", errRemove)
	}

	clearedAt := time.Now().UTC()
	s.statsMu.Lock()
	s.stats = persistedStats{
		Version:     statsVersion,
		MaskCounts:  make([]uint64, maskCount),
		TokenCounts: make([]uint64, maskCount),
		UpdatedAt:   clearedAt,
	}
	s.statsMu.Unlock()
	s.dayStatsMu.Lock()
	s.dayStats = make(map[string]*persistedDayStats)
	s.dirtyDays = make(map[string]struct{})
	s.dayStatsMu.Unlock()
	s.dropped.Store(0)
	if errStats := s.writeStats(); errStats != nil {
		return ClearResult{}, fmt.Errorf("write cleared data integration statistics: %w", errStats)
	}
	return ClearResult{RemovedRequests: removed, ClearedAt: clearedAt}, nil
}

func (s *Store) ensureInitialized() error {
	s.initOnce.Do(func() {
		if errMkdir := os.MkdirAll(s.sessionsDir, 0o700); errMkdir != nil {
			s.initErr = fmt.Errorf("create data integration directory: %w", errMkdir)
			return
		}
		if errMkdir := os.MkdirAll(s.dayStatsDir, 0o700); errMkdir != nil {
			s.initErr = fmt.Errorf("create data integration stats directory: %w", errMkdir)
			return
		}
		s.initErr = s.loadOrRebuildStats()
	})
	return s.initErr
}

func (s *Store) writeBatch(batch []pendingRecord) {
	groups := make(map[string][]pendingRecord)
	for _, record := range batch {
		path := filepath.Join(
			s.sessionsDir,
			record.capturedAt.Format("2006-01-02"),
			record.capturedAt.Format("1504")+".jsonl",
		)
		groups[path] = append(groups[path], record)
	}

	for path, records := range groups {
		var buffer bytes.Buffer
		for _, record := range records {
			_, _ = buffer.Write(record.data)
		}
		for {
			if errAppend := appendShard(path, buffer.Bytes()); errAppend != nil {
				log.WithError(errAppend).WithField("shard", filepath.Base(path)).
					Error("data integration disk write failed; applying backpressure and retrying")
				time.Sleep(time.Second)
				continue
			}
			break
		}

		s.statsMu.Lock()
		for _, record := range records {
			s.stats.Total++
			s.stats.MaskCounts[record.mask]++
			s.stats.TokenCounts[record.mask] += record.tokenCount
			if record.capturedAt.After(s.stats.UpdatedAt) {
				s.stats.UpdatedAt = record.capturedAt
			}
		}
		if info, errStat := os.Stat(path); errStat == nil && path >= s.stats.LastShard {
			s.stats.LastShard = path
			s.stats.LastShardSize = info.Size()
		}
		s.statsMu.Unlock()
		s.updateDayStats(records)
	}
}

func (s *Store) updateDayStats(records []pendingRecord) {
	s.dayStatsMu.Lock()
	defer s.dayStatsMu.Unlock()
	for _, record := range records {
		day := record.capturedAt.Format("2006-01-02")
		stats := s.dayStats[day]
		if stats == nil {
			stats = s.loadDayStatsFile(day)
			s.dayStats[day] = stats
		}
		minute := record.capturedAt.Format("1504")
		counts := stats.Minutes[minute]
		if len(counts) < maskCount {
			counts = append(counts, make([]uint64, maskCount-len(counts))...)
		}
		counts = counts[:maskCount]
		counts[record.mask]++
		stats.Minutes[minute] = counts
		tokens := stats.MinuteTokens[minute]
		if len(tokens) < maskCount {
			tokens = append(tokens, make([]uint64, maskCount-len(tokens))...)
		}
		tokens = tokens[:maskCount]
		tokens[record.mask] += record.tokenCount
		stats.MinuteTokens[minute] = tokens
		s.dirtyDays[day] = struct{}{}
	}
}

func (s *Store) writeAllStats() error {
	if errDayStats := s.writeDirtyDayStats(); errDayStats != nil {
		return errDayStats
	}
	return s.writeStats()
}

func (s *Store) writeDirtyDayStats() error {
	s.dayStatsMu.Lock()
	defer s.dayStatsMu.Unlock()
	for day := range s.dirtyDays {
		stats := s.dayStats[day]
		if stats == nil {
			delete(s.dirtyDays, day)
			continue
		}
		data, errMarshal := json.Marshal(stats)
		if errMarshal != nil {
			return fmt.Errorf("encode daily data integration stats: %w", errMarshal)
		}
		if errWrite := os.WriteFile(filepath.Join(s.dayStatsDir, day+".json"), data, 0o600); errWrite != nil {
			return fmt.Errorf("write daily data integration stats: %w", errWrite)
		}
		delete(s.dirtyDays, day)
	}
	return nil
}

func (s *Store) loadDayStatsFile(day string) *persistedDayStats {
	stats := &persistedDayStats{
		Version:      dayStatsVersion,
		Day:          day,
		Minutes:      make(map[string][]uint64),
		MinuteTokens: make(map[string][]uint64),
	}
	data, errRead := os.ReadFile(filepath.Join(s.dayStatsDir, day+".json"))
	if errRead != nil {
		return stats
	}
	var stored persistedDayStats
	if errUnmarshal := json.Unmarshal(data, &stored); errUnmarshal != nil ||
		stored.Version < minDayStatsVersion || stored.Version > dayStatsVersion || stored.Day != day {
		return stats
	}
	if stored.Minutes == nil {
		stored.Minutes = make(map[string][]uint64)
	}
	if stored.MinuteTokens == nil {
		stored.MinuteTokens = make(map[string][]uint64)
	}
	return &stored
}

func appendShard(path string, data []byte) error {
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return errMkdir
	}
	file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if errOpen != nil {
		return errOpen
	}
	info, errStat := file.Stat()
	if errStat != nil {
		_ = file.Close()
		return errStat
	}
	startSize := info.Size()
	remaining := data
	for len(remaining) > 0 {
		written, errWrite := file.Write(remaining)
		if errWrite != nil {
			_ = file.Truncate(startSize)
			_ = file.Close()
			return errWrite
		}
		if written == 0 {
			_ = file.Truncate(startSize)
			_ = file.Close()
			return io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	if errSync := file.Sync(); errSync != nil {
		_ = file.Truncate(startSize)
		_ = file.Close()
		return errSync
	}
	return file.Close()
}

func (s *Store) loadOrRebuildStats() error {
	data, errRead := os.ReadFile(s.statsPath)
	if errRead == nil {
		var stats persistedStats
		if errUnmarshal := json.Unmarshal(data, &stats); errUnmarshal == nil &&
			stats.Version >= minStatsVersion && stats.Version <= statsVersion {
			latestPath, latestSize, errLatest := s.latestShardState()
			if errLatest != nil {
				return errLatest
			}
			if latestPath == stats.LastShard && latestSize == stats.LastShardSize {
				if len(stats.MaskCounts) < maskCount {
					stats.MaskCounts = append(stats.MaskCounts, make([]uint64, maskCount-len(stats.MaskCounts))...)
				}
				stats.MaskCounts = stats.MaskCounts[:maskCount]
				if len(stats.TokenCounts) < maskCount {
					stats.TokenCounts = append(stats.TokenCounts, make([]uint64, maskCount-len(stats.TokenCounts))...)
				}
				stats.TokenCounts = stats.TokenCounts[:maskCount]
				s.stats = stats
				return nil
			}
		}
	}
	if errRead != nil && !errors.Is(errRead, os.ErrNotExist) {
		return fmt.Errorf("read data integration stats: %w", errRead)
	}

	stats := persistedStats{
		Version:     statsVersion,
		MaskCounts:  make([]uint64, maskCount),
		TokenCounts: make([]uint64, maskCount),
	}
	shards, errShards := s.shardPaths()
	if errShards != nil {
		return errShards
	}
	for _, path := range shards {
		shardTime, okShardTime := shardMinute(path)
		if !okShardTime {
			continue
		}
		day := shardTime.Format("2006-01-02")
		minute := shardTime.Format("1504")
		dayStats := s.dayStats[day]
		if dayStats == nil {
			dayStats = s.loadDayStatsFile(day)
			s.dayStats[day] = dayStats
		}
		minuteCounts := make([]uint64, maskCount)
		minuteTokens := make([]uint64, maskCount)
		if errScan := scanShard(path, func(_ int64, _ []byte, mask uint8, _ time.Time, tokenCount uint64) bool {
			stats.Total++
			stats.MaskCounts[mask]++
			stats.TokenCounts[mask] += tokenCount
			minuteCounts[mask]++
			minuteTokens[mask] += tokenCount
			return true
		}); errScan != nil {
			return errScan
		}
		dayStats.Minutes[minute] = minuteCounts
		dayStats.MinuteTokens[minute] = minuteTokens
		s.dirtyDays[day] = struct{}{}
	}
	stats.UpdatedAt = time.Now().UTC()
	if len(shards) > 0 {
		if info, errStat := os.Stat(shards[0]); errStat == nil {
			stats.LastShard = shards[0]
			stats.LastShardSize = info.Size()
		}
	}
	s.stats = stats
	return s.writeAllStats()
}

func (s *Store) writeStats() error {
	s.statsMu.RLock()
	stats := s.stats
	stats.MaskCounts = append([]uint64(nil), s.stats.MaskCounts...)
	stats.TokenCounts = append([]uint64(nil), s.stats.TokenCounts...)
	s.statsMu.RUnlock()
	stats.Version = statsVersion
	if len(stats.MaskCounts) != maskCount || len(stats.TokenCounts) != maskCount {
		return fmt.Errorf("invalid criteria mask count")
	}
	data, errMarshal := json.MarshalIndent(stats, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	return os.WriteFile(s.statsPath, data, 0o600)
}

func (s *Store) countsForRange(timeRange TimeRange) ([]uint64, []uint64, uint64, time.Time, error) {
	if timeRange.From == nil && timeRange.To == nil {
		s.statsMu.RLock()
		counts := append([]uint64(nil), s.stats.MaskCounts...)
		tokens := append([]uint64(nil), s.stats.TokenCounts...)
		total := s.stats.Total
		updatedAt := s.stats.UpdatedAt
		s.statsMu.RUnlock()
		return counts, tokens, total, updatedAt, nil
	}

	days, errDays := s.dayNames()
	if errDays != nil {
		return nil, nil, 0, time.Time{}, errDays
	}
	counts := make([]uint64, maskCount)
	tokens := make([]uint64, maskCount)
	var total uint64
	var updatedAt time.Time
	for _, day := range days {
		stats := s.dayStatsSnapshot(day)
		for minute, minuteCounts := range stats.Minutes {
			timestamp, errParse := time.Parse("2006-01-02 1504", day+" "+minute)
			if errParse != nil || !timeRange.includesMinute(timestamp) {
				continue
			}
			if !timeRange.includesWholeMinute(timestamp) {
				path := filepath.Join(s.sessionsDir, day, minute+".jsonl")
				if errScan := scanShard(path, func(_ int64, _ []byte, mask uint8, capturedAt time.Time, tokenCount uint64) bool {
					if timeRange.includes(capturedAt) {
						counts[mask]++
						tokens[mask] += tokenCount
						total++
						if capturedAt.After(updatedAt) {
							updatedAt = capturedAt
						}
					}
					return true
				}); errScan != nil {
					return nil, nil, 0, time.Time{}, errScan
				}
				continue
			}
			minuteTokens := stats.MinuteTokens[minute]
			for mask, count := range minuteCounts {
				if mask >= maskCount {
					break
				}
				counts[mask] += count
				if mask < len(minuteTokens) {
					tokens[mask] += minuteTokens[mask]
				}
				total += count
			}
			if timestamp.After(updatedAt) {
				updatedAt = timestamp
			}
		}
	}
	return counts, tokens, total, updatedAt, nil
}

func (s *Store) dayNames() ([]string, error) {
	names := make(map[string]struct{})
	entries, errRead := os.ReadDir(s.dayStatsDir)
	if errRead != nil {
		return nil, fmt.Errorf("read daily data integration stats: %w", errRead)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		names[strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))] = struct{}{}
	}
	s.dayStatsMu.RLock()
	for day := range s.dayStats {
		names[day] = struct{}{}
	}
	s.dayStatsMu.RUnlock()

	days := make([]string, 0, len(names))
	for day := range names {
		days = append(days, day)
	}
	sort.Strings(days)
	return days, nil
}

func (s *Store) dayStatsSnapshot(day string) persistedDayStats {
	s.dayStatsMu.RLock()
	stats := s.dayStats[day]
	if stats != nil {
		snapshot := cloneDayStats(stats)
		s.dayStatsMu.RUnlock()
		return snapshot
	}
	s.dayStatsMu.RUnlock()
	return *s.loadDayStatsFile(day)
}

func cloneDayStats(stats *persistedDayStats) persistedDayStats {
	snapshot := persistedDayStats{
		Version:      stats.Version,
		Day:          stats.Day,
		Minutes:      make(map[string][]uint64, len(stats.Minutes)),
		MinuteTokens: make(map[string][]uint64, len(stats.MinuteTokens)),
	}
	for minute, counts := range stats.Minutes {
		snapshot.Minutes[minute] = append([]uint64(nil), counts...)
	}
	for minute, tokens := range stats.MinuteTokens {
		snapshot.MinuteTokens[minute] = append([]uint64(nil), tokens...)
	}
	return snapshot
}

func (s *Store) shardPaths() ([]string, error) {
	var shards []string
	errWalk := filepath.WalkDir(s.sessionsDir, func(path string, entry os.DirEntry, errWalk error) error {
		if errWalk != nil {
			return errWalk
		}
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
			shards = append(shards, path)
		}
		return nil
	})
	if errWalk != nil {
		return nil, fmt.Errorf("scan stored session shards: %w", errWalk)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(shards)))
	return shards, nil
}

func (s *Store) latestShardState() (string, int64, error) {
	dayEntries, errDays := os.ReadDir(s.sessionsDir)
	if errDays != nil {
		return "", 0, fmt.Errorf("read session days: %w", errDays)
	}
	for dayIndex := len(dayEntries) - 1; dayIndex >= 0; dayIndex-- {
		day := dayEntries[dayIndex]
		if !day.IsDir() {
			continue
		}
		dayPath := filepath.Join(s.sessionsDir, day.Name())
		shardEntries, errShards := os.ReadDir(dayPath)
		if errShards != nil {
			return "", 0, fmt.Errorf("read session shards: %w", errShards)
		}
		for shardIndex := len(shardEntries) - 1; shardIndex >= 0; shardIndex-- {
			shard := shardEntries[shardIndex]
			if shard.IsDir() || !strings.HasSuffix(strings.ToLower(shard.Name()), ".jsonl") {
				continue
			}
			path := filepath.Join(dayPath, shard.Name())
			info, errInfo := shard.Info()
			if errInfo != nil {
				return "", 0, fmt.Errorf("read latest session shard info: %w", errInfo)
			}
			return path, info.Size(), nil
		}
	}
	return "", 0, nil
}

func shardMinute(path string) (time.Time, bool) {
	day := filepath.Base(filepath.Dir(path))
	minute := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	timestamp, errParse := time.Parse("2006-01-02 1504", day+" "+minute)
	return timestamp, errParse == nil
}

func shardMayOverlap(path string, timeRange TimeRange) bool {
	minute, okMinute := shardMinute(path)
	if !okMinute {
		return false
	}
	if timeRange.From != nil && !minute.Add(time.Minute).After(timeRange.From.UTC()) {
		return false
	}
	if timeRange.To != nil && minute.After(timeRange.To.UTC()) {
		return false
	}
	return true
}

func (r TimeRange) includes(timestamp time.Time) bool {
	if timestamp.IsZero() {
		return false
	}
	timestamp = timestamp.UTC()
	if r.From != nil && timestamp.Before(r.From.UTC()) {
		return false
	}
	if r.To != nil && timestamp.After(r.To.UTC()) {
		return false
	}
	return true
}

func (r TimeRange) includesMinute(timestamp time.Time) bool {
	timestamp = timestamp.UTC().Truncate(time.Minute)
	if r.From != nil && timestamp.Before(r.From.UTC().Truncate(time.Minute)) {
		return false
	}
	if r.To != nil && timestamp.After(r.To.UTC().Truncate(time.Minute)) {
		return false
	}
	return true
}

func (r TimeRange) includesWholeMinute(timestamp time.Time) bool {
	timestamp = timestamp.UTC().Truncate(time.Minute)
	if r.From != nil && timestamp.Before(r.From.UTC()) {
		return false
	}
	lastNanosecond := timestamp.Add(time.Minute - time.Nanosecond)
	return r.To == nil || !lastNanosecond.After(r.To.UTC())
}

func selectNewestFromShard(path string, selectedMask uint8, limit int, timeRange TimeRange) ([]RecordRef, error) {
	capacity := limit
	if capacity > 16_384 {
		capacity = 16_384
	}
	refs := make([]RecordRef, 0, capacity)
	next := 0
	full := false
	errScan := scanShard(path, func(offset int64, line []byte, mask uint8, capturedAt time.Time, _ uint64) bool {
		if mask&selectedMask == selectedMask && timeRange.includes(capturedAt) {
			ref := RecordRef{offset: offset, length: len(line)}
			if len(refs) < limit {
				refs = append(refs, ref)
			} else {
				refs[next] = ref
				next = (next + 1) % limit
				full = true
			}
		}
		return true
	})
	if errScan != nil {
		return nil, errScan
	}
	if full && next > 0 {
		ordered := make([]RecordRef, 0, len(refs))
		ordered = append(ordered, refs[next:]...)
		ordered = append(ordered, refs[:next]...)
		refs = ordered
	}
	for left, right := 0, len(refs)-1; left < right; left, right = left+1, right-1 {
		refs[left], refs[right] = refs[right], refs[left]
	}
	return refs, nil
}

func scanShard(path string, visit func(offset int64, line []byte, mask uint8, capturedAt time.Time, tokenCount uint64) bool) error {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return fmt.Errorf("open session shard: %w", errOpen)
	}
	defer func() {
		_ = file.Close()
	}()

	reader := bufio.NewReaderSize(file, 256<<10)
	var offset int64
	for {
		line, errRead := reader.ReadBytes('\n')
		if len(line) > 0 {
			var header struct {
				CapturedAt time.Time `json:"captured_at"`
				Evaluation struct {
					ValidatorVersion int    `json:"validator_version"`
					Mask             uint8  `json:"criteria_mask"`
					TokenCount       uint64 `json:"token_count"`
				} `json:"evaluation"`
			}
			if errUnmarshal := json.Unmarshal(line, &header); errUnmarshal == nil {
				mask := header.Evaluation.Mask
				tokenCount := header.Evaluation.TokenCount
				if !compatibleValidatorVersion(header.Evaluation.ValidatorVersion) || tokenCount == 0 {
					var legacy struct {
						Path    string          `json:"path"`
						Payload json.RawMessage `json:"payload"`
					}
					if errLegacy := json.Unmarshal(line, &legacy); errLegacy == nil && json.Valid(legacy.Payload) {
						enriched, _ := enrichNativeMetadata(legacy.Payload, legacy.Path, "")
						if evaluation, errEvaluate := Evaluate(enriched); errEvaluate == nil {
							mask = evaluation.Mask
							tokenCount = evaluation.TokenCount
						}
					}
				}
				if !visit(offset, line, mask, header.CapturedAt, tokenCount) {
					return nil
				}
			}
			offset += int64(len(line))
		}
		if errors.Is(errRead, io.EOF) {
			return nil
		}
		if errRead != nil {
			return fmt.Errorf("read session shard: %w", errRead)
		}
	}
}

func readStoredPayload(file *os.File, record RecordRef) ([]byte, error) {
	line := make([]byte, record.length)
	if _, errRead := file.ReadAt(line, record.offset); errRead != nil {
		return nil, fmt.Errorf("read stored session: %w", errRead)
	}
	var stored StoredRecord
	if errUnmarshal := json.Unmarshal(line, &stored); errUnmarshal != nil {
		return nil, fmt.Errorf("decode stored session: %w", errUnmarshal)
	}
	if !json.Valid(stored.Payload) {
		return nil, fmt.Errorf("stored session payload is invalid JSON")
	}
	enriched, errEnrich := enrichNativeMetadata(stored.Payload, stored.Path, "")
	if errEnrich != nil {
		return nil, fmt.Errorf("add native request metadata: %w", errEnrich)
	}
	return enriched, nil
}

func matchedForMask(counts []uint64, selectedMask uint8) uint64 {
	var matched uint64
	for mask, count := range counts {
		if uint8(mask)&selectedMask == selectedMask {
			matched += count
		}
	}
	return matched
}

func percentage(value, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(value) * 100 / float64(total)
}
