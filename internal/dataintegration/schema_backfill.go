package dataintegration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ToolSchemaBackfillResult describes one explicit stored-session maintenance run.
type ToolSchemaBackfillResult struct {
	ExaminedSessions        uint64 `json:"examined_sessions"`
	EligibleSessions        uint64 `json:"eligible_sessions"`
	AlreadyQualified        uint64 `json:"already_qualified"`
	PromotedSessions        uint64 `json:"promoted_sessions"`
	AddedDefinitions        uint64 `json:"added_definitions"`
	RemainingSchemaFailures uint64 `json:"remaining_schema_failures"`
	PrunedSchemaVersions    int    `json:"pruned_schema_versions"`
}

type toolSchemaBackfillRequest struct {
	mask      uint8
	timeRange TimeRange
	response  chan toolSchemaBackfillResponse
}

type toolSchemaBackfillResponse struct {
	result ToolSchemaBackfillResult
	err    error
}

// BackfillToolSchemas enriches stored sessions matching the current filters,
// persists the enriched payloads, and rebuilds statistics.
func (s *Store) BackfillToolSchemas(
	ctx context.Context,
	selectedMask uint8,
	timeRange TimeRange,
) (ToolSchemaBackfillResult, error) {
	if s == nil {
		return ToolSchemaBackfillResult{}, fmt.Errorf("data integration store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errWarmup := s.warmupStatus(); errWarmup != nil {
		return ToolSchemaBackfillResult{}, errWarmup
	}
	if errInit := s.ensureInitialized(); errInit != nil {
		return ToolSchemaBackfillResult{}, errInit
	}

	request := &toolSchemaBackfillRequest{
		mask:      selectedMask,
		timeRange: timeRange,
		response:  make(chan toolSchemaBackfillResponse, 1),
	}
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return ToolSchemaBackfillResult{}, fmt.Errorf("data integration store is closed")
	}
	s.startPipelineLocked()
	s.active.Add(1)
	s.stateMu.Unlock()
	defer s.active.Done()

	select {
	case s.queue <- pendingRecord{backfill: request}:
	case <-ctx.Done():
		return ToolSchemaBackfillResult{}, ctx.Err()
	}
	select {
	case response := <-request.response:
		return response.result, response.err
	case <-ctx.Done():
		return ToolSchemaBackfillResult{}, ctx.Err()
	}
}

func (s *Store) backfillToolSchemas(
	selectedMask uint8,
	timeRange TimeRange,
) (ToolSchemaBackfillResult, error) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()

	baseMask := (selectedMask &^ bitFor(CriterionToolSchema)) | storageRequirementMask
	schemaBit := bitFor(CriterionToolSchema)
	result := ToolSchemaBackfillResult{}
	result.PrunedSchemaVersions = s.schemaTable.compact()
	if result.PrunedSchemaVersions > 0 {
		if errWrite := s.writeToolSchemas(); errWrite != nil {
			return result, errWrite
		}
	}
	shards, errShards := s.shardPaths()
	if errShards != nil {
		return result, errShards
	}
	for _, path := range shards {
		if !shardMayOverlap(path, timeRange) {
			continue
		}
		shardResult, errRewrite := s.backfillToolSchemaShard(path, baseMask, schemaBit, timeRange)
		if errRewrite != nil {
			return result, errRewrite
		}
		result.ExaminedSessions += shardResult.ExaminedSessions
		result.EligibleSessions += shardResult.EligibleSessions
		result.AlreadyQualified += shardResult.AlreadyQualified
		result.PromotedSessions += shardResult.PromotedSessions
		result.AddedDefinitions += shardResult.AddedDefinitions
		result.RemainingSchemaFailures += shardResult.RemainingSchemaFailures
	}
	if errRebuild := s.rebuildStatsAfterBackfill(); errRebuild != nil {
		return result, errRebuild
	}
	return result, nil
}

