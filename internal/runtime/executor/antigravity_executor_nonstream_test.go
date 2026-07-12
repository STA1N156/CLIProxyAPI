package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestAntigravityConvertStreamToNonStreamMergesUsageMetadata(t *testing.T) {
	stream := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}],"cpaUsageMetadata":{"promptTokenCount":123,"totalTokenCount":123,"cachedContentTokenCount":20}}}
{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":45,"totalTokenCount":168}}}`)

	got := (&AntigravityExecutor{}).convertStreamToNonStream(stream)
	if value := gjson.GetBytes(got, "response.usageMetadata.promptTokenCount").Int(); value != 123 {
		t.Fatalf("promptTokenCount = %d, want 123; output=%s", value, got)
	}
	if value := gjson.GetBytes(got, "response.usageMetadata.candidatesTokenCount").Int(); value != 45 {
		t.Fatalf("candidatesTokenCount = %d, want 45; output=%s", value, got)
	}
	if value := gjson.GetBytes(got, "response.usageMetadata.totalTokenCount").Int(); value != 168 {
		t.Fatalf("totalTokenCount = %d, want 168; output=%s", value, got)
	}
	if value := gjson.GetBytes(got, "response.usageMetadata.cachedContentTokenCount").Int(); value != 20 {
		t.Fatalf("cachedContentTokenCount = %d, want 20; output=%s", value, got)
	}
	if gjson.GetBytes(got, "response.cpaUsageMetadata").Exists() {
		t.Fatalf("internal cpaUsageMetadata leaked to output: %s", got)
	}
}

func TestAntigravityConvertStreamToNonStreamReconstructsMissingPromptTokens(t *testing.T) {
	stream := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":1990,"thoughtsTokenCount":1739,"totalTokenCount":28625}}}`)

	got := (&AntigravityExecutor{}).convertStreamToNonStream(stream)
	if value := gjson.GetBytes(got, "response.usageMetadata.promptTokenCount").Int(); value != 24896 {
		t.Fatalf("promptTokenCount = %d, want 24896; output=%s", value, got)
	}
}

func TestEnsureAntigravityPromptTokenCountForDirectNonStream(t *testing.T) {
	payload := []byte(`{"response":{"usageMetadata":{"candidatesTokenCount":2787,"thoughtsTokenCount":2317,"totalTokenCount":34200}}}`)

	got := ensureAntigravityPromptTokenCount(payload)
	if value := gjson.GetBytes(got, "response.usageMetadata.promptTokenCount").Int(); value != 29096 {
		t.Fatalf("promptTokenCount = %d, want 29096; output=%s", value, got)
	}
}
