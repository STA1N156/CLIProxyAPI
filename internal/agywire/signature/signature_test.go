package signature

import (
	"context"
	"strings"
	"testing"
)

func TestReferenceEnvelope(t *testing.T) {
	key := Key("rt-a")
	p := Payload{Signature: "SIG", Calls: map[string]CallMeta{"toolu_1": {Summary: "List", GemID: "call_7"}}}
	want := "agyw1.eyJzIjoiU0lHIiwibSI6eyJ0b29sdV8xIjp7InMiOiJMaXN0IiwiZyI6ImNhbGxfNyJ9fX0.hZ5qJuVVfHxaUQl-bxihrg"
	if got := Wrap(key, p); got != want {
		t.Fatalf("reference envelope mismatch: %s", got)
	}
	if got, ok := Unwrap(key, want); !ok || got.Signature != "SIG" || got.Calls["toolu_1"].GemID != "call_7" {
		t.Fatal("round trip failed")
	}
	if _, ok := Unwrap(Key("rt-b"), want); ok {
		t.Fatal("accepted another account")
	}
	if _, ok := Unwrap(key, want+"x"); ok {
		t.Fatal("accepted tampering")
	}
	raw := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"` + want + `"},{"type":"tool_use","id":"toolu_1","input":{"signature":"` + want + `"}}]}]}`)
	ctx, prepared := Prepare(context.Background(), raw, key)
	if len(Restored(ctx)) != 1 || !strings.Contains(string(prepared), `"signature":"SIG"`) || !strings.Contains(string(prepared), `"input":{"signature":"agyw1.`) {
		t.Fatalf("history preparation corrupted the request: %s", prepared)
	}
}
