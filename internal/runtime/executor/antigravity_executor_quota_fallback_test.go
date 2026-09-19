package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityQuotaFallbackAcrossCredentials(t *testing.T) {
	const quotaMessage = "Resource has been exhausted (e.g. check quota)."
	for _, tc := range []struct {
		name      string
		model     string
		stream    bool
		status    int
		message   string
		move      bool
		endpoints bool
	}{
		{"gemini", "gemini-2.5-flash", false, 429, quotaMessage, true, false},
		{"gemini-stream", "gemini-2.5-flash", true, 429, quotaMessage, true, false},
		{"claude", "claude-sonnet-4-6", false, 429, quotaMessage, true, false},
		{"claude-stream", "claude-sonnet-4-6", true, 429, quotaMessage, true, false},
		{"endpoints-then-credentials", "gemini-2.5-flash", true, 429, quotaMessage, true, true},
		{"other-429", "gemini-2.5-flash", false, 429, "Rate limit exceeded", false, false},
		{"other-429-stream", "gemini-2.5-flash", true, 429, "Rate limit exceeded", false, false},
		{"not-exact", "gemini-2.5-flash", false, 429, quotaMessage + " Try again later.", false, false},
		{"other-status", "gemini-2.5-flash", false, 500, quotaMessage, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var bodies, credentials []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read request: %v", errRead)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				bodies = append(bodies, string(body))
				credentials = append(credentials, r.Header.Get("Authorization"))
				attempt := len(bodies)
				mu.Unlock()
				if attempt <= 2 {
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"message":%q}}`, tc.status, tc.message)
					return
				}
				response := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", response)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response)
				}
			}))
			defer server.Close()
			if tc.endpoints {
				originalBaseURLs := antigravityBaseURLFallbackOrder
				antigravityBaseURLFallbackOrder = func(*cliproxyauth.Auth) []string {
					return []string{server.URL + "/primary", server.URL + "/fallback"}
				}
				t.Cleanup(func() { antigravityBaseURLFallbackOrder = originalBaseURLs })
			}

			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 3)
			manager.RegisterExecutor(NewAntigravityExecutor(&config.Config{}))
			reg := registry.GetGlobalRegistry()
			for i := 0; i < 3; i++ {
				id := fmt.Sprintf("%s-%d", t.Name(), i)
				reg.RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: tc.model}})
				t.Cleanup(func() { reg.UnregisterClient(id) })
				_, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
					ID: id, Provider: "antigravity",
					Attributes: map[string]string{"base_url": server.URL},
					Metadata: map[string]any{
						"access_token": id, "project_id": "project-1",
						"expired": time.Now().Add(time.Hour).Format(time.RFC3339),
					},
				})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			const payload = `{"request":{"systemInstruction":{"parts":[{"text":"system-a"},{"text":"system-b"}]},"contents":[{"role":"user","parts":[{"text":"question"},{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"follow-up"}]}],"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`
			req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(payload)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity, OriginalRequest: []byte(payload)}
			// The second call reuses the inputs and parent context but must not inherit the fallback.
			ctx := context.Background()
			for i := 0; i < 2; i++ {
				if tc.stream {
					result, errStream := manager.ExecuteStream(ctx, []string{"antigravity"}, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := manager.Execute(ctx, []string{"antigravity"}, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 4 {
				t.Fatalf("requests = %d, want two failures, success, and a fresh request", len(bodies))
			}
			if (credentials[0] == credentials[1]) != tc.endpoints || credentials[1] == credentials[2] || credentials[0] == credentials[2] {
				t.Fatal("retry did not switch credentials")
			}
			for i, body := range bodies {
				parts := gjson.Get(body, "request.contents.0.parts")
				if tc.move && (i == 1 || i == 2) {
					if gjson.Get(body, "request.systemInstruction").Exists() || parts.Get("#").Int() != 5 || parts.Get("0.text").String() != "system-a" || parts.Get("1.text").String() != "system-b" || parts.Get("3.text").String() != "question" || parts.Get("4.inlineData.data").String() != "aW1hZ2U=" {
						t.Fatalf("retry %d did not relocate the prompt exactly once: %s", i, body)
					}
				} else if gjson.Get(body, "request.systemInstruction.parts.0.text").String() != "system-a" || parts.Get("0.text").String() != "question" || parts.Get("#").Int() != 2 {
					t.Fatalf("request %d changed without a matching 429: %s", i, body)
				}
				if gjson.Get(body, "request.contents.2.parts.0.text").String() != "follow-up" {
					t.Fatal("later user input changed")
				}
				if gjson.Get(body, "request.generationConfig").Raw != gjson.Get(bodies[0], "request.generationConfig").Raw {
					t.Fatal("thinking configuration changed on retry")
				}
			}
			if string(req.Payload) != payload || string(opts.OriginalRequest) != payload {
				t.Fatal("caller-owned payload was mutated")
			}
		})
	}
}
