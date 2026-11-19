package helps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type antigravityBackground struct {
	once sync.Once
	jobs chan func(context.Context)
}

var antigravityBackgroundJobs antigravityBackground

// StartAntigravityBackground binds a bounded, shared worker pool to the service.
func StartAntigravityBackground(ctx context.Context) {
	antigravityBackgroundJobs.once.Do(func() {
		antigravityBackgroundJobs.jobs = make(chan func(context.Context), 4096)
		for range 8 {
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case work := <-antigravityBackgroundJobs.jobs:
						work(ctx)
					}
				}
			}()
		}
	})
}

func submitAntigravityBackground(work func(context.Context)) {
	StartAntigravityBackground(context.Background())
	select {
	case antigravityBackgroundJobs.jobs <- work:
	default:
		log.Debug("antigravity: background queue full; skipping telemetry")
	}
}

// AntigravityClientTransport mirrors the handoff auxiliary requests around generation.
// The underlying transport is also used for telemetry so it cannot recurse.
type AntigravityClientTransport struct {
	Base           http.RoundTripper
	RefreshProject func(context.Context, string) (string, error)
}

type antigravityClientInfo struct{ project, cascade, trajectory string }
type antigravityClientInfoKey struct{}

// BindAntigravityClientRequest avoids copying the request body on the hot path.
func BindAntigravityClientRequest(req *http.Request, payload []byte) *http.Request {
	info := antigravityClientInfo{project: gjson.GetBytes(payload, "project").String(),
		cascade:    gjson.GetBytes(payload, "request.labels.cascade_id").String(),
		trajectory: gjson.GetBytes(payload, "request.labels.trajectory_id").String()}
	if info.trajectory == "" {
		session := gjson.GetBytes(payload, "request.sessionId").String()
		digest := sha256.Sum256([]byte(session + ":cascade"))
		info.cascade = antigravityUUID(digest[:16])
		info.trajectory = uuid.NewString()
	}
	return req.WithContext(context.WithValue(req.Context(), antigravityClientInfoKey{}, info))
}

func (t *AntigravityClientTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(req.URL.Path, ":generateContent") && !strings.HasSuffix(req.URL.Path, ":streamGenerateContent") {
		return t.Base.RoundTrip(req)
	}
	info, ok := req.Context().Value(antigravityClientInfoKey{}).(antigravityClientInfo)
	if !ok {
		return t.Base.RoundTrip(req)
	}
	trajectory, cascade := info.trajectory, info.cascade
	baseURL := req.URL.Scheme + "://" + req.URL.Host
	headers := req.Header.Clone()
	sendACL := func(id string) {
		submitAntigravityBackground(func(ctx context.Context) {
			AntigravityBackgroundPost(ctx, t.Base, baseURL, "writeTrajectoryAcls", headers, map[string]any{"trajectoryId": id})
		})
	}
	sendACL(trajectory)
	resp, err := t.Base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden && req.GetBody != nil && t.RefreshProject != nil {
		body, errRead := io.ReadAll(resp.Body)
		if errRead != nil {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Debugf("antigravity: close error response: %v", errClose)
			}
			return nil, errRead
		}
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("antigravity: close error response: %v", errClose)
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if AntigravityErrorReason(body) == "TOS_VIOLATION" {
			log.Warn("antigravity: upstream reported TOS_VIOLATION")
		}
		if AntigravityErrorReason(body) == "USER_PROJECT_DENIED" {
			project, errProject := t.RefreshProject(req.Context(), strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
			if errProject == nil && project != "" && project != info.project {
				original, errBody := req.GetBody()
				if errBody != nil {
					return resp, nil
				}
				payload, errRead := io.ReadAll(original)
				if errClose := original.Close(); errClose != nil {
					log.Debugf("antigravity: close request copy: %v", errClose)
				}
				if errRead != nil {
					return resp, nil
				}
				updated, errSet := sjson.SetBytes(payload, "project", project)
				if errSet == nil {
					id := strings.Split(gjson.GetBytes(updated, "requestId").String(), "/")
					requestID := "chat/" + uuid.NewString()
					if len(id) == 5 && id[0] == "agent" {
						stamp := time.Now().UnixMilli()
						if previous, errParse := strconv.ParseInt(id[2], 10, 64); errParse == nil && stamp <= previous {
							stamp = previous + 1
						}
						id[2] = strconv.FormatInt(stamp, 10)
						requestID = strings.Join(id, "/")
					}
					updated, _ = sjson.SetBytes(updated, "requestId", requestID)
					retry := req.Clone(req.Context())
					retry.Body = io.NopCloser(bytes.NewReader(updated))
					retry.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(updated)), nil }
					if req.ContentLength >= 0 {
						retry.ContentLength = int64(len(updated))
					}
					retry = BindAntigravityClientRequest(retry, updated)
					info = retry.Context().Value(antigravityClientInfoKey{}).(antigravityClientInfo)
					cascade, trajectory = info.cascade, info.trajectory
					sendACL(trajectory)
					resp, err = t.Base.RoundTrip(retry)
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		project := info.project
		resp.Body = &antigravityTelemetryBody{ReadCloser: resp.Body, complete: func() {
			submitAntigravityBackground(func(ctx context.Context) { t.recordTurn(ctx, baseURL, headers, project, cascade, trajectory) })
		}}
	}
	return resp, nil
}

type antigravityTelemetryBody struct {
	io.ReadCloser
	once     sync.Once
	complete func()
}

func (b *antigravityTelemetryBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.once.Do(b.complete)
	}
	return n, err
}

