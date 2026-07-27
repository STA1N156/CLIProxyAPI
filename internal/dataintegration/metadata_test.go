package dataintegration

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnrichNativeMetadataUsesOnlyRequestValues(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	enriched, errEnrich := enrichNativeMetadata(
		payload,
		"/v1beta/models/gemini-2.5-pro:generateContent",
		"native-session-1",
	)
	if errEnrich != nil {
		t.Fatalf("enrichNativeMetadata() error = %v", errEnrich)
	}
	if got := gjson.GetBytes(enriched, "model").String(); got != "gemini-2.5-pro" {
		t.Fatalf("model = %q, want gemini-2.5-pro", got)
	}
	if got := gjson.GetBytes(enriched, "session_id").String(); got != "native-session-1" {
		t.Fatalf("session_id = %q, want native-session-1", got)
	}
}

func TestEnrichNativeMetadataDoesNotInventSessionID(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	enriched, errEnrich := enrichNativeMetadata(
		payload,
		"/v1beta/models/gemini-2.5-flash:streamGenerateContent",
		"",
	)
	if errEnrich != nil {
		t.Fatalf("enrichNativeMetadata() error = %v", errEnrich)
	}
	if gjson.GetBytes(enriched, "session_id").Exists() {
		t.Fatal("session_id must remain absent when the request has none")
	}
}

func TestEnrichNativeMetadataPreservesBodyValues(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"model_name":"body-model","session_id":"body-session","sessionId":"other-session"}`)
	enriched, errEnrich := enrichNativeMetadata(
		payload,
		"/v1beta/models/path-model:generateContent",
		"header-session",
	)
	if errEnrich != nil {
		t.Fatalf("enrichNativeMetadata() error = %v", errEnrich)
	}
	if got := gjson.GetBytes(enriched, "model_name").String(); got != "body-model" {
		t.Fatalf("model_name = %q, want body-model", got)
	}
	if gjson.GetBytes(enriched, "model").Exists() {
		t.Fatal("model must not be added when model_name already exists")
	}
	if got := gjson.GetBytes(enriched, "session_id").String(); got != "body-session" {
		t.Fatalf("session_id = %q, want body-session", got)
	}
}
