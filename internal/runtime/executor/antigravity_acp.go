package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/agyacp"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type acpConversation struct {
	session                        *agyacp.Session
	process                        *agyacp.Process
	worker                         *acpWorker
	history                        []string
	resumeID, directory, workerKey string
	busy                           bool
	lastUsed                       time.Time
}
type acpWorker struct {
	key, group, directory string
	process               *agyacp.Process
	ready                 chan struct{}
	err                   error
	sessions              int
	lastUsed              time.Time
}

var acpPool = struct {
	sync.Mutex
	entries  map[string]*acpConversation
	workers  map[string]*acpWorker
	changed  chan struct{}
	waiting  int
	stopping bool
}{entries: make(map[string]*acpConversation), workers: make(map[string]*acpWorker), changed: make(chan struct{})}
var acpCleanup sync.Once

func (e *AntigravityExecutor) acpEnabled() bool {
	return e.cfg != nil && (e.cfg.Antigravity.ACP.Enabled == nil || *e.cfg.Antigravity.ACP.Enabled)
}
func acpDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func acpNotifyLocked() {
	if acpPool.waiting > 0 {
		close(acpPool.changed)
		acpPool.changed = make(chan struct{})
	}
}
func (c *acpConversation) release() {
	if c.session != nil && !c.session.Closed() && c.session.Turn() == nil {
		raw, _ := json.Marshal(struct {
			ID      string
			History []string
			Worker  string
		}{c.session.ID, c.history, c.workerKey})
		_ = os.MkdirAll(c.directory, 0700)
		_ = os.WriteFile(filepath.Join(c.directory, "session.json"), raw, 0600)
	}
	acpPool.Lock()
	c.busy = false
	c.lastUsed = time.Now()
	if c.worker != nil {
		c.worker.lastUsed = c.lastUsed
	}
	if c.session != nil && c.session.Closed() {
		acpParkLocked(c)
		c.history = nil
		c.resumeID = ""
		_ = os.Remove(filepath.Join(c.directory, "session.json"))
	}
	acpNotifyLocked()
	acpPool.Unlock()
}

