package helps

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestAntigravityHandoffAuxiliaryRequests(t *testing.T) {
	paths := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	raw := []byte(`{"project":"p","requestType":"chat","request":{"sessionId":"-1"}}`)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1internal:generateContent", bytes.NewReader(raw))
	req = BindAntigravityClientRequest(req, raw)
	transport := &AntigravityClientTransport{Base: http.DefaultTransport}
	resp, errDo := transport.RoundTrip(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	_, _ = io.ReadAll(resp.Body)
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	seen := map[string]bool{}
	for range 5 {
		select {
		case path := <-paths:
			seen[path] = true
		case <-time.After(3 * time.Second):
			t.Fatal("missing auxiliary requests", seen)
		}
	}
	for _, path := range []string{"generateContent", "writeTrajectoryAcls", "recordTrajectoryAnalytics", "recordClientEvent", "recordCodeAssistMetrics"} {
		if !seen["/v1internal:"+path] {
			t.Error("missing", path)
		}
	}
}

func TestAntigravityHandoffProjectRetry(t *testing.T) {
	for _, kind := range []string{"chat", "agent"} {
		t.Run(kind, func(t *testing.T) {
			type event struct{ path, body string }
			events := make(chan event, 7)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				events <- event{r.URL.Path, string(raw)}
				if strings.HasSuffix(r.URL.Path, ":generateContent") && gjson.GetBytes(raw, "project").String() == "old" {
					w.WriteHeader(403)
					_, _ = io.WriteString(w, `{"error":{"details":[{"reason":"USER_PROJECT_DENIED"}]}}`)
					return
				}
				_, _ = io.WriteString(w, `{}`)
			}))
			defer server.Close()
			raw := []byte(`{"project":"old","requestId":"chat/original","request":{"sessionId":"-1"}}`)
			if kind == "agent" {
				raw = []byte(`{"project":"old","requestId":"agent/cascade/9999999999999/trajectory/2","request":{"sessionId":"-1","labels":{"cascade_id":"cascade","trajectory_id":"trajectory"}}}`)
			}
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1internal:generateContent", bytes.NewReader(raw))
			req = BindAntigravityClientRequest(req, raw)
			initial := req.Context().Value(antigravityClientInfoKey{}).(antigravityClientInfo)
			transport := &AntigravityClientTransport{Base: http.DefaultTransport, RefreshProject: func(context.Context, string) (string, error) { return "new", nil }}
			resp, errDo := transport.RoundTrip(req)
			if errDo != nil {
				t.Fatal(errDo)
			}
			_, _ = io.ReadAll(resp.Body)
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			var requests []gjson.Result
			acls := map[string]int{}
			var telemetry gjson.Result
			for range 7 {
				select {
				case e := <-events:
					switch {
					case strings.HasSuffix(e.path, ":generateContent"):
						requests = append(requests, gjson.Parse(e.body))
					case strings.HasSuffix(e.path, ":writeTrajectoryAcls"):
						acls[gjson.Get(e.body, "trajectoryId").String()]++
					case strings.HasSuffix(e.path, ":recordTrajectoryAnalytics"):
						telemetry = gjson.Get(e.body, "trajectory")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("missing retry or auxiliary requests")
				}
			}
			if resp.StatusCode != 200 || len(requests) != 2 || requests[1].Get("project").String() != "new" {
				t.Fatal("project denial was not retried once with the new project")
			}
			if requests[0].Get("requestId").String() == requests[1].Get("requestId").String() {
				t.Fatal("retry did not update requestId")
			}
			latest := telemetry.Get("trajectoryId").String()
			if kind == "chat" && (initial.trajectory == latest || acls[initial.trajectory] != 1 || acls[latest] != 1) {
				t.Fatal("chat retry must send a new trajectory ACL and matching telemetry")
			}
			if kind == "agent" && (latest != initial.trajectory || acls[latest] != 2 || telemetry.Get("cascadeId").String() != initial.cascade) {
				t.Fatal("agent retry must resend the ACL and preserve conversation IDs")
			}
		})
	}
}
