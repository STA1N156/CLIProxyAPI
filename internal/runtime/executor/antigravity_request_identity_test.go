package executor

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestAntigravityRequestIdentityMatchesAgy2api(t *testing.T) {
	payload := []byte(`{"request":{"sessionId":"-123","labels":{"last_execution_id":"old","cascade_id":"old"},"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	first := geminiToAntigravity("gemini-3.5-flash-lite", payload, "project-a")
	retry := geminiToAntigravity("gemini-3.5-flash-lite", payload, "project-b")
	labels := gjson.GetBytes(first, "request.labels")
	if len(labels.Map()) != 7 || labels.Get("last_execution_id").Exists() || labels.Get("cascade_id").Exists() {
		t.Fatal("unexpected labels")
	}
	if labels.Get("last_step_index").String() != "2" || labels.Raw != gjson.GetBytes(retry, "request.labels").Raw {
		t.Fatal("unstable step or trajectory")
	}
	if labels.Get("model_enum").String() != "gemini-3.5-flash-lite" {
		t.Fatal("model enum is not the model name")
	}
}