// The caller holds the pool lock and has reserved or checked the conversation.
func acpParkLocked(c *acpConversation) {
	if c.session != nil {
		c.resumeID = c.session.ID
		c.process.CloseSession(c.session)
	}
	if c.worker != nil {
		c.worker.sessions--
		c.worker = nil
	}
	c.session = nil
	c.process = nil
}
func acpStopWorkerLocked(w *acpWorker) {
	for _, c := range acpPool.entries {
		if c.worker == w {
			acpParkLocked(c)
		}
	}
	if w.process != nil {
		w.process.Close()
	}
	delete(acpPool.workers, w.key)
}
func CloseAntigravityACP() {
	acpPool.Lock()
	defer acpPool.Unlock()
	acpPool.stopping = true
	for _, w := range acpPool.workers {
		if w.process != nil {
			w.process.Close()
		}
	}
	acpNotifyLocked()
}
func (e *AntigravityExecutor) acquireACP(ctx context.Context, auth *cliproxyauth.Auth, key string, tools []agyacp.Tool) (*acpConversation, error) {
	if auth == nil {
		return nil, statusErr{code: 401, msg: "missing Antigravity credential"}
	}
	cfg := e.cfg.Antigravity.ACP
	limit := cfg.MaxProcesses
	if limit <= 0 {
		limit = 128
	}
	capacity := cfg.MaxSessions
	if capacity <= 0 {
		capacity = 32
	}
	if capacity > 32 {
		capacity = 32
	}
	queueLimit := cfg.MaxQueue
	if queueLimit <= 0 {
		queueLimit = 4096
	}
	base := cfg.StateDir
	if base == "" {
		base = filepath.Join(e.cfg.AuthDir, ".acp")
	}
	proxy := antigravityProxyURL(e.cfg, auth)
	rawTools, _ := json.Marshal(tools)
	group := acpDigest(antigravityTransportScope(auth) + "\x00" + cfg.Path + "\x00" + proxy + "\x00" + cfg.Endpoint + "\x00" + base + fmt.Sprint(capacity) + "\x00" + string(rawTools))
	key = acpDigest(group + "\x00" + key)
	acpCleanup.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				acpPool.Lock()
				if acpPool.stopping {
					acpPool.Unlock()
					return
				}
				for k, c := range acpPool.entries {
					if !c.busy && time.Since(c.lastUsed) > 10*time.Minute {
						acpParkLocked(c)
						delete(acpPool.entries, k)
					}
				}
				for _, w := range acpPool.workers {
					if w.sessions == 0 && time.Since(w.lastUsed) > 10*time.Minute {
						acpStopWorkerLocked(w)
					}
				}
				acpNotifyLocked()
				acpPool.Unlock()
			}
		}()
	})
	acpPool.Lock()
	if acpPool.waiting >= queueLimit {
		acpPool.Unlock()
		return nil, statusErr{code: 503, msg: "ACP request queue is full"}
	}
	acpPool.waiting++
	for {
		if ctx.Err() != nil || acpPool.stopping {
			acpPool.waiting--
			acpPool.Unlock()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("ACP service stopped")
		}
		c := acpPool.entries[key]
		if c != nil && !c.busy && c.process != nil && c.process.Alive() && !c.session.Closed() {
			c.busy = true
			acpPool.waiting--
			acpPool.Unlock()
			return c, nil
		}
		if c == nil || !c.busy {
			if c == nil {
				c = &acpConversation{directory: filepath.Join(base, "sessions", key[:24]), lastUsed: time.Now()}
				if raw, err := os.ReadFile(filepath.Join(c.directory, "session.json")); err == nil {
					var saved struct {
						ID      string
						History []string
						Worker  string
					}
					if json.Unmarshal(raw, &saved) == nil {
						c.resumeID = saved.ID
						c.history = saved.History
						c.workerKey = saved.Worker
					}
				}
				acpPool.entries[key] = c
			}
			if c.worker != nil {
				acpParkLocked(c)
			}
			for _, w := range acpPool.workers {
				if w.process != nil && !w.process.Alive() {
					inUse := false
					for _, v := range acpPool.entries {
						if v.worker == w && v.busy {
							inUse = true
							break
						}
					}
					if !inUse {
						acpStopWorkerLocked(w)
					}
				}
			}
			var worker *acpWorker
			for _, w := range acpPool.workers {
				if w.group == group && (c.workerKey == "" || c.workerKey == w.key) && w.sessions < capacity && (w.process == nil || w.process.Alive()) {
					worker = w
					break
				}
			}
			// Reclaim a completed session before starting another process.
			if worker == nil {
				var oldest *acpConversation
				for _, v := range acpPool.entries {
					if v.worker != nil && v.worker.group == group && (c.workerKey == "" || c.workerKey == v.worker.key) && v.process.Alive() && !v.busy && v.session != nil && (v.session.Turn() == nil || time.Since(v.lastUsed) > 30*time.Second) && (oldest == nil || v.lastUsed.Before(oldest.lastUsed)) {
						oldest = v
					}
				}
				if oldest != nil {
					worker = oldest.worker
					acpParkLocked(oldest)
				}
			}
			if worker == nil && c.workerKey != "" {
				for _, w := range acpPool.workers {
					if w.group == group && w.sessions < capacity && w.process != nil && w.process.Alive() {
						worker = w
						break
					}
				}
			}
			if worker == nil && len(acpPool.workers) >= limit {
				var idle *acpWorker
				for _, w := range acpPool.workers {
					reclaimable := true
					for _, v := range acpPool.entries {
						if v.worker == w && (v.busy || (v.session != nil && v.session.Turn() != nil)) {
							reclaimable = false
							break
						}
					}
					if reclaimable && (idle == nil || w.lastUsed.Before(idle.lastUsed)) {
						idle = w
					}
				}
				if idle != nil {
					acpStopWorkerLocked(idle)
				}
			}
			start := false
			if worker == nil && len(acpPool.workers) < limit {
				workerKey := ""
				for i := 0; ; i++ {
					candidate := fmt.Sprintf("%s-%d", group[:24], i)
					if acpPool.workers[candidate] == nil {
						workerKey = candidate
						break
					}
				}
				worker = &acpWorker{key: workerKey, group: group, directory: filepath.Join(base, "workers", workerKey), ready: make(chan struct{}), lastUsed: time.Now()}
				acpPool.workers[workerKey] = worker
				start = true
			}
			if worker != nil {
				if c.workerKey != "" && c.workerKey != worker.key {
					c.resumeID = ""
					c.history = nil
				}
				c.workerKey = worker.key
				worker.sessions++
				c.worker = worker
				c.busy = true
				acpPool.waiting--
				acpPool.Unlock()
				if start {
					cliPath := cfg.Path
					if cliPath == "" {
						cliPath = os.Getenv("AGY_PATH")
					}
					process, err := agyacp.Start(ctx, agyacp.Options{Path: cliPath, Directory: worker.directory, Proxy: proxy, Endpoint: cfg.Endpoint, Token: metaStringValue(auth.Metadata, "access_token"), RefreshToken: metaStringValue(auth.Metadata, "refresh_token"), Email: metaStringValue(auth.Metadata, "email"), Expiry: tokenExpiry(auth.Metadata), MaxSessions: capacity, Tools: tools})
					acpPool.Lock()
					worker.process = process
					worker.err = err
					if acpPool.stopping && process != nil {
						process.Close()
						worker.err = fmt.Errorf("ACP service stopped")
					}
					close(worker.ready)
					acpPool.Unlock()
				}
				select {
				case <-worker.ready:
				case <-ctx.Done():
					acpPool.Lock()
					acpParkLocked(c)
					acpPool.Unlock()
					c.release()
					return nil, ctx.Err()
				}
				err := worker.err
				if err == nil {
					session, sessionErr := worker.process.NewSession(ctx, tools, c.resumeID)
					err = sessionErr
					if err == nil {
						c.process = worker.process
						c.session = session
						if c.resumeID != "" && session.ID != c.resumeID {
							c.history = nil
						}
					}
				}
				if err != nil {
					acpPool.Lock()
					acpParkLocked(c)
					if worker.sessions == 0 && worker.err != nil {
						delete(acpPool.workers, worker.key)
					}
					acpPool.Unlock()
					c.release()
					return nil, err
				}
				return c, nil
			}
		}
		changed := acpPool.changed
		acpPool.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		}
		acpPool.Lock()
	}
}

