package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuthBatch_AntigravityStaticModelsSurviveCanceledContext(t *testing.T) {
	var sawFetch bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawFetch = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"webSearchModelIds":["gemini-3-flash"]}`))
	}))
	defer server.Close()

	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	active := &coreauth.Auth{
		ID:       "antigravity-static-active",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
		},
	}
	disabled := &coreauth.Auth{
		ID:       "antigravity-static-disabled",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Disabled: true,
	}
	errorAuth := &coreauth.Auth{
		ID:       "antigravity-static-error",
		Provider: "antigravity",
		Status:   coreauth.StatusError,
	}

	registry := internalregistry.GetGlobalRegistry()
	for _, auth := range []*coreauth.Auth{active, disabled, errorAuth} {
		registry.UnregisterClient(auth.ID)
	}
	t.Cleanup(func() {
		for _, auth := range []*coreauth.Auth{active, disabled, errorAuth} {
			registry.UnregisterClient(auth.ID)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service.registerModelsForAuthBatch(ctx, []*coreauth.Auth{active, disabled, errorAuth})

	if sawFetch {
		t.Fatal("static Antigravity registration should not fetch upstream models")
	}
	models := registry.GetModelsForClient(active.ID)
	if len(models) == 0 {
		t.Fatal("expected active Antigravity auth to get static model registration")
	}
	for _, want := range []string{"gemini-2.5-flash", "gemini-2.5-flash-lite"} {
		found := false
		for _, model := range models {
			if model != nil && model.ID == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected active Antigravity auth to register model %s", want)
		}
	}
	if models := registry.GetModelsForClient(disabled.ID); len(models) != 0 {
		t.Fatalf("disabled auth models = %d, want 0", len(models))
	}
	if models := registry.GetModelsForClient(errorAuth.ID); len(models) != 0 {
		t.Fatalf("error auth models = %d, want 0", len(models))
	}
}
