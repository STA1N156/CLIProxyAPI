package dataintegration

import (
	"encoding/json"
	"testing"
)

func TestToContractPayloadConvertsGeminiWithoutInventingIDs(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"model":"gemini-3-flash",
		"session_id":"session-1",
		"metadata":{"provider":"antigravity","taskType":"code","domain":"software_engineering"},
		"thinking":{"summary":"top-level thought"},
		"reasoning_content":"top-level reasoning",
		"systemInstruction":{"parts":[{"text":"You are a coding agent."}]},
		"tools":[{"functionDeclarations":[{"name":"Read","description":"Read a file","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING","description":"File path"}},"required":["path"]}}]}],
		"contents":[
			{"role":"user","parts":[{"text":"Read the file."}]},
			{"role":"model","parts":[
				{"thought":true,"text":"I should inspect it."},
				{"functionCall":{"name":"Read","args":{"path":"main.go"}},"thoughtSignature":"native-signature"}
			]},
			{"role":"user","parts":[{"functionResponse":{"name":"Read","response":{"content":"package main"}}}]},
			{"role":"model","parts":[{"text":"Done."}]}
		]
	}`)

	encoded, errConvert := toContractPayload(payload, "trajectory")
	if errConvert != nil {
		t.Fatalf("toContractPayload() error = %v", errConvert)
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(encoded, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if _, exists := root["contents"]; exists {
		t.Fatal("contents must not be duplicated in contract output")
	}
	if _, exists := root["systemInstruction"]; exists {
		t.Fatal("systemInstruction must be mapped into the message sequence")
	}
	if root["reasoning"] != "top-level reasoning" {
		t.Fatalf("reasoning = %v", root["reasoning"])
	}
	if root["thinking"] == nil || root["metadata"] == nil {
		t.Fatal("optional native fields were not preserved")
	}
	if root["task_type"] != "code" || root["domain"] != "software_engineering" {
		t.Fatalf("task_type/domain = %v/%v", root["task_type"], root["domain"])
	}

	messages, okMessages := root["trajectory"].([]any)
	if !okMessages || len(messages) != 5 {
		t.Fatalf("trajectory length = %d, want 5", len(messages))
	}
	roles := []string{"system", "user", "assistant", "tool", "assistant"}
	for index, role := range roles {
		message := messages[index].(map[string]any)
		if message["role"] != role {
			t.Fatalf("message %d role = %v, want %s", index, message["role"], role)
		}
		if _, exists := message["content"]; !exists {
			t.Fatalf("message %d is missing content", index)
		}
	}
	assistant := messages[2].(map[string]any)
	if assistant["thinking"] != "I should inspect it." {
		t.Fatalf("assistant thinking = %v", assistant["thinking"])
	}
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	if _, exists := call["id"]; exists {
		t.Fatal("tool call ID must not be invented")
	}
	if call["thoughtSignature"] != "native-signature" {
		t.Fatalf("thoughtSignature = %v", call["thoughtSignature"])
	}
	result := messages[3].(map[string]any)
	if _, exists := result["tool_call_id"]; exists {
		t.Fatal("tool result ID must not be invented")
	}
	if result["content"] != "package main" {
		t.Fatalf("tool result content = %v", result["content"])
	}

	tool := root["tools"].([]any)[0].(map[string]any)
	function := tool["function"].(map[string]any)
	parameters := function["parameters"].(map[string]any)
	if tool["type"] != "function" || parameters["type"] != "object" {
		t.Fatalf("contract tool schema = %#v", tool)
	}
	properties := parameters["properties"].(map[string]any)
	if properties["path"].(map[string]any)["type"] != "string" {
		t.Fatalf("property type was not normalized: %#v", properties["path"])
	}
}

func TestToContractPayloadOmitsMissingOptionalFields(t *testing.T) {
	t.Parallel()
	encoded, errConvert := toContractPayload(
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
		"messages",
	)
	if errConvert != nil {
		t.Fatalf("toContractPayload() error = %v", errConvert)
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(encoded, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	for _, field := range []string{"metadata", "task_type", "domain", "thinking", "reasoning"} {
		if _, exists := root[field]; exists {
			t.Fatalf("missing optional field %s must be omitted", field)
		}
	}
}

func TestNormalizeExportOptionsValidatesMessageField(t *testing.T) {
	t.Parallel()
	if _, errOptions := normalizeExportOptions(ExportOptions{
		Format:       "json",
		Layout:       ExportLayoutContract,
		MessageField: "invalid",
	}); errOptions == nil {
		t.Fatal("invalid message field must fail")
	}
}
