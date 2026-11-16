package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/agyacp"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func acpResponse(model, id string, parts []any, finish string, usage any) []byte {
	candidate := map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": parts}}
	if finish != "" {
		candidate["finishReason"] = finish
	}
	response := map[string]any{"candidates": []any{candidate}, "modelVersion": model, "responseId": id, "createTime": time.Now().UTC().Format(time.RFC3339Nano)}
	if usage != nil {
		response["usageMetadata"] = usage
	}
	raw, _ := json.Marshal(map[string]any{"response": response})
	return raw
}
func acpEventPart(event agyacp.Event) any {
	if event.Call != nil {
		return map[string]any{"functionCall": map[string]any{"id": event.Call.ID, "name": event.Call.Name, "args": json.RawMessage(event.Call.Arguments)}}
	}
	if event.Text != "" {
		return map[string]any{"text": event.Text, "thought": event.Thought}
	}
	return nil
}
func acpEventError(event agyacp.Event) error {
	if event.Err != nil {
		return event.Err
	}
	text := strings.TrimSpace(event.Text)
	if event.Thought || (!strings.HasPrefix(text, "⚠") && !strings.HasPrefix(text, "Agent execution terminated")) {
		return nil
	}
	code := http.StatusBadGateway
	switch {
	case strings.Contains(text, "Terms of Service"), strings.Contains(text, "code 403"):
		code = 403
	case strings.Contains(text, "Resource has been exhausted"), strings.Contains(text, "code 429"):
		code = 429
	case strings.Contains(text, "credits balance"):
		code = 402
	}
	return statusErr{code: code, msg: text}
}
func acpUsage(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	r := gjson.ParseBytes(raw)
	input := r.Get("inputTokens").Int()
	output := r.Get("outputTokens").Int()
	if input == 0 {
		input = r.Get("input_tokens").Int()
	}
	if output == 0 {
		output = r.Get("output_tokens").Int()
	}
	cached := r.Get("cachedReadTokens").Int()
	if r.Get("totalTokens").Int() >= input+output+cached {
		input += cached + r.Get("cachedWriteTokens").Int()
	}
	return map[string]any{"promptTokenCount": input, "candidatesTokenCount": output, "totalTokenCount": input + output, "cachedContentTokenCount": cached}
}
func (e *AntigravityExecutor) executeACP(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	p, c, translated, turn, err := e.prepareACP(ctx, auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer c.release()
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	parts := []any{}
	var usage any
	finish := "STOP"
	id := "acp-" + c.session.ID
	for {
		event := turn.Next(ctx)
		if err = acpEventError(event); err != nil {
			p.Cancel(c.session)
			reporter.PublishFailure(ctx, err)
			return cliproxyexecutor.Response{}, err
		}
		if part := acpEventPart(event); part != nil {
			parts = append(parts, part)
		}
		if event.Usage != nil {
			usage = acpUsage(event.Usage)
		}
		if event.Call != nil {
			for _, sibling := range acpToolBatch(ctx, turn) {
				if err = acpEventError(sibling); err != nil {
					p.Cancel(c.session)
					return cliproxyexecutor.Response{}, err
				}
				if part := acpEventPart(sibling); part != nil {
					parts = append(parts, part)
				}
			}
			break
		}
		if event.Finished {
			if event.StopReason == "max_tokens" {
				finish = "MAX_TOKENS"
			}
			break
		}
	}

	raw := acpResponse(req.Model, id, parts, finish, usage)
	c.history = append(c.history, acpHistory([]gjson.Result{gjson.GetBytes(raw, "response.candidates.0.content")})[0])
	var param any
	converted := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatAntigravity, cliproxyexecutor.ResponseFormatOrSource(opts), req.Model, req.Payload, translated, raw, &param)
	reporter.Publish(ctx, helps.ParseAntigravityUsage(raw))
	return cliproxyexecutor.Response{Payload: converted}, nil
}
func (e *AntigravityExecutor) executeACPStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	p, c, translated, turn, err := e.prepareACP(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	// Delay committing HTTP 200 until the CLI has produced output or a tool call.
	var first agyacp.Event
	var initialUsage any
	for {
		first = turn.Next(ctx)
		if err = acpEventError(first); err != nil {
			p.Cancel(c.session)
			c.release()
			return nil, err
		}
		if first.Usage != nil {
			initialUsage = acpUsage(first.Usage)
		}
		if first.Text != "" || first.Call != nil || first.Finished {
			break
		}
	}
	out := make(chan cliproxyexecutor.StreamChunk, 32)
	go func() {
		defer close(out)
		defer c.release()
		reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
		var param any
		usage := initialUsage
		parts := []any{}
		finish := "STOP"
		id := "acp-" + c.session.ID
		send := func(raw []byte) bool {
			for _, chunk := range sdktranslator.TranslateStream(ctx, sdktranslator.FormatAntigravity, cliproxyexecutor.ResponseFormatOrSource(opts), req.Model, req.Payload, translated, raw, &param) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		event := first
		for {
			if err := acpEventError(event); err != nil {
				reporter.PublishFailure(ctx, err)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: err}:
				case <-ctx.Done():
				}
				p.Cancel(c.session)
				return
			}
			if event.Usage != nil {
				usage = acpUsage(event.Usage)
			}
			if part := acpEventPart(event); part != nil {
				parts = append(parts, part)
				if !send(acpResponse(req.Model, id, []any{part}, "", nil)) {
					p.Cancel(c.session)
					return
				}
			}
			if event.Call != nil {
				for _, sibling := range acpToolBatch(ctx, turn) {
					if err := acpEventError(sibling); err != nil {
						p.Cancel(c.session)
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: err}:
						case <-ctx.Done():
						}
						return
					}
					if part := acpEventPart(sibling); part != nil {
						parts = append(parts, part)
						if !send(acpResponse(req.Model, id, []any{part}, "", nil)) {
							p.Cancel(c.session)
							return
						}
					}
				}
				break
			}
			if event.Finished {
				if event.StopReason == "max_tokens" {
					finish = "MAX_TOKENS"
				}
				break
			}
			event = turn.Next(ctx)
		}
		raw := acpResponse(req.Model, id, nil, finish, usage)
		full := acpResponse(req.Model, id, parts, finish, usage)
		c.history = append(c.history, acpHistory([]gjson.Result{gjson.GetBytes(full, "response.candidates.0.content")})[0])
		if !send(raw) {
			p.Cancel(c.session)
			return
		}
		_ = send([]byte("[DONE]"))
		reporter.Publish(ctx, helps.ParseAntigravityUsage(full))
	}()
	return &cliproxyexecutor.StreamResult{Chunks: out}, nil
}

// Group parallel MCP calls without delaying ordinary text streaming.
func acpToolBatch(ctx context.Context, turn *agyacp.Turn) []agyacp.Event {
	timer := time.NewTimer(30 * time.Millisecond)
	defer timer.Stop()
	var events []agyacp.Event
	for {
		select {
		case event := <-turn.Events():
			events = append(events, event)
			if event.Finished {
				return events
			}
		case <-timer.C:
			return events
		case <-ctx.Done():
			return events
		case <-turn.Done():
			return events
		}
	}
}
