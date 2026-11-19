package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestAntigravityBuildRequest_SanitizesGeminiToolSchema(t *testing.T) {
	body := buildRequestBodyFromPayload(t, "gemini-2.5-pro")
	if got := body["requestType"]; got != "agent" {
		t.Fatalf("requestType = %v, want agent", got)
	}

	decl := extractFirstFunctionDeclaration(t, body)
	if _, ok := decl["parametersJsonSchema"]; ok {
		t.Fatalf("parametersJsonSchema should be renamed to parameters")
	}

	params, ok := decl["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing or invalid type")
	}
	assertSchemaSanitizedAndPropertyPreserved(t, params)
}

func TestAntigravityBuildRequest_ClassifiesTextAsChat(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-2.5-pro", []byte(`{"requestType":"agent","request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`))
	if got := body["requestType"]; got != "chat" {
		t.Fatalf("requestType = %v, want chat", got)
	}
	request := body["request"].(map[string]any)
	content := request["contents"].([]any)[0].(map[string]any)
	text := content["parts"].([]any)[0].(map[string]any)["text"].(string)
	if text != "hello" {
		t.Fatal("user message must be forwarded without a wrapper")
	}
}

func TestAntigravityBuildRequest_SanitizesAntigravityToolSchema(t *testing.T) {
	body := buildRequestBodyFromPayload(t, "claude-opus-4-6")

	decl := extractFirstFunctionDeclaration(t, body)
	params, ok := decl["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing or invalid type")
	}
	assertSchemaSanitizedAndPropertyPreserved(t, params)
}

func TestAntigravityBuildRequest_NormalizesClaudeToolRequest(t *testing.T) {
	raw := []byte(`{"request":{"system_instruction":{"parts":[{"text":"instructions"}]},"tools":[{"function_declarations":[{"name":"a","parameters":{"type":"object","properties":{"type":{"type":"string"}}}},{"name":"b"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY"}},"thinking_config":{"thinking_budget":512},"contents":[{"role":"model","parts":[{"functionCall":{"name":"a","args":{"b":2,"a":1}}}]},{"role":"user","parts":[{"functionResponse":{"name":"a","response":{"ok":true}}}]}]}}`)
	build := func(input []byte) map[string]any {
		return buildRequestBodyFromRawPayload(t, "claude-opus-4-6", input)["request"].(map[string]any)
	}
	request := build(raw)
	tools := request["tools"].([]any)
	first := tools[0].(map[string]any)["functionDeclarations"].([]any)
	second := tools[1].(map[string]any)["functionDeclarations"].([]any)
	if len(tools) != 2 || len(first) != 1 || len(second) != 1 || second[0].(map[string]any)["name"] != "b" {
		t.Fatalf("tool declarations were not split in order: %v", tools)
	}
	schema := first[0].(map[string]any)["parameters"].(map[string]any)
	if schema["type"] != "OBJECT" || schema["properties"].(map[string]any)["type"].(map[string]any)["type"] != "STRING" {
		t.Fatalf("schema types were not normalized: %v", schema)
	}
	if request["systemInstruction"].(map[string]any)["role"] != "user" || request["system_instruction"] != nil {
		t.Fatalf("system instruction was not normalized")
	}
	if request["generationConfig"].(map[string]any)["thinkingConfig"] != nil ||
		request["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)["mode"] != "ANY" {
		t.Fatal("Claude forced tool mode must be retained without thinking")
	}
	part := func(request map[string]any, index int, name string) map[string]any {
		content := request["contents"].([]any)[index].(map[string]any)
		return content["parts"].([]any)[0].(map[string]any)[name].(map[string]any)
	}
	call := part(request, 0, "functionCall")
	response := part(request, 1, "functionResponse")
	reordered := build([]byte(strings.Replace(string(raw), `"b":2,"a":1`, `"a":1,"b":2`, 1)))
	if id := call["id"]; id == nil || id != response["id"] || id != part(reordered, 0, "functionCall")["id"] {
		t.Fatal("tool IDs must be stable and paired")
	}
}

func TestAntigravityBuildRequest_SkipsSchemaSanitizationWithoutToolsField(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3.1-flash-image", []byte(`{
		"request": {
			"contents": [
				{
					"role": "user",
					"x-debug": "keep-me",
					"parts": [
						{
							"text": "hello"
						}
					]
				}
			],
			"nonSchema": {
				"nullable": true,
				"x-extra": "keep-me"
			},
			"generationConfig": {
				"maxOutputTokens": 128
			}
		}
	}`))

	assertNonSchemaRequestPreserved(t, body)
}

func TestAntigravityBuildRequest_SkipsSchemaSanitizationWithEmptyToolsArray(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3.1-flash-image", []byte(`{
		"request": {
			"tools": [],
			"contents": [
				{
					"role": "user",
					"x-debug": "keep-me",
					"parts": [
						{
							"text": "hello"
						}
					]
				}
			],
			"nonSchema": {
				"nullable": true,
				"x-extra": "keep-me"
			},
			"generationConfig": {
				"maxOutputTokens": 128
			}
		}
	}`))

	assertNonSchemaRequestPreserved(t, body)
}

func TestAntigravityBuildRequest_UsesAuthProjectID(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3.1-pro", []byte(`{
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "hello"}]
				}
			]
		}
	}`))

	if got, ok := body["project"].(string); !ok || got != "project-1" {
		t.Fatalf("project should come from auth metadata, got=%v", body["project"])
	}
}

func TestAntigravityBuildRequest_UsesRouteModelWhenPayloadContainsDifferentModel(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3-flash-agent", []byte(`{
		"model": "gemini-3.1-flash-lite",
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "Perform a web search"}]
				}
			],
			"tools": [{"googleSearch": {}}]
		}
	}`))

	if got, ok := body["model"].(string); !ok || got != "gemini-3-flash-agent" {
		t.Fatalf("request model should stay on route model, got=%v", body["model"])
	}
}

func TestAntigravityBuildRequest_UsesAccountSessionIDAndPreservesExplicit(t *testing.T) {
	t.Parallel()

	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	req, err := executor.buildRequest(context.Background(), auth, "token", "gemini-3.1-pro", payload, false, "", "https://example.com", "-123456789")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	body := requestBody(t, req)
	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid: %v", body["request"])
	}
	if got := request["sessionId"]; got == "" || got == "-123456789" {
		t.Fatalf("request.sessionId should be derived from the account, got %v", got)
	}

	explicitPayload := []byte(`{"request":{"sessionId":"-987654321","contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	explicitReq, errExplicit := executor.buildRequest(context.Background(), auth, "token", "gemini-3.1-pro", explicitPayload, false, "", "https://example.com", "-123456789")
	if errExplicit != nil {
		t.Fatalf("buildRequest explicit error: %v", errExplicit)
	}
	explicitBody := requestBody(t, explicitReq)
	explicitRequest, ok := explicitBody["request"].(map[string]any)
	if !ok {
		t.Fatalf("explicit request missing or invalid: %v", explicitBody["request"])
	}
	if got := explicitRequest["sessionId"]; got != "-987654321" {
		t.Fatalf("explicit request.sessionId = %v, want -987654321", got)
	}
}

func TestAntigravityBuildRequest_KeepsSearchToolsAsAgent(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3.1-flash-lite", []byte(`{
		"requestType": "web_search",
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "北京天气 2026-06-12"}]
				}
			],
			"tools": [
				{
					"googleSearch": {
						"enhancedContent": {
							"imageSearch": {
								"maxResultCount": 5
							}
						}
					}
				}
			],
			"generationConfig": {
				"candidateCount": 1
			}
		}
	}`))

	if got, ok := body["requestType"].(string); !ok || got != "agent" {
		t.Fatalf("requestType should be agent, got=%v", body["requestType"])
	}
	if _, ok := body["requestId"]; !ok {
		t.Fatalf("request should have requestId: %v", body["requestId"])
	}
	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid: %v", body["request"])
	}
	if _, ok := request["sessionId"]; !ok {
		t.Fatalf("request should have request.sessionId: %v", request["sessionId"])
	}
	if tools, ok := request["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("search tool should be retained: %v", request["tools"])
	}
	if got, ok := body["project"].(string); !ok || got != "project-1" {
		t.Fatalf("project should come from auth metadata, got=%v", body["project"])
	}
}

func TestAntigravityBuildRequest_KeepsConnectionAlive(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}

	req, err := executor.buildRequest(context.Background(), auth, "token", "gemini-3.1-pro", []byte(`{"request":{}}`), false, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	if req.Close {
		t.Fatal("Antigravity requests should reuse the upstream connection")
	}
	if got := req.Header.Get("Connection"); strings.EqualFold(got, "close") {
		t.Fatalf("Connection header = %q, want a reusable connection", got)
	}
}

func TestCloneTransportWithHTTP11_EnablesHTTP2AndSetsPoolLimits(t *testing.T) {
	base := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 2,
	}
	transport := cloneTransportWithHTTP11(base)

	if transport == base {
		t.Fatal("transport should be cloned")
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("Antigravity transport must negotiate HTTP/2")
	}
	if transport.MaxIdleConns < antigravityMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConns = %d, want at least %d", transport.MaxIdleConns, antigravityMaxIdleConnsPerHost)
	}
	if transport.MaxIdleConnsPerHost < antigravityMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want at least %d", transport.MaxIdleConnsPerHost, antigravityMaxIdleConnsPerHost)
	}
}

func TestShouldResolveAntigravityWebSearchGroundingURLsRequiresTypedWebSearchAndSearchRequest(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	translatedWithGoogleSearch := []byte(`{"requestType":"web_search","request":{"tools":[{"googleSearch":{}}]}}`)
	translatedWithoutGoogleSearch := []byte(`{"request":{"contents":[]}}`)

	if !shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatClaude, original, translatedWithGoogleSearch) {
		t.Fatal("expected typed Claude web search translated to web_search request to resolve grounding URLs")
	}
	if shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatClaude, original, translatedWithoutGoogleSearch) {
		t.Fatal("expected request without googleSearch to skip grounding URL resolution")
	}
	if shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatOpenAI, original, translatedWithGoogleSearch) {
		t.Fatal("expected non-Claude source format to skip grounding URL resolution")
	}
}

func TestAntigravityPrepareRequestAuth_FetchesMissingProjectID(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token": "token",
		"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected project discovery request: %s", req.URL.String())
		}
		if got := req.Header.Get("X-Goog-Api-Client"); got != "" {
			t.Fatalf("X-Goog-Api-Client = %q, want empty", got)
		}
		raw, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read discovery body: %v", errRead)
		}
		if !strings.Contains(string(raw), `"ideType":"ANTIGRAVITY"`) {
			t.Fatalf("unexpected discovery body: %s", string(raw))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"fetched-project"}`)),
		}, nil
	}))

	updated, err := executor.PrepareRequestAuth(ctx, auth)
	if err != nil {
		t.Fatalf("PrepareRequestAuth error: %v", err)
	}
	if updated == nil {
		t.Fatalf("PrepareRequestAuth returned nil auth")
	}
	if _, ok := auth.Metadata["project_id"]; ok {
		t.Fatalf("original auth metadata should not be mutated")
	}
	if got, ok := updated.Metadata["project_id"].(string); !ok || got != "fetched-project" {
		t.Fatalf("updated auth metadata project_id = %v, want fetched-project", updated.Metadata["project_id"])
	}
}

