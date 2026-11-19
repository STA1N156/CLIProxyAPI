package helps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/agywire/agent"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

type wireRequestKey struct{}

type FailedTransport struct{ Err error }

func (t FailedTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.Err }

type wireRequest struct {
	payload             []byte
	model, conversation string
}

func BindAntigravityWireRequest(req *http.Request, payload []byte, model, conversation string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), wireRequestKey{}, wireRequest{payload, model, conversation}))
}

type wireTokenSource struct{ fn func() (string, error) }
type wireAccount struct {
	once   sync.Once
	ready  chan struct{}
	engine *agent.Embedded
	err    error
	token  atomic.Pointer[wireTokenSource]
	active atomic.Int64
	last   atomic.Int64
}

var wireAccounts sync.Map
var wireSweepOnce sync.Once

// AntigravityWireTransport keeps the account engine in-process and reuses CPA pools.
type AntigravityWireTransport struct {
	Base                       http.RoundTripper
	Account, StateDir, BaseURL string
	Token                      func() (string, error)
	SignatureKey               [32]byte
	Record                     func(*http.Request, []byte)
}

func (t *AntigravityWireTransport) account() (*wireAccount, error) {
	key := t.Account + "|" + t.BaseURL + "|" + t.StateDir
	var entry *wireAccount
	for {
		value, exists := wireAccounts.Load(key)
		if !exists {
			value, _ = wireAccounts.LoadOrStore(key, &wireAccount{ready: make(chan struct{})})
		}
		entry = value.(*wireAccount)
		acquired := false
		for n := entry.active.Load(); n >= 0; n = entry.active.Load() {
			if entry.active.CompareAndSwap(n, n+1) {
				acquired = true
				break
			}
		}
		if acquired {
			break
		}
		wireAccounts.CompareAndDelete(key, entry)
	}
	entry.token.Store(&wireTokenSource{t.Token})
	entry.last.Store(time.Now().UnixNano())
	entry.once.Do(func() {
		defer close(entry.ready)
		sum := sha256.Sum256([]byte(key))
		home := filepath.Join(t.StateDir, hex.EncodeToString(sum[:]), "home")
		base := t.Base
		if t.Record != nil {
			base = wireLoggingTransport{base, t.Record}
		}
		if t.BaseURL != "" && t.BaseURL != "https://daily-cloudcode-pa.googleapis.com" {
			endpoint, err := url.Parse(t.BaseURL)
			if err != nil {
				entry.err = err
				return
			}
			base = wireEndpointTransport{base, endpoint}
		}
		entry.engine, entry.err = agent.NewEmbedded(context.Background(), home, &http.Client{Transport: base}, func() (string, error) { return entry.token.Load().fn() })
	})
	wireSweepOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for now := range ticker.C {
				wireAccounts.Range(func(key, value any) bool {
					e := value.(*wireAccount)
					select {
					case <-e.ready:
					default:
						return true
					}
					if now.Sub(time.Unix(0, e.last.Load())) > time.Hour && e.active.CompareAndSwap(0, -1) {
						wireAccounts.CompareAndDelete(key, e)
						if e.engine != nil {
							e.engine.Close()
						}
					}
					return true
				})
			}
		}()
	})
	if entry.err != nil {
		entry.active.Add(-1)
		wireAccounts.CompareAndDelete(key, entry)
	}
	return entry, entry.err
}

type wireEndpointTransport struct {
	base     http.RoundTripper
	endpoint *url.URL
}

func (t wireEndpointTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	target := *req.URL
	target.Scheme = t.endpoint.Scheme
	target.Host = t.endpoint.Host
	target.Path = strings.TrimRight(t.endpoint.Path, "/") + target.Path
	clone.URL = &target
	clone.Host = t.endpoint.Host
	return t.base.RoundTrip(clone)
}

func CloseAntigravityWire() {
	wireAccounts.Range(func(key, value any) bool {
		e := value.(*wireAccount)
		<-e.ready
		if wireAccounts.CompareAndDelete(key, e) && e.engine != nil {
			e.engine.Close()
		}
		return true
	})
}

