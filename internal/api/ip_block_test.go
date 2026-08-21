package api

import (
	"net/http/httptest"
	"testing"
)

func TestRequestHasIP(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/responses", nil)
	request.RemoteAddr = "10.42.0.1:1234"
	request.Header.Set("X-Forwarded-For", "127.0.0.1, 107.173.42.94")
	if !requestHasIP(request, blockedClientIP) {
		t.Fatal("blocked forwarded IP was accepted")
	}

	request.Header.Set("X-Forwarded-For", "127.0.0.1, 203.0.113.1")
	if requestHasIP(request, blockedClientIP) {
		t.Fatal("unrelated forwarded IP was blocked")
	}
}
