package executor

import (
	"context"

	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

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

func TestCloneTransportWithHTTP11_SetsPoolLimits(t *testing.T) {
	base := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 2,
	}
	transport := cloneTransportWithHTTP11(base)

	if transport == base {
		t.Fatal("transport should be cloned")
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("Antigravity transport must use HTTP/1.1")
	}
	if transport.MaxIdleConns < antigravityMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConns = %d, want at least %d", transport.MaxIdleConns, antigravityMaxIdleConnsPerHost)
	}
	if transport.MaxIdleConnsPerHost < antigravityMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want at least %d", transport.MaxIdleConnsPerHost, antigravityMaxIdleConnsPerHost)
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
