package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAntigravityRequestIdentityMatchesAgy2api(t *testing.T) {
	payload := []byte(`{"request":{"sessionId":"-123","tools":[{"functionDeclarations":[{"name":"lookup"}]}],"labels":{"last_execution_id":"old","cascade_id":"old"},"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	first := geminiToAntigravity("gemini-3.5-flash-lite", payload, "project-a")
	retry := geminiToAntigravity("gemini-3.5-flash-lite", payload, "project-b")
	labels := gjson.GetBytes(first, "request.labels")
	if len(labels.Map()) != 10 || !labels.Get("last_execution_id").Exists() || !labels.Get("cascade_id").Exists() {
		t.Fatal("unexpected labels")
	}
	if labels.Get("last_step_index").String() != "1" || labels.Raw != gjson.GetBytes(retry, "request.labels").Raw {
		t.Fatal("unstable step or trajectory")
	}
	if labels.Get("model_enum").String() != "MODEL_PLACEHOLDER_M198" {
		t.Fatal("model enum does not match the handoff table")
	}
	if !strings.HasPrefix(gjson.GetBytes(first, "request.contents.2.parts.0.text").String(), "<USER_REQUEST>\nnext\n</USER_REQUEST>") {
		t.Fatal("missing user envelope")
	}
}

func TestAntigravityHandoffChatAndAccountSession(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	first := geminiToAntigravity("gemini-3.5-flash-lite", payload, "project", "account-a")
	second := geminiToAntigravity("gemini-4-hydrogen-high", payload, "project", "account-a")
	other := geminiToAntigravity("gemini-3.5-flash-lite", payload, "project", "account-b")
	if gjson.GetBytes(first, "request.labels").Exists() || !strings.HasPrefix(gjson.GetBytes(first, "requestId").String(), "chat/") {
		t.Fatal("chat must not carry agent labels")
	}
	session := gjson.GetBytes(first, "request.sessionId").String()
	if session != gjson.GetBytes(second, "request.sessionId").String() || session == gjson.GetBytes(other, "request.sessionId").String() {
		t.Fatal("session must be stable per account")
	}
}
