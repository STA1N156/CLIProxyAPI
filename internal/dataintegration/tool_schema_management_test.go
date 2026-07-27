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
			{"type":"function","function":{"name":"DeclaredForLater","description":"Future tool","parameters":{"type":"object","properties":{}}}},
			{"type":"function","function":{"name":"DeclaredIncomplete","parameters":{"type":"object","properties":{}}}}
		],
		"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}]
	}`)
	if _, errObserve := table.observeAndEnrich(payload, time.Now().UTC()); errObserve != nil {
		t.Fatalf("observeAndEnrich() error = %v", errObserve)
	}
	tools, versions := table.counts()
	if tools != 3 || versions != 3 {
		t.Fatalf("schema counts = %d/%d, want every declared tool", tools, versions)
	}
	completeTools, completeVersions := table.completeCounts()
	if completeTools != 2 || completeVersions != 2 {
		t.Fatalf("complete schema counts = %d/%d, want only usable definitions", completeTools, completeVersions)
	}
}

func TestToolSchemaTableKeepsIncompleteDistinctSignatures(t *testing.T) {
	table := newToolSchemaTable()
	payload := []byte(`{
		"tools":[
			{"type":"function","function":{
				"name":"Edit",
				"description":"Edit a file",
				"parameters":{"type":"object","properties":{
					"file_path":{"type":"string","description":"File path"},
					"old_string":{"type":"string","description":"Text to replace"},
					"new_string":{"type":"string","description":"Replacement text"}
				},"required":["file_path","old_string","new_string"]}
			}},
			{"type":"function","function":{
				"name":"Edit",
				"description":"Edit a file",
				"parameters":{"type":"object","properties":{
					"file_path":{"type":"string"},
					"diff":{"type":"string","description":"Patch to apply"},
					"patch_mode":{"type":"string"}
				},"required":["file_path","diff","patch_mode"]}
			}}
		],
		"messages":[{"role":"user","content":"edit"}]
	}`)
	table.observeStoredDefinitions(payload, time.Now().UTC())

	tools, versions := table.counts()
	completeTools, completeVersions := table.completeCounts()
	if tools != 1 || versions != 2 {
		t.Fatalf("schema counts = %d/%d, want both distinct signatures", tools, versions)
	}
	if completeTools != 1 || completeVersions != 1 {
		t.Fatalf("complete schema counts = %d/%d, want the incomplete signature retained", completeTools, completeVersions)
	}
}

func TestToolSchemaSignatureIgnoresRequiredOrder(t *testing.T) {
	left := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
		"required":   []any{"a", "b"},
	}
	right := map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
		"required":   []any{"b", "a"},
	}
	if toolSchemaSignature(left) != toolSchemaSignature(right) {
		t.Fatal("required field order must not create a duplicate signature")
	}
}

func TestSameNameIncompleteDefinitionFillsOnlyMissingDescriptions(t *testing.T) {
	table := newToolSchemaTable()
	donor := []byte(`{
		"tools":[{"type":"function","function":{
			"name":"Edit",
			"description":"Edit a file",
			"parameters":{"type":"object","properties":{
				"file_path":{"type":"string","description":"Absolute file path"},
				"old_string":{"type":"string","description":"Text to replace"}
			},"required":["file_path","old_string"]}
		}}],
		"messages":[{"role":"user","content":"remember this schema"}]
	}`)
	table.observeStoredDefinitions(donor, time.Now().UTC())

	payload := []byte(`{
		"model":"gemini-3.5-flash",
		"tools":[{"type":"function","function":{
			"name":"Edit",
			"description":"Apply a patch",
			"parameters":{"type":"object","properties":{
				"file_path":{"type":"string","minLength":1},
				"diff":{"type":"string","description":"Original patch description"}
			},"required":["file_path","diff"]}
		}}],
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Edit","arguments":"{\"file_path\":\"/tmp/a\",\"diff\":\"@@\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"user","content":"second"},
			{"role":"assistant","content":"done"}
		]
	}`)
	enriched, errEnrich := table.observeAndEnrich(payload, time.Now().UTC())
	if errEnrich != nil {
		t.Fatalf("observeAndEnrich() error = %v", errEnrich)
	}
	evaluation, errEvaluate := Evaluate(enriched)
	if errEvaluate != nil || evaluation.Mask&bitFor(CriterionToolSchema) == 0 {
		t.Fatalf("repaired payload did not pass schema validation: mask=%d err=%v", evaluation.Mask, errEvaluate)
	}

	var root map[string]any
	if errDecode := json.Unmarshal(enriched, &root); errDecode != nil {
		t.Fatal(errDecode)
	}
	definition := payloadToolDefinitionMaps(root)[0]
	schema, _ := firstMap(definition, "parameters")
	properties, _ := schema["properties"].(map[string]any)
	filePath, _ := properties["file_path"].(map[string]any)
	diff, _ := properties["diff"].(map[string]any)
	if firstString(filePath, "description") != "Absolute file path" {
		t.Fatalf("file_path description = %q", firstString(filePath, "description"))
	}
	if firstString(diff, "description") != "Original patch description" {
		t.Fatalf("existing description was overwritten: %q", firstString(diff, "description"))
	}
	if filePath["minLength"] != float64(1) {
		t.Fatalf("original field was changed: %#v", filePath)
	}
}

func TestSameNameRepairStillRejectsMismatchedArguments(t *testing.T) {
	table := newToolSchemaTable()
	donor := []byte(`{
		"tools":[{"type":"function","function":{
			"name":"TodoWrite",
			"description":"Update tasks",
			"parameters":{"type":"object","properties":{
				"items":{"type":"array","description":"Task items","items":{"type":"string"}}
			},"required":["items"]}
		}}],
		"messages":[{"role":"user","content":"remember this schema"}]
	}`)
	table.observeStoredDefinitions(donor, time.Now().UTC())

	payload := []byte(`{
		"tools":[{"type":"function","function":{
			"name":"TodoWrite",
			"description":"Update tasks",
			"parameters":{"type":"object","properties":{
				"items":{"type":"array","items":{"type":"string"}}
			},"required":["items"]}
		}}],
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"TodoWrite","arguments":"{\"todos\":[]}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"user","content":"second"},
			{"role":"assistant","content":"done"}
		]
	}`)
	enriched, errEnrich := table.observeAndEnrich(payload, time.Now().UTC())
	if errEnrich != nil {
		t.Fatalf("observeAndEnrich() error = %v", errEnrich)
	}
	evaluation, errEvaluate := Evaluate(enriched)
	if errEvaluate != nil {
		t.Fatal(errEvaluate)
	}
	if evaluation.Mask&bitFor(CriterionToolSchema) != 0 {
		t.Fatal("argument/signature mismatch must remain rejected")
	}
}

func TestToolSchemaImportKeepsIncompleteVersion(t *testing.T) {
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	defer func() { _ = store.Close(context.Background()) }()

	registry := persistedToolSchemaTable{
		Version: toolSchemaTableVersion,
		Tools: map[string]persistedToolSchemaSet{
			"Edit": {
				Versions: []persistedToolSchemaVersion{
					{Definition: json.RawMessage(`{
						"name":"Edit",
						"description":"Edit a file",
						"parameters":{"type":"object","properties":{
							"file_path":{"type":"string"},
							"diff":{"type":"string","description":"Patch"}
						},"required":["file_path","diff"]}
					}`)},
				},
			},
		},
	}
	data, errMarshal := json.Marshal(registry)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	result, errImport := store.ImportToolSchemas(data)
	if errImport != nil {
		t.Fatalf("ImportToolSchemas() error = %v", errImport)
	}
	if result.AddedTools != 1 || result.AddedVersions != 1 || result.SkippedIncomplete != 0 {
		t.Fatalf("import result = %+v, want incomplete signature retained", result)
	}
	completeTools, completeVersions := store.schemaTable.completeCounts()
	if completeTools != 0 || completeVersions != 0 {
		t.Fatalf("incomplete import became complete without a real description: %d/%d", completeTools, completeVersions)
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
