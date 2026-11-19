package misc

import (
	"net/http"
	"strings"
)

// IsAntigravityTraceHeader reports which downstream headers the Hub forwards.
func IsAntigravityTraceHeader(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(name, "x-b3-") {
		return true
	}
	switch name {
	case "traceparent", "tracestate", "x-cloud-trace-context", "x-goog-request-params", "x-request-id":
		return true
	}
	return false
}

// SanitizeAntigravityHeaders removes client-only and hop-by-hop headers.
// Leave Accept-Encoding to net/http so automatic gzip decoding stays enabled.
func SanitizeAntigravityHeaders(req *http.Request) {
	for name := range req.Header {
		switch strings.ToLower(name) {
		case "authorization", "content-type", "user-agent":
			continue
		}
		if !IsAntigravityTraceHeader(name) {
			delete(req.Header, name)
		}
	}
	req.Close = false
}

// ApplyAntigravityOAuthHeaders mirrors the plain httpx client used by agy2api
// for OAuth and Google user-info requests.
func ApplyAntigravityOAuthHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "python-httpx/0.28.1")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive")
}
