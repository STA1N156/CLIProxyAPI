package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAntigravityModelHintsRequestIncludesCredentialProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["project"] != "account-project" {
			t.Errorf("model request project = %v, error = %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webSearchModelIds":["search-model"],"models":{"search-model":{"model":"MODEL_SEARCH"}}}`))
	}))
	defer server.Close()
	auth := &coreauth.Auth{ID: "model-project-test", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "fake", "project_id": "account-project"}}
	hints := (&Service{}).fetchAntigravityModelCapabilityHintsForAuth(context.Background(), auth)
	if _, ok := hints.WebSearchModelIDs["search-model"]; !ok {
		t.Fatal("model hints were not returned")
	}
	if hints.ModelEnums["search-model"] != "MODEL_SEARCH" {
		t.Fatal("official model enum was not returned")
	}
}
