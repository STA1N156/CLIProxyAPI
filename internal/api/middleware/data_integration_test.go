package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/dataintegration"
)

func TestDataIntegrationMiddlewareRecordsAndRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, errStore := dataintegration.NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	router := gin.New()
	router.Use(DataIntegrationMiddleware(store))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil || !strings.Contains(string(body), `"messages"`) {
			c.Status(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	stats, errStats := store.Stats(0, dataintegration.TimeRange{})
	if errStats != nil {
		t.Fatalf("Stats() error = %v", errStats)
	}
	if stats.TotalRequests != 1 {
		t.Fatalf("stored requests = %d, want 1", stats.TotalRequests)
	}
}

func TestDataIntegrationMiddlewareSkipsUnauthorizedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, errStore := dataintegration.NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	router := gin.New()
	router.POST(
		"/v1/responses",
		func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) },
		DataIntegrationMiddleware(store),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":[{"role":"user","content":"hello"}]}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	stats, errStats := store.Stats(0, dataintegration.TimeRange{})
	if errStats != nil {
		t.Fatalf("Stats() error = %v", errStats)
	}
	if stats.TotalRequests != 0 {
		t.Fatalf("stored unauthorized requests = %d, want 0", stats.TotalRequests)
	}
}

func TestRequestSessionIDUsesOnlyExplicitHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Session_id", "native-session")
	if got := requestSessionID(request); got != "native-session" {
		t.Fatalf("requestSessionID() = %q, want native-session", got)
	}

	request.Header.Del("Session_id")
	request.Header.Set("X-Client-Request-Id", "not-a-session")
	if got := requestSessionID(request); got != "" {
		t.Fatalf("requestSessionID() = %q, want empty", got)
	}
}
