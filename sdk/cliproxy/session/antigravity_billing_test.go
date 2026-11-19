package session

import (
	"fmt"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestAntigravityBillingHeaderDoesNotChangeAffinity(t *testing.T) {
	var previous string
	for _, value := range []string{"one", "two"} {
		raw := []byte(fmt.Sprintf(`{"systemInstruction":{"parts":[{"text":"x-anthropic-billing-header: cch=%s;\nFollow the user"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, value))
		id := DeriveAntigravityID(sdktranslator.FormatGemini, raw, "caller", "model")
		if previous != "" && id != previous {
			t.Fatal("billing metadata changed account affinity")
		}
		previous = id
	}
}