type wireLoggingTransport struct {
	base   http.RoundTripper
	record func(*http.Request, []byte)
}

func (t wireLoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	t.record(req, body)
	return t.base.RoundTrip(req)
}

type wireEvent struct {
	data []byte
	err  error
}
type wireBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	stop   func() bool
}

func (b *wireBody) Close() error { b.stop(); b.cancel(); return b.ReadCloser.Close() }

func (t *AntigravityWireTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	p, ok := req.Context().Value(wireRequestKey{}).(wireRequest)
	if !ok {
		return t.Base.RoundTrip(req)
	}
	entry, err := t.account()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(req.Context())
	events := make(chan wireEvent, 32)
	go func() {
		defer entry.active.Add(-1)
		defer func() { entry.last.Store(time.Now().UnixNano()) }()
		err := entry.engine.Stream(ctx, p.payload, p.model, p.conversation, func(data []byte) error {
			select {
			case events <- wireEvent{data: data}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, t.SignatureKey)
		select {
		case events <- wireEvent{err: err}:
		case <-ctx.Done():
		}
		close(events)
	}()
	var first wireEvent
	select {
	case event, ok := <-events:
		if !ok {
			cancel()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, io.ErrUnexpectedEOF
		}
		first = event
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	if first.err != nil {
		cancel()
		var configError *agent.ConfigurationError
		if errors.As(first.err, &configError) {
			return nil, cliproxyauth.NewRequestScopedError(configError.Error(), http.StatusNotImplemented)
		}
		if errors.Is(first.err, context.Canceled) || errors.Is(first.err, context.DeadlineExceeded) {
			return nil, first.err
		}
		code := http.StatusBadGateway
		message := first.err.Error()
		var api *agent.APIError
		if errors.As(first.err, &api) {
			code = api.HTTPStatus
			if api.Raw != "" {
				message = api.Raw
			}
		}
		if errors.Is(first.err, agent.ErrAuth) {
			code = http.StatusUnauthorized
		}
		if strings.HasPrefix(message, "unknown model:") {
			code = http.StatusNotFound
		}
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(message)), Request: req}, nil
	}
	if !strings.Contains(req.URL.Path, ":streamGenerateContent") {
		defer cancel()
		var parts []json.RawMessage
		var last []byte
		consume := func(event wireEvent) error {
			if event.err != nil {
				return event.err
			}
			if len(event.data) == 0 {
				return nil
			}
			last = event.data
			for _, p := range gjson.GetBytes(last, "response.candidates.0.content.parts").Array() {
				parts = append(parts, json.RawMessage(p.Raw))
			}
			return nil
		}
		_ = consume(first)
		for event := range events {
			if err := consume(event); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var result map[string]any
		if len(last) > 0 {
			_ = json.Unmarshal(last, &result)
		} else {
			result = map[string]any{"response": map[string]any{"candidates": []any{map[string]any{"content": map[string]any{}, "finishReason": "STOP"}}}}
		}
		response := result["response"].(map[string]any)
		response["candidates"].([]any)[0].(map[string]any)["content"] = map[string]any{"role": "model", "parts": parts}
		raw, _ := json.Marshal(result)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: req}, nil
	}
	reader, writer := io.Pipe()
	stopClose := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
	go func() {
		defer cancel()
		defer stopClose()
		write := func(event wireEvent) error {
			if event.err != nil {
				return event.err
			}
			if len(event.data) > 0 {
				if _, err := writer.Write(append(append([]byte("data: "), event.data...), '\n', '\n')); err != nil {
					return err
				}
			}
			return nil
		}
		if err := write(first); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		for event := range events {
			if err := write(event); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
		}
		_ = writer.CloseWithError(ctx.Err())
	}()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &wireBody{ReadCloser: reader, cancel: cancel, stop: stopClose}, Request: req}, nil
}

func (t *AntigravityWireTransport) CloseIdleConnections() {
	if c, ok := t.Base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
