package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wiresig "github.com/router-for-me/CLIProxyAPI/v7/internal/agywire/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityWireSignatureRebuild(t *testing.T) {
	for _, model := range []string{"claude-sonnet-4-5", "gemini-3.6-flash-high"} {
		t.Run(model, func(t *testing.T) { checkAntigravityWireSignatureRebuild(t, model) })
	}
}

func checkAntigravityWireSignatureRebuild(t *testing.T, model string) {
	defer helps.CloseAntigravityWire()
	upstream := make(chan []byte, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":fetchAvailableModels") {
			_, _ = fmt.Fprintf(w, `{"models":{%q:{"model":"MODEL_TEST","maxTokens":1000000,"maxOutputTokens":64000}}}`, model)
			return
		}
		if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
			raw, _ := io.ReadAll(r.Body)
			if gjson.GetBytes(raw, "requestType").String() == "agent" {
				upstream <- raw
			}
			_, _ = io.WriteString(w, "data: "+`{"response":{"candidates":[{"content":{"parts":[{"text":"OK","thoughtSignature":"NEW_SIGNATURE"}]},"finishReason":"STOP"}]}}`+"\n\n")
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "local-test", "refresh_token": "rt-a", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
	cfg := &config.Config{}
	cfg.AuthDir = t.TempDir()
	sig := wiresig.Wrap(wiresig.Key("rt-a"), wiresig.Payload{Signature: "ORIGINAL_SIGNATURE", Calls: map[string]wiresig.CallMeta{"toolu_one": {GemID: "call_original", Summary: "Read file"}}})
	raw := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","max_tokens":1000,"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}],"messages":[{"role":"user","content":"read it"},{"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":%q},{"type":"tool_use","id":"toolu_one","name":"Read","input":{"file_path":"a.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_one","content":"found"}]}]}`, sig))
	result, err := NewAntigravityExecutor(cfg).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: model, Payload: raw}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, Stream: true, OriginalRequest: raw})
	if err != nil {
		t.Fatal(err)
	}
	var response strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		response.Write(chunk.Payload)
	}
	select {
	case request := <-upstream:
		parts := gjson.GetBytes(request, "request.contents.1.parts")
		if !strings.Contains(parts.Raw, "call_original") || !strings.Contains(parts.Raw, "ORIGINAL_SIGNATURE") || !strings.Contains(parts.Raw, "Read file") || strings.Contains(parts.Raw, wiresig.Prefix) {
			t.Fatalf("history was not restored: %s", parts.Raw)
		}
	default:
		t.Fatal("no generation request")
	}
	if !strings.Contains(response.String(), wiresig.Prefix) {
		t.Fatalf("missing output envelope: %s", response.String())
	}
}
