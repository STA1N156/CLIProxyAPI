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
	if stats.MatchedRequests != 1 || stats.MatchedTokens == 0 {
		t.Fatalf("matched requests/tokens = %d/%d", stats.MatchedRequests, stats.MatchedTokens)
	}

	var output bytes.Buffer
	if errExport := store.WriteZIP(&output, 1, evaluation.Mask, TimeRange{}, "json"); errExport != nil {
		t.Fatal(errExport)
	}
	archive, errZIP := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if errZIP != nil {
		t.Fatal(errZIP)
	}
	if len(archive.File) != 1 {
		t.Fatalf("ZIP files = %d, want 1", len(archive.File))
	}
}