// AntigravityBackgroundPost sends an auxiliary request through the account pool.
func AntigravityBackgroundPost(ctx context.Context, transport http.RoundTripper, baseURL, path string, headers http.Header, body any) int {
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return 0
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1internal:"+path, bytes.NewReader(raw))
	if errReq != nil {
		return 0
	}
	req.Header = headers.Clone()
	misc.SanitizeAntigravityHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	resp, errDo := transport.RoundTrip(req)
	if errDo != nil {
		log.Debugf("antigravity: %s failed: %v", path, errDo)
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if errClose := resp.Body.Close(); errClose != nil {
		log.Debugf("antigravity: %s close: %v", path, errClose)
	}
	log.Debugf("antigravity: %s returned %d", path, resp.StatusCode)
	return resp.StatusCode
}

func (t *AntigravityClientTransport) CloseIdleConnections() {
	if closer, ok := t.Base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t *AntigravityClientTransport) recordTurn(ctx context.Context, base string, headers http.Header, project, cascade, trajectory string) {
	AntigravityBackgroundPost(ctx, t.Base, base, "recordTrajectoryAnalytics", headers, map[string]any{
		"trajectory": map[string]any{"cascadeId": cascade, "trajectoryId": trajectory,
			"source": "CORTEX_TRAJECTORY_SOURCE_INTERACTIVE_CASCADE", "trajectoryType": "CORTEX_TRAJECTORY_TYPE_INTERACTIVE_CASCADE",
			"steps": []any{}, "metadata": map[string]any{"agentScript": map[string]any{"name": "self"}}},
	})
	AntigravityBackgroundPost(ctx, t.Base, base, "recordClientEvent", headers, map[string]any{
		"project": project, "ide_type": "ANTIGRAVITY", "metric": map[string]any{"conversation_offered": map[string]any{}},
	})
	AntigravityBackgroundPost(ctx, t.Base, base, "recordCodeAssistMetrics", headers, map[string]any{
		"project": project, "request_id": uuid.NewString(), "metrics": []any{},
		"metadata": map[string]any{"ideType": "ANTIGRAVITY", "ideVersion": misc.AntigravityLatestVersion(),
			"pluginVersion": misc.AntigravityLatestVersion(), "platform": "WINDOWS_AMD64", "updateChannel": "stable", "ideName": "antigravity"},
	})
}

func AntigravityErrorReason(body []byte) string {
	for _, detail := range gjson.GetBytes(body, "error.details").Array() {
		if reason := detail.Get("reason").String(); reason != "" {
			return reason
		}
	}
	return ""
}
