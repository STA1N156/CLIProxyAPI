package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

func TestAntigravityRequestIdentityAcrossHistoryAndCredentialRetry(t *testing.T) {
	first := []byte(`{"request":{"sessionId":"-123","contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	next := []byte(`{"request":{"sessionId":"-123","contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"hi"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	a := geminiToAntigravity("gemini-3.5-flash-lite", first, "project-a")
	for _, row := range []struct {
		payload []byte
		project string
		step    string
		request string
		outer   string
	}{
		{first, "project-b", "0", "0", "1"}, {next, "project-a", "2", "1", "3"},
		{[]byte(`{"request":{"sessionId":"-123","contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"functionCall":{"name":"a"}},{"functionCall":{"name":"b"}}]},{"role":"model","parts":[{"functionResponse":{"name":"a"}},{"functionResponse":{"name":"b"}}]}]}}`), "project-a", "3", "1", "4"},
	} {
		b := geminiToAntigravity("gemini-3.5-flash-lite", row.payload, row.project)
		for _, key := range []string{"cascade_id", "trajectory_id", "root_cascade_id"} {
			if gjson.GetBytes(a, "request.labels."+key).String() != gjson.GetBytes(b, "request.labels."+key).String() {
				t.Fatalf("%s changed across history or account retry", key)
			}
		}
		labels := gjson.GetBytes(b, "request.labels")
		parts := strings.Split(gjson.GetBytes(b, "requestId").String(), "/")
		if len(parts) != 5 || parts[1] != labels.Get("cascade_id").String() || parts[3] != labels.Get("trajectory_id").String() {
			t.Fatal("request ID is not linked to trace labels")
		}
		if labels.Get("last_step_index").String() != row.step || labels.Get("request_id").String() != parts[3]+"-"+row.request || parts[4] != row.outer {
			t.Fatal("step labels do not follow conversation history")
		}
	}
	retry := geminiToAntigravity("gemini-3.5-flash-lite", a, "project-b")
	if gjson.GetBytes(a, "request.labels").Raw != gjson.GetBytes(retry, "request.labels").Raw || !strings.HasSuffix(gjson.GetBytes(retry, "requestId").String(), "/1") {
		t.Fatal("retry advanced request counters")
	}
	other := geminiToAntigravity("gemini-3.5-flash-lite", []byte(`{"request":{"sessionId":"-456","contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`), "project-a")
	if gjson.GetBytes(a, "request.labels.cascade_id").String() == gjson.GetBytes(other, "request.labels.cascade_id").String() {
		t.Fatal("different sessions share a cascade")
	}
	explicit := geminiToAntigravity("gemini-3.5-flash-lite", []byte(`{"request":{"sessionId":"-123","labels":{"cascade_id":"existing-cascade","trajectory_id":"existing-trajectory","root_cascade_id":"parent","last_step_index":"7","request_id":"existing-trajectory-3","custom":"keep"}}}`), "project-a")
	if gjson.GetBytes(explicit, "request.labels.custom").String() != "keep" || gjson.GetBytes(explicit, "request.labels.request_id").String() != "existing-trajectory-3" || (!strings.Contains(gjson.GetBytes(explicit, "requestId").String(), "/existing-cascade/") || !strings.HasSuffix(gjson.GetBytes(explicit, "requestId").String(), "/8")) {
		t.Fatal("explicit trace labels were not preserved")
	}
}

func TestAntigravityIdentityRetainsModelUsageAndRefreshesEnums(t *testing.T) {
	payload := []byte(`{"request":{"sessionId":"model-switch-test","contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	geminiToAntigravity("claude-sonnet-5-5-medium", payload, "project")
	got := geminiToAntigravity("gemini-3.8-flash-medium", payload, "project")
	for _, key := range []string{"used_claude", "used_claude_conservative", "used_non_gemini_model"} {
		if gjson.GetBytes(got, "request.labels."+key).String() != "true" {
			t.Fatalf("%s lost the prior Claude turn", key)
		}
	}
	const scope = "enum-refresh-test"
	for _, enum := range []string{"MODEL_OLD", "MODEL_NEW"} {
		helps.CacheAntigravityModelEnums(scope, map[string]string{"gemini-3.8-flash-medium": enum})
		updated := helps.ApplyAntigravityModelEnum(got, scope, "gemini-3.8-flash-medium")
		if gjson.GetBytes(updated, "request.labels.model_enum").String() != enum {
			t.Fatal("model enum did not follow the refreshed credential catalog")
		}
	}
	if gjson.GetBytes(helps.ApplyAntigravityModelEnum(got, "other-credential", "gemini-3.8-flash-medium"), "request.labels.model_enum").Exists() {
		t.Fatal("model enum leaked across credentials")
	}
}
