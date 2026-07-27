package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/dataintegration"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	log "github.com/sirupsen/logrus"
)

const maxCapturedBodyPreallocation = 1 << 20

// DataIntegrationMiddleware copies authenticated session requests and hands
// them to the background pipeline after the proxy handler has completed.
func DataIntegrationMiddleware(store *dataintegration.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if store == nil || !isDataIntegrationRequest(c.Request) || c.Request.Body == nil {
			c.Next()
			return
		}

		body := newCapturedRequestBody(c.Request.Body, c.Request.ContentLength)
		c.Request.Body = body
		capturedAt := time.Now().UTC()
		path := c.Request.URL.Path
		requestID := logging.GetGinRequestID(c)
		sessionID := requestSessionID(c.Request)
		contentEncoding := c.Request.Header.Get("Content-Encoding")

		c.Next()
		if errRecord := store.EnqueueCapturedRequest(
			capturedAt,
			path,
			requestID,
			sessionID,
			contentEncoding,
			body.Bytes(),
		); errRecord != nil &&
			!errors.Is(errRecord, dataintegration.ErrQueueFull) &&
			!errors.Is(errRecord, dataintegration.ErrClearing) {
			log.WithError(errRecord).Warn("failed to queue data integration request")
		}
	}
}

type capturedRequestBody struct {
	source io.ReadCloser
	data   bytes.Buffer
}

func newCapturedRequestBody(source io.ReadCloser, contentLength int64) *capturedRequestBody {
	body := &capturedRequestBody{source: source}
	if contentLength > 0 && contentLength <= maxCapturedBodyPreallocation {
		body.data.Grow(int(contentLength))
	}
	return body
}

func (b *capturedRequestBody) Read(buffer []byte) (int, error) {
	read, errRead := b.source.Read(buffer)
	if read > 0 {
		_, _ = b.data.Write(buffer[:read])
	}
	return read, errRead
}

func (b *capturedRequestBody) Close() error {
	return b.source.Close()
}

func (b *capturedRequestBody) Bytes() []byte {
	return b.data.Bytes()
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