func TestAntigravityPrepareRequestAuth_RefreshesExpiredTokenWithProjectID(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token":  "expired-token",
		"refresh_token": "refresh-token",
		"expired":       time.Now().Add(-1 * time.Minute).Format(time.RFC3339),
		"project_id":    "existing-project",
	}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://oauth2.googleapis.com/token" {
			t.Fatalf("unexpected token refresh request: %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh-token","refresh_token":"fresh-refresh-token","expires_in":3600}`)),
		}, nil
	}))

	updated, err := executor.PrepareRequestAuth(ctx, auth)
	if err != nil {
		t.Fatalf("PrepareRequestAuth error: %v", err)
	}
	if updated == nil {
		t.Fatalf("PrepareRequestAuth returned nil auth")
	}
	if got := metaStringValue(auth.Metadata, "access_token"); got != "expired-token" {
		t.Fatalf("original auth access_token = %q, want expired-token", got)
	}
	if got := metaStringValue(updated.Metadata, "access_token"); got != "fresh-token" {
		t.Fatalf("updated auth access_token = %q, want fresh-token", got)
	}
	if got := metaStringValue(updated.Metadata, "refresh_token"); got != "fresh-refresh-token" {
		t.Fatalf("updated auth refresh_token = %q, want fresh-refresh-token", got)
	}
	if got := antigravityProjectIDFromAuth(updated); got != "existing-project" {
		t.Fatalf("updated auth project_id = %q, want existing-project", got)
	}
}

func TestAntigravityBuildRequest_RejectsMissingProjectID(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{}}

	_, err := executor.buildRequest(context.Background(), auth, "token", "gemini-3.1-pro", []byte(`{"request":{}}`), false, "", "https://example.com")
	if err == nil {
		t.Fatalf("buildRequest should fail when auth has no project_id")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error should expose status code, got %T", err)
	}
	if got := status.StatusCode(); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", got, http.StatusBadRequest)
	}
}

func assertNonSchemaRequestPreserved(t *testing.T, body map[string]any) {
	t.Helper()

	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid type")
	}

	contents, ok := request["contents"].([]any)
	if !ok || len(contents) == 0 {
		t.Fatalf("contents missing or empty")
	}
	content, ok := contents[0].(map[string]any)
	if !ok {
		t.Fatalf("content missing or invalid type")
	}
	if got, ok := content["x-debug"].(string); !ok || got != "keep-me" {
		t.Fatalf("x-debug should be preserved when no tool schema exists, got=%v", content["x-debug"])
	}

	nonSchema, ok := request["nonSchema"].(map[string]any)
	if !ok {
		t.Fatalf("nonSchema missing or invalid type")
	}
	if _, ok := nonSchema["nullable"]; !ok {
		t.Fatalf("nullable should be preserved outside schema cleanup path")
	}
	if got, ok := nonSchema["x-extra"].(string); !ok || got != "keep-me" {
		t.Fatalf("x-extra should be preserved outside schema cleanup path, got=%v", nonSchema["x-extra"])
	}

	if generationConfig, ok := request["generationConfig"].(map[string]any); ok {
		if generationConfig["candidateCount"] != float64(1) || generationConfig["maxOutputTokens"] != nil {
			t.Fatalf("image generation config should match agy2api")
		}
	}
}

func buildRequestBodyFromPayload(t *testing.T, modelName string) map[string]any {
	t.Helper()
	return buildRequestBodyFromRawPayload(t, modelName, []byte(`{
		"userAgent": "client-provided",
		"request": {
			"tools": [
				{
					"function_declarations": [
						{
							"name": "tool_1",
							"parametersJsonSchema": {
								"$schema": "http://json-schema.org/draft-07/schema#",
								"$id": "root-schema",
								"$comment": "root comment should be removed",
								"type": "object",
								"properties": {
									"$id": {"type": "string"},
									"arg": {
										"type": "object",
										"$comment": "nested comment should be removed",
										"prefill": "hello",
										"properties": {
											"mode": {
												"type": "string",
												"deprecated": true,
												"enum": ["a", "b"],
												"enumDescriptions": ["Alpha", "Beta"],
												"enumTitles": ["A", "B"]
											}
										}
									}
								},
								"patternProperties": {
									"^x-": {"type": "string"}
								}
							}
						}
					]
				}
			]
		}
	}`))
}

func buildRequestBodyFromRawPayload(t *testing.T, modelName string, payload []byte) map[string]any {
	t.Helper()

	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}

	req, err := executor.buildRequest(context.Background(), auth, "token", modelName, payload, false, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	if got := req.Header.Get("User-Agent"); got != misc.AntigravityUserAgent() {
		t.Fatalf("User-Agent = %q", got)
	}
	body := requestBody(t, req)
	if body["userAgent"] != "antigravity" {
		t.Fatal("request body must identify antigravity")
	}
	return body
}

func requestBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()

	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal request body error: %v, body=%s", err, string(raw))
	}
	return body
}

func extractFirstFunctionDeclaration(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid type")
	}
	tools, ok := request["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools missing or empty")
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("first tool invalid type")
	}
	decls, ok := tool["functionDeclarations"].([]any)
	if !ok || len(decls) == 0 {
		t.Fatalf("functionDeclarations missing or empty")
	}
	decl, ok := decls[0].(map[string]any)
	if !ok {
		t.Fatalf("first function declaration invalid type")
	}
	return decl
}

func assertSchemaSanitizedAndPropertyPreserved(t *testing.T, params map[string]any) {
	t.Helper()

	if _, ok := params["$id"]; ok {
		t.Fatalf("root $id should be removed from schema")
	}
	if _, ok := params["$comment"]; ok {
		t.Fatalf("root $comment should be removed from schema")
	}
	if _, ok := params["patternProperties"]; ok {
		t.Fatalf("patternProperties should be removed from schema")
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing or invalid type")
	}
	if _, ok := props["$id"]; !ok {
		t.Fatalf("property named $id should be preserved")
	}

	arg, ok := props["arg"].(map[string]any)
	if !ok {
		t.Fatalf("arg property missing or invalid type")
	}
	if _, ok := arg["prefill"]; ok {
		t.Fatalf("prefill should be removed from nested schema")
	}
	if _, ok := arg["$comment"]; ok {
		t.Fatalf("nested $comment should be removed from schema")
	}

	argProps, ok := arg["properties"].(map[string]any)
	if !ok {
		t.Fatalf("arg.properties missing or invalid type")
	}
	mode, ok := argProps["mode"].(map[string]any)
	if !ok {
		t.Fatalf("mode property missing or invalid type")
	}
	if _, ok := mode["enumTitles"]; ok {
		t.Fatalf("enumTitles should be removed from nested schema")
	}
	if _, ok := mode["enumDescriptions"]; ok {
		t.Fatalf("enumDescriptions should be removed from nested schema")
	}
	if _, ok := mode["deprecated"]; ok {
		t.Fatalf("deprecated should be removed from nested schema")
	}
}
