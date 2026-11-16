package agyacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// RunMCP serves one CLI-owned stdio MCP connection through the local account relay.
func RunMCP(ctx context.Context, endpoint, secret string) error {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var writeMu sync.Mutex
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		var message rpcMessage
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(message.ID) == 0 {
			continue
		}
		wg.Add(1)
		go func(m rpcMessage) {
			defer wg.Done()
			raw, _ := json.Marshal(m)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(raw)))
			var result rpcMessage
			if err == nil {
				req.Header.Set("Authorization", "Bearer "+secret)
				var resp *http.Response
				resp, err = client.Do(req)
				if err == nil {
					err = json.NewDecoder(resp.Body).Decode(&result)
					_ = resp.Body.Close()
				}
			}
			if err != nil {
				result = rpcMessage{JSONRPC: "2.0", ID: m.ID, Error: &rpcError{Code: -32603, Message: "CPA MCP relay disconnected"}}
			}
			writeMu.Lock()
			_ = encoder.Encode(result)
			writeMu.Unlock()
		}(message)
	}
}

func (p *Process) startMCP() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	p.mcpSecret = uuid.NewString()
	p.mcpURL = "http://" + listener.Addr().String()
	p.mcp = &http.Server{Handler: http.HandlerFunc(p.handleMCP)}
	go func() { _ = p.mcp.Serve(listener) }()
	return nil
}

func (p *Process) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+p.mcpSecret {
		http.Error(w, "forbidden", 403)
		return
	}
	var m rpcMessage
	if json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&m) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if len(m.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result := rpcMessage{JSONRPC: "2.0", ID: m.ID}
	p.mu.Lock()
	s := p.sessions[strings.TrimPrefix(r.URL.Path, "/")]
	p.mu.Unlock()
	var value any
	switch m.Method {
	case "initialize":
		var init struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &init)
		value = map[string]any{"protocolVersion": init.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{"listChanged": true}}, "serverInfo": map[string]any{"name": "cpa-tools", "version": "1.0"}}
	case "ping":
		value = map[string]any{}
	case "tools/list":

		tools := []Tool{}
		if s != nil {
			tools = s.tools
			s.listedOnce.Do(func() { close(s.listed) })
		}
		value = map[string]any{"tools": tools}
	case "tools/call":
		var route struct {
			Meta struct {
				ConversationID string `json:"antigravity.google/conversation_id"`
			} `json:"_meta"`
		}
		_ = json.Unmarshal(m.Params, &route)
		sessionID := route.Meta.ConversationID
		p.mu.Lock()
		s = p.sessions[sessionID]
		p.mu.Unlock()

		if s == nil {
			result.Error = &rpcError{Code: -32602, Message: "unknown session"}
			break
		}
		var args struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(m.Params, &args)
		s.mu.Lock()
		turn := s.turn
		s.mu.Unlock()
		if turn == nil {
			result.Error = &rpcError{Code: -32603, Message: "no active turn"}
			break
		}
		declared := false
		for _, tool := range s.tools {
			if tool.Name == args.Name {
				declared = true
				break
			}
		}
		if !declared {
			result.Error = &rpcError{Code: -32602, Message: "undeclared client tool"}
			break
		}
		call := &ToolCall{ID: uuid.NewString(), Name: args.Name, Arguments: args.Arguments, reply: make(chan json.RawMessage, 1)}
		turn.mu.Lock()
		turn.pending[call.ID] = call
		turn.mu.Unlock()
		select {
		case turn.events <- Event{Call: call}:
		case <-turn.life.Done():
			return
		case <-r.Context().Done():
			return
		}
		select {
		case raw := <-call.reply:
			value = map[string]any{"content": []map[string]any{{"type": "text", "text": string(raw)}}}
		case <-p.ctx.Done():
			return
		case <-r.Context().Done():
			return
		case <-turn.life.Done():
			result.Error = &rpcError{Code: -32603, Message: "turn ended before tool result"}
		}
	default:
		result.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("unknown MCP method %s", m.Method)}
	}
	if result.Error == nil {
		result.Result, _ = json.Marshal(value)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
