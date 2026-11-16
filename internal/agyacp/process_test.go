package agyacp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNextDrainsOutputBeforeProcessFailure(t *testing.T) {
	failure := errors.New("CLI exited")
	turn := &Turn{events: make(chan Event, 1), done: make(chan struct{}), err: failure}
	turn.events <- Event{Text: "last chunk"}
	close(turn.done)
	if got := turn.Next(context.Background()); got.Text != "last chunk" {
		t.Fatalf("lost buffered output: %+v", got)
	}
	if got := turn.Next(context.Background()); !got.Finished || !errors.Is(got.Err, failure) {
		t.Fatalf("missing terminal failure: %+v", got)
	}
}

func TestPermissionOnlyAcceptsDeclaredClientTool(t *testing.T) {
	p := &Process{sessions: map[string]*Session{"session-a": {tools: []Tool{{Name: "read"}}}}}
	for _, value := range []string{"mcp(ide-tools/read)", "mcp_ide-tools_read", "ide-tools/read"} {
		if !p.clientTool("session-a", value) {
			t.Errorf("declared tool rejected: %s", value)
		}
	}
	for _, value := range []string{"run_command", "other", "run mcp_ide-tools_read", "mcp(ide-tools/write)"} {
		if p.clientTool("session-a", value) {
			t.Errorf("unrelated tool allowed: %s", value)
		}
	}
}

func TestMCPRoutesSimultaneousSessions(t *testing.T) {
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	makeSession := func() *Session {
		return &Session{tools: []Tool{{Name: "echo"}}, turn: &Turn{life: life, events: make(chan Event, 1), pending: make(map[string]*ToolCall), done: make(chan struct{})}}
	}
	first, second := makeSession(), makeSession()
	p := &Process{ctx: life, mcpSecret: "secret", sessions: map[string]*Session{"one": first, "two": second}}
	finished := make(chan string, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"session":"` + id + `"},"_meta":{"antigravity.google/conversation_id":"` + id + `"}}}`
			request := httptest.NewRequest(http.MethodPost, "/ide", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()
			p.handleMCP(response, request)
			finished <- response.Body.String()
		}(id)
	}
	for id, s := range map[string]*Session{"one": first, "two": second} {
		select {
		case event := <-s.turn.events:
			var args map[string]string
			_ = json.Unmarshal(event.Call.Arguments, &args)
			if args["session"] != id {
				t.Fatalf("crossed sessions: %s received %s", id, args["session"])
			}
			if !s.turn.Reply(event.Call.ID, json.RawMessage(`{"ok":true}`)) {
				t.Fatal("reply rejected")
			}
		case <-time.After(time.Second):
			t.Fatal("tool call not routed")
		}
	}
	for range 2 {
		select {
		case body := <-finished:
			if !strings.Contains(body, "result") {
				t.Fatalf("bad response: %s", body)
			}
		case <-time.After(time.Second):
			t.Fatal("reply stuck")
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/ide", strings.NewReader(`{"id":2,"method":"tools/call","params":{"name":"echo","_meta":{"antigravity.google/conversation_id":"unknown"}}}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	p.handleMCP(response, request)
	if !strings.Contains(response.Body.String(), "unknown session") {
		t.Fatal("unknown session accepted")
	}
}
