package misc

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAntigravityHandoffNegotiatesHTTP2(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("protocol = %s", r.Proto)
		}
		if r.Header.Get("Connection") != "" || r.Header.Get("Accept") != "" {
			t.Error("unexpected forced headers")
		}
		_, _ = io.WriteString(w, "OK")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	transport := CloneAntigravityTransport(&http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	req.Header.Set("User-Agent", AntigravityUserAgent())
	req.Header.Set("X-Goog-User-Project", "must-be-removed")
	SanitizeAntigravityHeaders(req)
	if req.Header.Get("X-Goog-User-Project") != "" {
		t.Fatal("project header was forwarded")
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if resp.ProtoMajor != 2 {
		t.Fatalf("response protocol = %s", resp.Proto)
	}
}