func acpTools(payload []byte) []agyacp.Tool {
	var tools []agyacp.Tool
	gjson.GetBytes(payload, "request.tools").ForEach(func(_, group gjson.Result) bool {
		declarations := group.Get("functionDeclarations")
		if !declarations.Exists() {
			declarations = group.Get("function_declarations")
		}
		declarations.ForEach(func(_, f gjson.Result) bool {
			schema := f.Get("parametersJsonSchema")
			if !schema.Exists() {
				schema = f.Get("parameters")
			}
			if !schema.Exists() {
				schema = gjson.Parse(`{"type":"object","properties":{}}`)
			}
			var parsed any
			_ = json.Unmarshal([]byte(schema.Raw), &parsed)
			normalizeACPSchema(parsed)
			raw, _ := json.Marshal(parsed)
			tools = append(tools, agyacp.Tool{Name: f.Get("name").String(), Description: f.Get("description").String(), InputSchema: raw, Meta: map[string]any{"antigravity/alwaysLoad": true}})
			return true
		})
		return true
	})
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}
func normalizeACPSchema(value any) {
	switch v := value.(type) {
	case map[string]any:
		if t, ok := v["type"].(string); ok {
			v["type"] = strings.ToLower(t)
		}
		for _, child := range v {
			normalizeACPSchema(child)
		}
	case []any:
		for _, child := range v {
			normalizeACPSchema(child)
		}
	}
}
func acpHistory(contents []gjson.Result) []string {
	out := make([]string, len(contents))
	for i, c := range contents {
		var value map[string]any
		_ = json.Unmarshal([]byte(c.Raw), &value)
		parts, _ := value["parts"].([]any)
		filtered := make([]any, 0, len(parts))
		for _, part := range parts {
			if object, ok := part.(map[string]any); ok {
				if thought, _ := object["thought"].(bool); thought {
					continue
				}
				delete(object, "thoughtSignature")
				delete(object, "thought_signature")
				delete(object, "thought")
				if len(object) == 0 {
					continue
				}
			}
			if object, ok := part.(map[string]any); ok && len(object) == 1 {
				if text, ok := object["text"].(string); ok && len(filtered) > 0 {
					if previous, ok := filtered[len(filtered)-1].(map[string]any); ok && len(previous) == 1 {
						if old, ok := previous["text"].(string); ok {
							previous["text"] = old + text
							continue
						}
					}
				}
			}
			filtered = append(filtered, part)
		}
		value["parts"] = filtered
		raw, _ := json.Marshal(value)
		out[i] = acpDigest(string(raw))
	}
	return out
}
func acpPrefix(prefix, all []string) bool {
	if len(prefix) > len(all) {
		return false
	}
	for i, v := range prefix {
		if all[i] != v {
			return false
		}
	}
	return true
}
func acpPrompt(contents []gjson.Result, system string) []any {
	parts := []any{}
	if system != "" {
		parts = append(parts, map[string]any{"type": "text", "text": "<external_system_instructions>\n" + system + "\n</external_system_instructions>"})
	}
	for _, c := range contents {
		role := c.Get("role").String()
		if role == "model" || role == "assistant" {
			parts = append(parts, map[string]any{"type": "text", "text": "<previous_assistant_message>"})
		}
		c.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if text := part.Get("text"); text.Exists() {
				parts = append(parts, map[string]any{"type": "text", "text": text.String()})
			}
			data := part.Get("inlineData")
			if !data.Exists() {
				data = part.Get("inline_data")
			}
			if data.Exists() {
				mime := data.Get("mimeType").String()
				if mime == "" {
					mime = data.Get("mime_type").String()
				}
				if strings.HasPrefix(mime, "image/") {
					parts = append(parts, map[string]any{"type": "image", "mimeType": mime, "data": data.Get("data").String()})
				}
			}
			for _, key := range []string{"functionCall", "functionResponse"} {
				if v := part.Get(key); v.Exists() {
					parts = append(parts, map[string]any{"type": "text", "text": "<previous_" + key + ">" + v.Raw + "</previous_" + key + ">"})
				}
			}
			return true
		})
		if role == "model" || role == "assistant" {
			parts = append(parts, map[string]any{"type": "text", "text": "</previous_assistant_message>"})
		}
	}
	return parts
}
func (e *AntigravityExecutor) prepareACP(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*agyacp.Process, *acpConversation, []byte, *agyacp.Turn, error) {
	model := thinking.ParseSuffix(req.Model).ModelName
	translated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatAntigravity, model, req.Payload, true)
	contents := gjson.GetBytes(translated, "request.contents").Array()
	for _, content := range contents {
		for _, part := range content.Get("parts").Array() {
			data := part.Get("inlineData")
			if !data.Exists() {
				data = part.Get("inline_data")
			}
			if part.Get("fileData").Exists() || part.Get("file_data").Exists() || (data.Exists() && !strings.HasPrefix(data.Get("mimeType").String()+data.Get("mime_type").String(), "image/")) {
				return nil, nil, nil, nil, statusErr{code: 400, msg: "official ACP supports inline images; audio, video and file URLs require conversion before this request"}
			}
		}
	}
	systemParts := gjson.GetBytes(translated, "request.systemInstruction.parts").Array()
	var texts []string
	for _, p := range systemParts {
		texts = append(texts, p.Get("text").String())
	}
	system := strings.Join(texts, "\n")
	tools := acpTools(translated)
	rawTools, _ := json.Marshal(tools)
	toolsHash := acpDigest(string(rawTools))
	history := acpHistory(contents)
	key := helps.ProviderSessionUUID("antigravity-acp", opts.Metadata, req.Metadata)
	if key == "" {
		key = gjson.GetBytes(translated, "request.sessionId").String()
	}
	if key == "" {
		for _, c := range contents {
			if c.Get("role").String() == "user" {
				key = acpDigest(c.Raw)
				break
			}
		}
	}
	key = acpDigest(key + "\x00" + system + "\x00" + toolsHash)
	c, err := e.acquireACP(ctx, auth, key, tools)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	p := c.process
	success := false
	defer func() {
		if !success {
			c.release()
		}
	}()
	turn := c.session.Turn()
	if turn != nil {
		replies := 0
		for _, content := range contents {
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				f := part.Get("functionResponse")
				if f.Exists() {
					data := json.RawMessage(f.Get("response").Raw)
					if turn.Reply(f.Get("id").String(), data) {
						replies++
					}
				}
				return true
			})
		}
		if replies == 0 {
			return nil, nil, nil, nil, statusErr{code: 409, msg: "ACP conversation is waiting for tool results"}
		}
	} else {
		start := 0
		if acpPrefix(c.history, history) && len(c.history) < len(history) {
			start = len(c.history)
		} else if len(c.history) > 0 {
			p.CloseSession(c.session)
			session, errSession := p.NewSession(ctx, tools, "")
			if errSession != nil {
				return nil, nil, nil, nil, statusErr{code: 503, msg: errSession.Error()}
			}
			c.session = session
			c.history = nil
		}
		promptSystem := ""
		if len(c.history) == 0 {
			promptSystem = system
		}
		parts := acpPrompt(contents[start:], promptSystem)
		if len(parts) == 0 {
			return nil, nil, nil, nil, statusErr{code: 400, msg: "no new input for ACP conversation"}
		}
		turn, err = p.Prompt(ctx, c.session, model, parts)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	c.history = history
	success = true
	return p, c, translated, turn, nil
}

