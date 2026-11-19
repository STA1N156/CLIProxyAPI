package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRollingBillingHeaderKeepsWireConversation(t *testing.T) {
	var previous string
	for _, value := range []string{"one", "two"} {
		raw := []byte(fmt.Sprintf(`{"request":{"systemInstruction":{"parts":[{"text":"x-anthropic-billing-header: cch=%s;\nFollow the user"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`, value))
		p, err := wireInput(raw, "model", "", "home")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.Turns[0].Text, "billing-header") {
			t.Fatal("billing header leaked upstream")
		}
		if previous != "" && p.Conv != previous {
			t.Fatal("rolling metadata changed the conversation")
		}
		previous = p.Conv
	}
}

func TestResolveWireResultsByNameAndExplicitID(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		st := &wireConv{TurnFPs: []string{"history"}, Pending: []wirePending{
			{ToolUseID: "first", Call: FunctionCall{Name: "mcp_c_lookup"}},
			{ToolUseID: "second", Call: FunctionCall{Name: "mcp_c_lookup"}},
		}}
		second := "lookup"
		if explicit {
			second = "second"
		}
		p := &wireChatParams{Turns: []wireTurn{
			{Results: []wireResult{{ID: "lookup"}}}, {},
			{Results: []wireResult{{ID: "lookup"}, {ID: second}}},
		}}
		resolveWireResultIDs(st, p)
		if p.Turns[0].Results[0].ID != "lookup" {
			t.Fatal("historical result was changed")
		}
		got := p.Turns[2].Results
		if got[0].ID != "first" || got[1].ID != "second" {
			t.Fatalf("results were not paired independently: %+v", got)
		}
	}
}

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInitialCatalogFailureKeepsHTTPStatus(t *testing.T) {
	client := &http.Client{Transport: reviewTransport(func(req *http.Request) (*http.Response, error) {
		status, body := 200, `{}`
		if strings.HasSuffix(req.URL.Path, ":fetchAvailableModels") {
			status = 503
			body = `{"error":{"message":"temporarily unavailable"}}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	engine, err := NewEmbedded(context.Background(), t.TempDir(), client, func() (string, error) { return "test-token", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	_, err = engine.d.Catalog(context.Background())
	var api *APIError
	if !errors.As(err, &api) || api.HTTPStatus != 503 {
		t.Fatalf("expected original 503, got %v", err)
	}
}
