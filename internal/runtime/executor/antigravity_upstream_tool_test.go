package executor

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestAntigravityUpstreamToolSignature(t *testing.T) {
	acc := newAntigravityReasoningReplayAccumulator(
		antigravityReasoningReplayScope{modelName: "gemini-3-flash", sessionKey: "test"},
		[]byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"read files"}]}]}}`),
	)
	acc.observeResponsePayload([]byte(`{"candidates":[{"content":{"parts":[
		{"thought":true,"text":"Read both files","thoughtSignature":"native-signature"},
		{"functionCall":{"id":"a","name":"Read","args":{"path":"a"}}},
		{"functionCall":{"id":"b","name":"Read","args":{"path":"b"}}}
	]},"finishReason":"STOP"}]}`))
	var signatures []string
	for _, raw := range acc.items {
		item := gjson.ParseBytes(raw)
		if item.Get("type").String() == "function_call_part" {
			signatures = append(signatures, item.Get("thoughtSignature").String())
		}
	}
	if !acc.terminal || len(signatures) != 2 || signatures[0] != "native-signature" || signatures[1] != "" {
		t.Fatalf("terminal=%v, tool signatures=%v", acc.terminal, signatures)
	}
}

func TestAntigravityUpstreamReplayCallerIsolation(t *testing.T) {
	req := cliproxyexecutor.Request{Payload: []byte(`{"session_id":"shared-session"}`)}
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"same prompt"}]}]}}`)
	scope := func(caller string) antigravityReasoningReplayScope {
		return antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3-flash", req,
			cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: caller}}, payload)
	}
	a, b := scope("caller-a"), scope("caller-b")
	if !a.valid() || !b.valid() || a.sessionKey == b.sessionKey || a.sessionKey != scope("caller-a").sessionKey {
		t.Fatal("replay scope must be stable per caller and isolated between callers")
	}
}