func (s *Store) backfillToolSchemaShard(
	path string,
	baseMask, schemaBit uint8,
	timeRange TimeRange,
) (ToolSchemaBackfillResult, error) {
	result := ToolSchemaBackfillResult{}
	source, errOpen := os.Open(path)
	if errOpen != nil {
		return result, fmt.Errorf("open session shard for schema backfill: %w", errOpen)
	}
	defer source.Close()

	temporary, errTemp := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-schema-*")
	if errTemp != nil {
		return result, fmt.Errorf("create schema backfill file: %w", errTemp)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	reader := bufio.NewReaderSize(source, 256<<10)
	writer := bufio.NewWriterSize(temporary, 256<<10)
	changed := false
	for {
		line, errRead := reader.ReadBytes('\n')
		if len(line) > 0 {
			result.ExaminedSessions++
			rewritten, lineChanged, lineResult := s.backfillToolSchemaLine(
				line,
				baseMask,
				schemaBit,
				timeRange,
			)
			result.EligibleSessions += lineResult.EligibleSessions
			result.AlreadyQualified += lineResult.AlreadyQualified
			result.PromotedSessions += lineResult.PromotedSessions
			result.AddedDefinitions += lineResult.AddedDefinitions
			result.RemainingSchemaFailures += lineResult.RemainingSchemaFailures
			if _, errWrite := writer.Write(rewritten); errWrite != nil {
				return result, fmt.Errorf("write schema backfill file: %w", errWrite)
			}
			changed = changed || lineChanged
		}
		if errors.Is(errRead, io.EOF) {
			break
		}
		if errRead != nil {
			return result, fmt.Errorf("read session shard for schema backfill: %w", errRead)
		}
	}
	if errFlush := writer.Flush(); errFlush != nil {
		return result, fmt.Errorf("flush schema backfill file: %w", errFlush)
	}
	if errSync := temporary.Sync(); errSync != nil {
		return result, fmt.Errorf("sync schema backfill file: %w", errSync)
	}
	if errClose := temporary.Close(); errClose != nil {
		return result, fmt.Errorf("close schema backfill file: %w", errClose)
	}
	if errClose := source.Close(); errClose != nil {
		return result, fmt.Errorf("close source session shard: %w", errClose)
	}
	if !changed {
		return result, nil
	}
	if errReplace := replaceSchemaBackfillFile(path, temporaryPath); errReplace != nil {
		return result, errReplace
	}
	temporaryPath = ""
	return result, nil
}

func (s *Store) backfillToolSchemaLine(
	line []byte,
	baseMask, schemaBit uint8,
	timeRange TimeRange,
) ([]byte, bool, ToolSchemaBackfillResult) {
	result := ToolSchemaBackfillResult{}
	var record StoredRecord
	if json.Unmarshal(line, &record) != nil || !json.Valid(record.Payload) ||
		!timeRange.includes(record.CapturedAt) {
		return line, false, result
	}
	evaluation, errEvaluate := Evaluate(record.Payload)
	if errEvaluate != nil || evaluation.Mask&baseMask != baseMask {
		return line, false, result
	}
	result.EligibleSessions = 1
	if evaluation.Mask&schemaBit != 0 {
		result.AlreadyQualified = 1
		rewritten, changed := updateStoredEvaluation(line, record, evaluation)
		return rewritten, changed, result
	}

	enriched, errEnrich := s.schemaTable.enrich(record.Payload)
	if errEnrich != nil {
		result.RemainingSchemaFailures = 1
		rewritten, changed := updateStoredEvaluation(line, record, evaluation)
		return rewritten, changed, result
	}
	enrichedEvaluation, errEvaluate := Evaluate(enriched)
	if errEvaluate != nil || enrichedEvaluation.Mask&schemaBit == 0 {
		result.RemainingSchemaFailures = 1
		rewritten, changed := updateStoredEvaluation(line, record, evaluation)
		return rewritten, changed, result
	}
	beforeDefinitions := countPayloadToolDefinitions(record.Payload)
	afterDefinitions := countPayloadToolDefinitions(enriched)
	if afterDefinitions > beforeDefinitions {
		result.AddedDefinitions = uint64(afterDefinitions - beforeDefinitions)
	}
	result.PromotedSessions = 1
	record.Payload = append(json.RawMessage(nil), enriched...)
	rewritten, changed := marshalStoredRecord(line, record, enrichedEvaluation)
	return rewritten, changed, result
}

func updateStoredEvaluation(
	original []byte,
	record StoredRecord,
	evaluation Evaluation,
) ([]byte, bool) {
	if record.Evaluation == evaluation {
		return original, false
	}
	return marshalStoredRecord(original, record, evaluation)
}

func marshalStoredRecord(original []byte, record StoredRecord, evaluation Evaluation) ([]byte, bool) {
	record.Evaluation = evaluation
	encoded, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		return original, false
	}
	return append(encoded, '\n'), true
}

func countPayloadToolDefinitions(payload []byte) int {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return 0
	}
	return len(extractToolDefinitions(root))
}

func replaceSchemaBackfillFile(path, temporaryPath string) error {
	backupPath := path + ".schema-backfill-backup"
	_ = os.Remove(backupPath)
	if errBackup := os.Rename(path, backupPath); errBackup != nil {
		return fmt.Errorf("backup session shard for schema backfill: %w", errBackup)
	}
	if errReplace := os.Rename(temporaryPath, path); errReplace != nil {
		_ = os.Rename(backupPath, path)
		return fmt.Errorf("replace session shard after schema backfill: %w", errReplace)
	}
	if errRemove := os.Remove(backupPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		return fmt.Errorf("remove schema backfill backup: %w", errRemove)
	}
	return nil
}

func (s *Store) rebuildStatsAfterBackfill() error {
	if errRemove := os.Remove(s.statsPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		return fmt.Errorf("reset statistics after schema backfill: %w", errRemove)
	}
	if errRemove := os.RemoveAll(s.dayStatsDir); errRemove != nil {
		return fmt.Errorf("reset daily statistics after schema backfill: %w", errRemove)
	}
	if errMkdir := os.MkdirAll(s.dayStatsDir, 0o700); errMkdir != nil {
		return fmt.Errorf("recreate daily statistics after schema backfill: %w", errMkdir)
	}
	s.statsMu.Lock()
	s.stats = persistedStats{}
	s.statsMu.Unlock()
	s.dayStatsMu.Lock()
	s.dayStats = make(map[string]*persistedDayStats)
	s.dirtyDays = make(map[string]struct{})
	s.dayStatsMu.Unlock()
	return s.loadOrRebuildStats()
}
