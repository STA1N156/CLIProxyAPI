package dataintegration

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"
)

func TestStoreRecordsFiltersAndExports(t *testing.T) {
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatal(errStore)
	}
	if _, errRecord := store.Record("/v1/responses", "too-short", []byte(`{"input":"hello"}`)); errRecord != nil {
		t.Fatal(errRecord)
	}
	if _, errRecord := store.Record("/v1/responses", "bad-first-role", []byte(`{"messages":[{"role":"assistant","content":"bad start"},{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"three"},{"role":"assistant","content":"four"}]}`)); errRecord != nil {
		t.Fatal(errRecord)
	}
	evaluation, errRecord := store.Record("/v1/responses", "request-1", validOpenAISession(t))
	if errRecord != nil {
		t.Fatal(errRecord)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatal(errClose)
	}

	stats, errStats := store.Stats(evaluation.Mask, TimeRange{})
	if errStats != nil {
		t.Fatal(errStats)
	}
	if stats.TotalRequests != 1 || stats.MatchedRequests != 1 || stats.MatchedTokens == 0 {
		t.Fatalf("total/matched/tokens = %d/%d/%d", stats.TotalRequests, stats.MatchedRequests, stats.MatchedTokens)
	}

	var output bytes.Buffer
	if errExport := store.WriteZIP(&output, 1, evaluation.Mask, TimeRange{}, "json"); errExport != nil {
		t.Fatal(errExport)
	}
	archive, errZIP := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if errZIP != nil {
		t.Fatal(errZIP)
	}
	if len(archive.File) != 2 {
		t.Fatalf("ZIP files = %d, want 2", len(archive.File))
	}
}