// AntigravityACPModels caches the catalog per credential, without keeping a discovery process alive.
var acpCatalogs sync.Map

type acpCatalog struct {
	sync.Mutex
	models    []agyacp.Model
	refreshed time.Time
}

func AntigravityACPModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]agyacp.Model, error) {
	if auth == nil {
		return nil, fmt.Errorf("missing Antigravity credential")
	}
	value, _ := acpCatalogs.LoadOrStore(antigravityTransportScope(auth), &acpCatalog{})
	cache := value.(*acpCatalog)
	cache.Lock()
	defer cache.Unlock()
	if len(cache.models) > 0 && time.Since(cache.refreshed) < 3*time.Hour {
		return append([]agyacp.Model(nil), cache.models...), nil
	}
	e := NewAntigravityExecutor(cfg)
	c, err := e.acquireACP(ctx, auth, "catalog", nil)
	if err != nil {
		return nil, err
	}
	cache.models = append([]agyacp.Model(nil), c.session.Models...)
	cache.refreshed = time.Now()
	acpPool.Lock()
	acpParkLocked(c)
	acpPool.Unlock()
	c.release()
	return append([]agyacp.Model(nil), cache.models...), nil
}

func (e *AntigravityExecutor) refreshACP(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	c, err := e.acquireACP(ctx, auth, "catalog", nil)
	if err != nil {
		return nil, err
	}
	defer c.release()
	token, err := c.process.Credential()
	if err != nil {
		return nil, err
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = token.AccessToken
	if token.RefreshToken != "" {
		updated.Metadata["refresh_token"] = token.RefreshToken
	}
	updated.Metadata["expired"] = token.Expiry.Format(time.RFC3339)
	return updated, nil
}
