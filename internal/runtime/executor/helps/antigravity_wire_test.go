package helps

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

type wireTestTransport func(*http.Request) (*http.Response, error)

func (f wireTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAntigravityWireConfigurationErrorIsRequestScoped(t *testing.T) {
	base := wireTestTransport(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		if strings.HasSuffix(req.URL.Path, ":fetchAvailableModels") {
			body = `{"models":{"gemini-4-hydrogen-low":{"model":"MODEL_TEST","modelExperimentIds":["monitors"]}},"sharedModelExperiments":{"monitors":{"experiments":{"allow-experimental-monitors":{"boolValue":true},"default-sonar-monitors-config":{"stringValue":"monitors {"}}}}}`
		}
		if strings.HasSuffix(req.URL.Path, ":streamGenerateContent") {
			t.Error("invalid configuration reached generation")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	transport := &AntigravityWireTransport{Base: base, Account: t.Name(), StateDir: t.TempDir(), Token: func() (string, error) { return "test-token", nil }}
	defer CloseAntigravityWire()
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	req, _ := http.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent", bytes.NewReader(payload))
	req = BindAntigravityWireRequest(req, payload, "gemini-4-hydrogen-low", "test-conversation")
	_, err := (&http.Client{Transport: transport}).Do(req)
	var scoped *cliproxyauth.Error
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() || scoped.StatusCode() != http.StatusNotImplemented {
		t.Fatalf("expected request-scoped configuration error, got %v", err)
	}
}

func TestAntigravityWireConversation(t *testing.T) {
	var mu sync.Mutex
	var generation [][]byte
	base := wireTestTransport(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		if strings.HasSuffix(req.URL.Path, ":fetchAvailableModels") {
			body = `{"models":{"gemini-3.5-flash-lite":{"model":"MODEL_PLACEHOLDER_M198","maxTokens":1000000,"maxOutputTokens":4096,"thinkingLevel":2}},"defaultAgentModelId":"gemini-3.5-flash-lite"}`
		}
		if strings.HasSuffix(req.URL.Path, ":streamGenerateContent") {
			raw, _ := io.ReadAll(req.Body)
			if gjson.GetBytes(raw, "requestType").String() == "chat" {
				mu.Lock()
				generation = append(generation, raw)
				n := len(generation)
				mu.Unlock()
				if n == 1 {
					body = "data: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"mcp_c_lookup","args":{"q":"test","toolSummary":"Search test"}},"thoughtSignature":"signature"}]} ,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4,"candidatesTokenCount":2,"thoughtsTokenCount":2,"totalTokenCount":14}}}` + "\n\n"
					for _, tool := range gjson.GetBytes(raw, "request.tools").Array() {
						name := tool.Get("functionDeclarations.0.name").String()
						if strings.HasSuffix(name, "_lookup") {
							body = strings.ReplaceAll(body, "mcp_c_lookup", name)
						}
					}
				} else {
					body = "data: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":15}}}` + "\n\n"
				}
			} else {
				body = "data: " + `{"response":{"candidates":[{"content":{"parts":[{"text":"Title"}]},"finishReason":"STOP"}]}}` + "\n\n"
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	transport := &AntigravityWireTransport{Base: base, Account: t.Name(), StateDir: t.TempDir(), Token: func() (string, error) { return "test-token", nil }}
	defer CloseAntigravityWire()
	first := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Client instructions"}]},"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"OBJECT","properties":{"q":{"type":"STRING"}}}}]}],"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	send := func(raw []byte) []byte {
		req, _ := http.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent", bytes.NewReader(raw))
		req = BindAntigravityWireRequest(req, raw, "gemini-3.5-flash-lite", "stable-conversation")
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, b)
		}
		return b
	}
	response := send(first)
	usage := gjson.GetBytes(response, "response.usageMetadata")
	if usage.Get("promptTokenCount").Int() != 10 || usage.Get("candidatesTokenCount").Int() != 2 || usage.Get("thoughtsTokenCount").Int() != 2 {
		t.Fatalf("usage mapping failed: %s", usage.Raw)
	}
	call := gjson.GetBytes(response, "response.candidates.0.content.parts.0.functionCall")
	if call.Get("name").String() != "lookup" {
		t.Fatalf("tool mapping failed: %s", response)
	}
	second := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Client instructions"}]},"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"OBJECT","properties":{"q":{"type":"STRING"}}}}]}],"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"functionCall":` + call.Raw + `}]},{"role":"user","parts":[{"functionResponse":{"id":"` + call.Get("id").String() + `","name":"lookup","response":{"result":"found"}}}]}]}}`)
	response = send(second)
	if !bytes.Contains(response, []byte(`"text":"OK"`)) {
		t.Fatalf("missing text: %s", response)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(generation) != 2 {
		t.Fatalf("generation count %d", len(generation))
	}
	a, b := gjson.ParseBytes(generation[0]), gjson.ParseBytes(generation[1])
	if a.Get("request.labels.cascade_id").String() != b.Get("request.labels.cascade_id").String() {
		t.Fatal("conversation was rebuilt")
	}
	if a.Get("request.generationConfig.maxOutputTokens").Int() != 4096 || a.Get("request.generationConfig.thinkingConfig.thinkingLevel").String() != "MEDIUM" {
		t.Fatal("catalog settings not applied")
	}
	if a.Get("request.safetySettings").Exists() || a.Get("request.sessionId").String() != "-3750763034362895579" {
		t.Fatal("unexpected CLI shape")
	}
	if !strings.Contains(a.Get("request.contents.0.parts.0.text").String(), "Client instructions") {
		t.Fatal("client instructions lost")
	}
	if !strings.Contains(b.Get("request.contents.2.parts.0.functionResponse.response.output").String(), "found") {
		t.Fatalf("tool result lost: %s", generation[1])
	}
}
