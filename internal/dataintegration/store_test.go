package dataintegration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreFiltersAndWritesOneFilePerSessionZIP(t *testing.T) {
	t.Parallel()
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	payload := validOpenAISession(t)
	var expectedTokens uint64
	for index := 0; index < 3; index++ {
		var session map[string]any
		if errUnmarshal := json.Unmarshal(payload, &session); errUnmarshal != nil {
			t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
		}
		session["sequence"] = index
		encoded, errMarshal := json.Marshal(session)
		if errMarshal != nil {
			t.Fatalf("json.Marshal() error = %v", errMarshal)
		}
		evaluation, errRecord := store.Record("/v1/chat/completions", fmt.Sprintf("request-%d", index), encoded)
		if errRecord != nil {
			t.Fatalf("Record() error = %v", errRecord)
		}
		expectedTokens += evaluation.TokenCount
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	mask, errMask := MaskForKeys([]string{CriterionEffectiveTurns, CriterionFirstRole})
	if errMask != nil {
		t.Fatalf("MaskForKeys() error = %v", errMask)
	}
	stats, errStats := store.Stats(mask, TimeRange{})
	if errStats != nil {
		t.Fatalf("Stats() error = %v", errStats)
	}
	if stats.TotalRequests != 3 || stats.MatchedRequests != 3 {
		t.Fatalf("stats total/matched = %d/%d, want 3/3", stats.TotalRequests, stats.MatchedRequests)
	}
	if stats.MatchedTokens != expectedTokens {
		t.Fatalf("matched tokens = %d, want %d", stats.MatchedTokens, expectedTokens)
	}

	var archive bytes.Buffer
	if errZIP := store.WriteZIP(&archive, 2, mask, TimeRange{}, "jsonl"); errZIP != nil {
		t.Fatalf("WriteZIP() error = %v", errZIP)
	}
	reader, errReader := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if errReader != nil {
		t.Fatalf("zip.NewReader() error = %v", errReader)
	}
	if len(reader.File) != 3 {
		t.Fatalf("zip entries = %d, want manifest + 2 sessions", len(reader.File))
	}
	for _, file := range reader.File {
		if file.Name == "manifest.json" {
			continue
		}
		if file.Name != "sessions/000001.jsonl" && file.Name != "sessions/000002.jsonl" {
			t.Fatalf("unexpected zip entry %q", file.Name)
		}
		handle, errOpen := file.Open()
		if errOpen != nil {
			t.Fatalf("open zip entry: %v", errOpen)
		}
		data, errRead := io.ReadAll(handle)
		_ = handle.Close()
		if errRead != nil {
			t.Fatalf("read zip entry: %v", errRead)
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			t.Fatalf("JSONL entry must contain one newline-terminated record")
		}
		var session struct {
			Sequence int `json:"sequence"`
		}
		if errUnmarshal := json.Unmarshal(data, &session); errUnmarshal != nil {
			t.Fatalf("decode JSONL entry: %v", errUnmarshal)
		}
		expected := 2
		if file.Name == "sessions/000002.jsonl" {
			expected = 1
		}
		if session.Sequence != expected {
			t.Fatalf("%s sequence = %d, want %d", file.Name, session.Sequence, expected)
		}
	}
}

func TestStoreExportsNativeAntigravityMetadata(t *testing.T) {
	t.Parallel()
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	if _, errRecord := store.RecordNative(
		"/v1beta/models/gemini-2.5-pro:generateContent",
		"proxy-request-id",
		"native-session-id",
		payload,
	); errRecord != nil {
		t.Fatalf("RecordNative() error = %v", errRecord)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	var archive bytes.Buffer
	if errZIP := store.WriteZIP(&archive, 1, 0, TimeRange{}, "json"); errZIP != nil {
		t.Fatalf("WriteZIP() error = %v", errZIP)
	}
	reader, errReader := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if errReader != nil {
		t.Fatalf("zip.NewReader() error = %v", errReader)
	}
	for _, file := range reader.File {
		if file.Name != "sessions/000001.json" {
			continue
		}
		handle, errOpen := file.Open()
		if errOpen != nil {
			t.Fatalf("open session: %v", errOpen)
		}
		data, errRead := io.ReadAll(handle)
		_ = handle.Close()
		if errRead != nil {
			t.Fatalf("read session: %v", errRead)
		}
		var session struct {
			Model     string `json:"model"`
			SessionID string `json:"session_id"`
			RequestID string `json:"request_id"`
		}
		if errUnmarshal := json.Unmarshal(data, &session); errUnmarshal != nil {
			t.Fatalf("decode session: %v", errUnmarshal)
		}
		if session.Model != "gemini-2.5-pro" || session.SessionID != "native-session-id" {
			t.Fatalf("model/session_id = %q/%q", session.Model, session.SessionID)
		}
		if session.RequestID != "" {
			t.Fatalf("proxy request_id leaked into exported data: %q", session.RequestID)
		}
		return
	}
	t.Fatal("exported session is missing")
}

func TestStorePreservesOptionalRequestFieldsInExport(t *testing.T) {
	t.Parallel()
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	payload := []byte(`{
		"model":"gemini-3-flash",
		"session_id":"session-1",
		"metadata":{"provider":"antigravity","timestamp":"2026-07-27T12:00:00Z"},
		"task_type":"code",
		"domain":"software_engineering",
		"thinking":{"summary":"inspect the repository"},
		"reasoning":"use the repository result"
	}`)
	if _, errRecord := store.Record("/v1beta/models/gemini-3-flash:generateContent", "", payload); errRecord != nil {
		t.Fatalf("Record() error = %v", errRecord)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	var archive bytes.Buffer
	if errZIP := store.WriteZIP(&archive, 1, 0, TimeRange{}, "json"); errZIP != nil {
		t.Fatalf("WriteZIP() error = %v", errZIP)
	}
	reader, errReader := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if errReader != nil {
		t.Fatalf("zip.NewReader() error = %v", errReader)
	}
	for _, file := range reader.File {
		if file.Name != "sessions/000001.json" {
			continue
		}
		handle, errOpen := file.Open()
		if errOpen != nil {
			t.Fatalf("open session: %v", errOpen)
		}
		data, errRead := io.ReadAll(handle)
		_ = handle.Close()
		if errRead != nil {
			t.Fatalf("read session: %v", errRead)
		}
		var session map[string]any
		if errUnmarshal := json.Unmarshal(data, &session); errUnmarshal != nil {
			t.Fatalf("decode session: %v", errUnmarshal)
		}
		for _, key := range []string{"metadata", "task_type", "domain", "thinking", "reasoning"} {
			if value, exists := session[key]; !exists || value == nil {
				t.Fatalf("%s was not preserved in exported session", key)
			}
		}
		return
	}
	t.Fatal("exported session is missing")
}

func TestStoreConcurrentBurstAndTimeRange(t *testing.T) {
	t.Parallel()
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	payload := validOpenAISession(t)

	const total = 1000
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for index := worker; index < total; index += 16 {
				if _, errRecord := store.Record("/v1/responses", fmt.Sprintf("request-%d", index), payload); errRecord != nil {
					t.Errorf("Record() error = %v", errRecord)
					return
				}
			}
		}(worker)
	}
	workers.Wait()
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	all, errAll := store.Stats(0, TimeRange{})
	if errAll != nil {
		t.Fatalf("Stats(all) error = %v", errAll)
	}
	if all.TotalRequests != total {
		t.Fatalf("total requests = %d, want %d", all.TotalRequests, total)
	}

	future := time.Now().UTC().Add(time.Hour)
	filtered, errFiltered := store.Stats(0, TimeRange{From: &future})
	if errFiltered != nil {
		t.Fatalf("Stats(filtered) error = %v", errFiltered)
	}
	if filtered.TotalRequests != 0 {
		t.Fatalf("future range total = %d, want 0", filtered.TotalRequests)
	}
}

func TestStoreClearRemovesExistingDataAndKeepsNewRequests(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, errStore := NewStore(root)
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	payload := validOpenAISession(t)
	for index := 0; index < 2; index++ {
		if _, errRecord := store.Record("/v1/responses", fmt.Sprintf("before-%d", index), payload); errRecord != nil {
			t.Fatalf("Record(before clear) error = %v", errRecord)
		}
	}

	cleared, errClear := store.Clear(context.Background())
	if errClear != nil {
		t.Fatalf("Clear() error = %v", errClear)
	}
	if cleared.RemovedRequests != 2 || cleared.ClearedAt.IsZero() {
		t.Fatalf("Clear() result = %+v, want 2 removed requests", cleared)
	}
	empty, errStats := store.Stats(0, TimeRange{})
	if errStats != nil || empty.TotalRequests != 0 {
		t.Fatalf("Stats(after clear) total/error = %d/%v", empty.TotalRequests, errStats)
	}
	shards, errShards := store.shardPaths()
	if errShards != nil || len(shards) != 0 {
		t.Fatalf("shards after clear = %v, %v", shards, errShards)
	}

	if _, errRecord := store.Record("/v1/responses", "after-clear", payload); errRecord != nil {
		t.Fatalf("Record(after clear) error = %v", errRecord)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	remaining, errRemaining := store.Stats(0, TimeRange{})
	if errRemaining != nil || remaining.TotalRequests != 1 {
		t.Fatalf("Stats(final) total/error = %d/%v", remaining.TotalRequests, errRemaining)
	}
}

func TestStoreTimeRangeUsesExactBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	minute := time.Date(2026, 7, 27, 12, 30, 0, 0, time.UTC)
	path := filepath.Join(root, "sessions", "2026-07-27", "1230.jsonl")
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	for _, capturedAt := range []time.Time{minute.Add(10 * time.Second), minute.Add(50 * time.Second)} {
		record := StoredRecord{
			CapturedAt: capturedAt,
			Path:       "/v1/responses",
			Evaluation: Evaluation{Mask: 1},
			Payload:    []byte(`{"input":[]}`),
		}
		data, errMarshal := json.Marshal(record)
		if errMarshal != nil {
			t.Fatalf("json.Marshal() error = %v", errMarshal)
		}
		if errAppend := appendShard(path, append(data, '\n')); errAppend != nil {
			t.Fatalf("appendShard() error = %v", errAppend)
		}
	}

	store, errStore := NewStore(root)
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	from := minute.Add(20 * time.Second)
	to := minute.Add(40 * time.Second)
	stats, errStats := store.Stats(0, TimeRange{From: &from, To: &to})
	if errStats != nil {
		t.Fatalf("Stats() error = %v", errStats)
	}
	if stats.TotalRequests != 0 {
		t.Fatalf("boundary-filtered requests = %d, want 0", stats.TotalRequests)
	}
	includedFrom := minute.Add(5 * time.Second)
	includedTo := minute.Add(20 * time.Second)
	included, errIncluded := store.Stats(0, TimeRange{From: &includedFrom, To: &includedTo})
	if errIncluded != nil {
		t.Fatalf("Stats(included) error = %v", errIncluded)
	}
	if included.TotalRequests != 1 || included.MatchedTokens == 0 {
		t.Fatalf("included requests/tokens = %d/%d, want 1/positive", included.TotalRequests, included.MatchedTokens)
	}
}

func TestStoreRebuildsStatsWhenShardOutrunsSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, errStore := NewStore(root)
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	payload := validOpenAISession(t)
	if _, errRecord := store.Record("/v1/responses", "request-1", payload); errRecord != nil {
		t.Fatalf("Record() error = %v", errRecord)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	shards, errShards := store.shardPaths()
	if errShards != nil || len(shards) != 1 {
		t.Fatalf("shardPaths() = %v, %v", shards, errShards)
	}
	line, errRead := os.ReadFile(shards[0])
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if errAppend := appendShard(shards[0], line); errAppend != nil {
		t.Fatalf("appendShard() error = %v", errAppend)
	}

	reopened, errReopen := NewStore(root)
	if errReopen != nil {
		t.Fatalf("NewStore(reopen) error = %v", errReopen)
	}
	stats, errStats := reopened.Stats(0, TimeRange{})
	if errStats != nil {
		t.Fatalf("Stats() error = %v", errStats)
	}
	if stats.TotalRequests != 2 {
		t.Fatalf("recovered requests = %d, want 2", stats.TotalRequests)
	}
}

func TestStoreLoadsCompatiblePreviousStatsVersions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, errStore := NewStore(root)
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	if _, errRecord := store.Record("/v1/responses", "request-1", validOpenAISession(t)); errRecord != nil {
		t.Fatalf("Record() error = %v", errRecord)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	statsData, errStatsRead := os.ReadFile(store.statsPath)
	if errStatsRead != nil {
		t.Fatalf("ReadFile(stats) error = %v", errStatsRead)
	}
	var stats persistedStats
	if errUnmarshal := json.Unmarshal(statsData, &stats); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal(stats) error = %v", errUnmarshal)
	}
	stats.Version = minStatsVersion
	statsData, errStatsMarshal := json.Marshal(stats)
	if errStatsMarshal != nil {
		t.Fatalf("json.Marshal(stats) error = %v", errStatsMarshal)
	}
	if errWrite := os.WriteFile(store.statsPath, statsData, 0o600); errWrite != nil {
		t.Fatalf("WriteFile(stats) error = %v", errWrite)
	}

	dayFiles, errDayFiles := filepath.Glob(filepath.Join(store.dayStatsDir, "*.json"))
	if errDayFiles != nil || len(dayFiles) != 1 {
		t.Fatalf("day stats files = %v, %v", dayFiles, errDayFiles)
	}
	dayData, errDayRead := os.ReadFile(dayFiles[0])
	if errDayRead != nil {
		t.Fatalf("ReadFile(day stats) error = %v", errDayRead)
	}
	var dayStats persistedDayStats
	if errUnmarshal := json.Unmarshal(dayData, &dayStats); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal(day stats) error = %v", errUnmarshal)
	}
	dayStats.Version = minDayStatsVersion
	dayData, errDayMarshal := json.Marshal(dayStats)
	if errDayMarshal != nil {
		t.Fatalf("json.Marshal(day stats) error = %v", errDayMarshal)
	}
	if errWrite := os.WriteFile(dayFiles[0], dayData, 0o600); errWrite != nil {
		t.Fatalf("WriteFile(day stats) error = %v", errWrite)
	}

	reopened, errReopen := NewStore(root)
	if errReopen != nil {
		t.Fatalf("NewStore(reopen) error = %v", errReopen)
	}
	all, errAll := reopened.Stats(0, TimeRange{})
	if errAll != nil || all.TotalRequests != 1 {
		t.Fatalf("Stats(all) total/error = %d/%v", all.TotalRequests, errAll)
	}
	from := time.Now().UTC().Add(-time.Hour)
	ranged, errRanged := reopened.Stats(0, TimeRange{From: &from})
	if errRanged != nil || ranged.TotalRequests != 1 {
		t.Fatalf("Stats(range) total/error = %d/%v", ranged.TotalRequests, errRanged)
	}
	persistedData, errPersistedRead := os.ReadFile(reopened.statsPath)
	if errPersistedRead != nil {
		t.Fatalf("ReadFile(reopened stats) error = %v", errPersistedRead)
	}
	var persisted persistedStats
	if errUnmarshal := json.Unmarshal(persistedData, &persisted); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal(reopened stats) error = %v", errUnmarshal)
	}
	if persisted.Version != minStatsVersion {
		t.Fatalf("compatible stats were rebuilt: version = %d", persisted.Version)
	}
}

