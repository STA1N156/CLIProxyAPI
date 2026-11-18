package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityLastExecutionAcrossTurnsAndRetries(t *testing.T) {
	for _, tc := range []struct {
		model  string
		stream bool
	}{
		{"gemini-3.8-flash-medium", false}, {"gemini-3.8-flash-medium", true}, {"claude-sonnet-4-6", false},
	} {
		t.Run(fmt.Sprintf("%s/stream=%v", tc.model, tc.stream), func(t *testing.T) {
			var sent [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				sent = append(sent, body)
				if len(sent) == 2 {
					w.WriteHeader(429)
					_, _ = w.Write([]byte(`{"error":{"code":429,"message":"retry"}}`))
					return
				}
				response := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}]}}`
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					response = "data: " + response + "\n\n"
				}
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			e := NewAntigravityExecutor(&config.Config{})
			session := uuid.NewString()
			contents := []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}
			call := func(account string) error {
				raw, _ := json.Marshal(map[string]any{"request": map[string]any{"sessionId": session, "contents": contents}})
				auth := &cliproxyauth.Auth{ID: account, Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "fake", "project_id": "project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
				req := cliproxyexecutor.Request{Model: tc.model, Payload: raw}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}
				if !tc.stream {
					_, err := e.Execute(context.Background(), auth, req, opts)
					return err
				}
				res, err := e.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					return err
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						return chunk.Err
					}
				}
				return nil
			}
			if err := call("a"); err != nil {
				t.Fatal(err)
			}
			contents = append(contents, map[string]any{"role": "model", "parts": []any{map[string]any{"text": "OK"}}}, map[string]any{"role": "user", "parts": []any{map[string]any{"text": "next"}}})
			if err := call("a"); err == nil {
				t.Fatal("expected retryable failure")
			}
			if err := call("b"); err != nil {
				t.Fatal(err)
			}
			if err := call("b"); err != nil {
				t.Fatal(err)
			}
			path := "request.labels.last_execution_id"
			if gjson.GetBytes(sent[0], path).Exists() {
				t.Fatal("first turn has last execution ID")
			}
			previous := gjson.GetBytes(sent[1], path).String()
			if _, err := uuid.Parse(previous); err != nil {
				t.Fatal("second turn missing execution UUID")
			}
			for _, body := range sent[2:] {
				if gjson.GetBytes(body, path).String() != previous {
					t.Fatal("retry or credential change advanced execution ID")
				}
			}
			contents = append(contents, map[string]any{"role": "model", "parts": []any{map[string]any{"text": "OK"}}}, map[string]any{"role": "user", "parts": []any{map[string]any{"text": "third"}}})
			if err := call("b"); err != nil {
				t.Fatal(err)
			}
			if next := gjson.GetBytes(sent[4], path).String(); next == "" || next == previous {
				t.Fatal("completed turn did not advance execution ID")
			}
		})
	}
}

func TestAntigravityOfficialOutputLimits(t *testing.T) {
	for _, row := range []struct {
		model string
		limit int
	}{
		{"gemini-3.8-flash-medium", 65536}, {"gemini-4-carbon-high", 262144},
		{"claude-sonnet-4-6", 64000}, {"gpt-oss-120b-medium", 32768},
		{"gemini-3.1-flash-image", 65536}, {"unknown-model", 123},
	} {
		for _, tools := range []bool{false, true} {
			payload := map[string]any{"request": map[string]any{"generationConfig": map[string]any{"maxOutputTokens": 123}}}
			if tools {
				payload["request"].(map[string]any)["tools"] = []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "ping", "parameters": map[string]any{"type": "object"}}}}}
			}
			raw, _ := json.Marshal(payload)
			auth := &cliproxyauth.Auth{ID: "limit-test", Metadata: map[string]any{"project_id": "project"}}
			req, err := (&AntigravityExecutor{}).buildRequest(context.Background(), auth, "token", row.model, raw, false, "", "https://example.invalid")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(req.Body)
			_ = req.Body.Close()
			if got := gjson.GetBytes(body, "request.generationConfig.maxOutputTokens").Int(); got != int64(row.limit) {
				t.Fatalf("%s tools=%v: output limit %d, want %d", row.model, tools, got, row.limit)
			}
		}
	}
	for _, limit := range []int{32768, 16384} {
		helps.CacheAntigravityModelEnums("limit-refresh", nil, map[string]int{"gemini-3.8-flash-medium": limit})
		out := helps.ApplyAntigravityOutputLimit([]byte(`{"request":{}}`), "limit-refresh", "gemini-3.8-flash-medium")
		if gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int() != int64(limit) {
			t.Fatal("credential output limit was not refreshed")
		}
	}
	out := helps.ApplyAntigravityOutputLimit([]byte(`{"request":{}}`), "other-limit-account", "gemini-3.8-flash-medium")
	if gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int() != 65536 {
		t.Fatal("credential output limit leaked across accounts")
	}
}

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
		{first, "project-b", "1", "1", "1"}, {next, "project-a", "2", "2", "2"},
		{[]byte(`{"request":{"sessionId":"-123","contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"functionCall":{"name":"a"}},{"functionCall":{"name":"b"}}]},{"role":"model","parts":[{"functionResponse":{"name":"a"}},{"functionResponse":{"name":"b"}}]}]}}`), "project-a", "2", "2", "2"},
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
	if gjson.GetBytes(explicit, "request.labels.custom").String() != "keep" || gjson.GetBytes(explicit, "request.labels.request_id").String() != "existing-trajectory-3" || (!strings.Contains(gjson.GetBytes(explicit, "requestId").String(), "/existing-cascade/") || !strings.HasSuffix(gjson.GetBytes(explicit, "requestId").String(), "/7")) {
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
		helps.CacheAntigravityModelEnums(scope, map[string]string{"gemini-3.8-flash-medium": enum}, nil)
		updated := helps.ApplyAntigravityModelEnum(got, scope, "gemini-3.8-flash-medium")
		if gjson.GetBytes(updated, "request.labels.model_enum").String() != enum {
			t.Fatal("model enum did not follow the refreshed credential catalog")
		}
	}
	if gjson.GetBytes(helps.ApplyAntigravityModelEnum(got, "other-credential", "gemini-3.8-flash-medium"), "request.labels.model_enum").String() != "gemini-3.8-flash-medium" {
		t.Fatal("model enum leaked across credentials")
	}
}
