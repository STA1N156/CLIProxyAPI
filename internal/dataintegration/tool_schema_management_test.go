package dataintegration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestToolSchemaImportAndBackfillUsesCompatibleVersion(t *testing.T) {
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	defer func() {
		if errClose := store.Close(context.Background()); errClose != nil {
			t.Errorf("Close() error = %v", errClose)
		}
	}()

	payload := []byte(`{
		"model":"gemini-3.5-flash",
		"messages":[
			{"role":"user","content":"first task"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"TodoWrite","arguments":"{\"items\":[]}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"user","content":"second task"},
			{"role":"assistant","content":"done"}
		]
	}`)
	if _, errRecord := store.Record("/v1/chat/completions", "request-1", payload); errRecord != nil {
		t.Fatalf("Record() error = %v", errRecord)
	}

	registry := persistedToolSchemaTable{
		Version: toolSchemaTableVersion,
		Tools: map[string]persistedToolSchemaSet{
			"TodoWrite": {
				Versions: []persistedToolSchemaVersion{
					{Definition: mustToolDefinition(t, "TodoWrite", "todos", "Todo list"), ObservedCount: 10},
					{Definition: mustToolDefinition(t, "TodoWrite", "items", "Items list"), ObservedCount: 8},
					{Definition: mustToolDefinition(t, "TodoWrite", "items", "Alternate wording"), ObservedCount: 1},
				},
			},
		},
	}
	registryJSON, errMarshal := json.Marshal(registry)
	if errMarshal != nil {
		t.Fatalf("marshal registry: %v", errMarshal)
	}
	imported, errImport := store.ImportToolSchemas(registryJSON)
	if errImport != nil {
		t.Fatalf("ImportToolSchemas() error = %v", errImport)
	}
	if imported.AddedTools != 1 || imported.TotalVersions != 2 {
		t.Fatalf("import result = %+v, want one tool and two compatible signatures", imported)
	}

	mask, errMask := MaskForKeys([]string{
		CriterionToolCall,
		CriterionToolSchema,
		CriterionToolPairing,
		CriterionMachineRatio,
	})
	if errMask != nil {
		t.Fatal(errMask)
	}
	backfilled, errBackfill := store.BackfillToolSchemas(context.Background(), mask, TimeRange{})
	if errBackfill != nil {
		t.Fatalf("BackfillToolSchemas() error = %v", errBackfill)
	}
	if backfilled.PromotedSessions != 1 || backfilled.AddedDefinitions != 1 {
		t.Fatalf("backfill result = %+v, want one promoted session and definition", backfilled)
	}

	stats, errStats := store.Stats(mask, TimeRange{})
	if errStats != nil {
		t.Fatalf("Stats() error = %v", errStats)
	}
	if stats.MatchedRequests != 1 {
		t.Fatalf("matched requests = %d, want 1", stats.MatchedRequests)
	}

	var output bytes.Buffer
	if errZIP := store.WriteZIP(&output, 1, mask, TimeRange{}, "json"); errZIP != nil {
		t.Fatalf("WriteZIP() error = %v", errZIP)
	}
	archive, errZIP := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if errZIP != nil {
		t.Fatalf("open ZIP: %v", errZIP)
	}
	session, errOpen := archive.File[1].Open()
	if errOpen != nil {
		t.Fatalf("open exported session: %v", errOpen)
	}
	exported, errRead := io.ReadAll(session)
	_ = session.Close()
	if errRead != nil {
		t.Fatalf("read exported session: %v", errRead)
	}
	evaluation, errEvaluate := Evaluate(exported)
	if errEvaluate != nil || evaluation.Mask&bitFor(CriterionToolSchema) == 0 {
		t.Fatalf("persisted export did not pass schema validation: mask=%d err=%v", evaluation.Mask, errEvaluate)
	}
	if !bytes.Contains(exported, []byte(`"items"`)) {
		t.Fatal("backfill did not choose the items-compatible schema")
	}
}

func TestPutToolSchemaRejectsIncompleteDefinition(t *testing.T) {
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	defer func() {
		_ = store.Close(context.Background())
	}()
	incomplete := json.RawMessage(`{"name":"Read","description":"Read file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}`)
	if _, errPut := store.PutToolSchema("Read", incomplete); errPut == nil {
		t.Fatal("incomplete nested property must be rejected")
	}
}

func TestToolSchemaTablePreservesAllDeclaredTools(t *testing.T) {
	table := newToolSchemaTable()
	payload := []byte(`{
		"tools":[
			{"type":"function","function":{"name":"Read","description":"Read file","parameters":{"type":"object","properties":{}}}},
			{"type":"function","function":{"name":"DeclaredForLater","description":"Future tool","parameters":{"type":"object","properties":{}}}}
		],
		"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}]
	}`)
	if _, errObserve := table.observeAndEnrich(payload, time.Now().UTC()); errObserve != nil {
		t.Fatalf("observeAndEnrich() error = %v", errObserve)
	}
	tools, versions := table.counts()
	if tools != 2 || versions != 2 {
		t.Fatalf("schema counts = %d/%d, want every declared tool", tools, versions)
	}
}

func mustToolDefinition(
	t *testing.T,
	name, field, fieldDescription string,
) json.RawMessage {
	t.Helper()
	definition := map[string]any{
		"name":        name,
		"description": "Update a list",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				field: map[string]any{
					"type":        "array",
					"description": fieldDescription,
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []any{field},
		},
	}
	encoded, errMarshal := json.Marshal(definition)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return encoded
}
