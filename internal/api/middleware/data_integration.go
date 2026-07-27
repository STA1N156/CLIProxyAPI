package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/dataintegration"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	log "github.com/sirupsen/logrus"
)

// DataIntegrationMiddleware records authenticated JSON session requests after
// their six selectable validation results have been calculated.
func DataIntegrationMiddleware(store *dataintegration.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if store == nil || !isDataIntegrationRequest(c.Request) {
			c.Next()
			return
		}

		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.Next()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		decoded, errDecode := decodeCapturedRequestBody(body, c.Request.Header.Get("Content-Encoding"))
		if errDecode != nil || !json.Valid(decoded) {
			c.Next()
			return
		}

		if _, errRecord := store.RecordNative(
			c.Request.URL.Path,
			logging.GetGinRequestID(c),
			requestSessionID(c.Request),
			decoded,
		); errRecord != nil {
			log.WithError(errRecord).Warn("failed to store data integration request")
		}
		c.Next()
	}
}

func requestSessionID(request *http.Request) string {
	if request == nil {
		return ""
	}
	for _, name := range []string{"X-Session-ID", "Session-Id", "Session_id", "X-Claude-Code-Session-Id"} {
		if value := strings.TrimSpace(request.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func isDataIntegrationRequest(request *http.Request) bool {
	if request == nil || request.URL == nil || request.Method != http.MethodPost {
		return false
	}
	path := request.URL.Path
	switch path {
	case "/v1/chat/completions", "/v1/messages", "/v1/responses",
		"/backend-api/codex/responses", "/v1beta/interactions":
		return true
	}
	if !strings.HasPrefix(path, "/v1beta/models/") {
		return false
	}
	return strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent")
}