func TestStoreRevalidatesOlderRecords(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, errStore := NewStore(root)
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	payload := []byte(`{
		"messages": [
			{"role":"user","content":"look it up"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup_value"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"result"}
		]
	}`)
	if _, errRecord := store.Record("/v1/responses", "request-1", payload); errRecord != nil {
		t.Fatalf("Record() error = %v", errRecord)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	shards, errShards := store.shardPaths()
	if errShards != nil || len(shards) != 1 {
		t.Fatalf("shardPaths() = %v, %v", shards, errShards)
	}
	line, errRead := os.ReadFile(shards[0])
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var record StoredRecord
	if errUnmarshal := json.Unmarshal(line, &record); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	record.Evaluation.ValidatorVersion = minValidatorVersion - 1
	record.Evaluation.Mask = 63
	legacyLine, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	legacyLine = append(legacyLine, '\n')
	if errWrite := os.WriteFile(shards[0], legacyLine, 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	if errRemove := os.Remove(store.statsPath); errRemove != nil {
		t.Fatalf("Remove(stats) error = %v", errRemove)
	}

	reopened, errReopen := NewStore(root)
	if errReopen != nil {
		t.Fatalf("NewStore(reopen) error = %v", errReopen)
	}
	stats, errStats := reopened.Stats(bitFor(CriterionToolCall), TimeRange{})
	if errStats != nil {
		t.Fatalf("Stats() error = %v", errStats)
	}
	if stats.MatchedRequests != 0 {
		t.Fatalf("revalidated matching requests = %d, want 0", stats.MatchedRequests)
	}
}

func BenchmarkStoreRecord(b *testing.B) {
	store, errStore := NewStore(b.TempDir())
	if errStore != nil {
		b.Fatalf("NewStore() error = %v", errStore)
	}
	payload := validOpenAISessionForBenchmark(b)
	b.ResetTimer()
	b.RunParallel(func(parallel *testing.PB) {
		for parallel.Next() {
			if _, errRecord := store.Record("/v1/responses", "", payload); errRecord != nil {
				b.Errorf("Record() error = %v", errRecord)
				return
			}
		}
	})
	b.StopTimer()
	if errClose := store.Close(context.Background()); errClose != nil {
		b.Fatalf("Close() error = %v", errClose)
	}
}

func validOpenAISessionForBenchmark(b *testing.B) []byte {
	b.Helper()
	return []byte(`{"model":"gpt-5","tools":[{"type":"function","function":{"name":"search_code","description":"Search source code","parameters":{"type":"object","properties":{"query":{"type":"string","description":"Search query"}}}}}],"messages":[{"role":"system","content":"coding agent"},{"role":"user","content":"find handler"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_code","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"},{"role":"user","content":"open it"},{"role":"assistant","content":"done"}]}`)
}
