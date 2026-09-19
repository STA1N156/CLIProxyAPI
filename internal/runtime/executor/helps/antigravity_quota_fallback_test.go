package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestMoveAntigravitySystemInstructionToUser(t *testing.T) {
	for _, tc := range []struct {
		name, input string
	}{
		{"snake-case", `{"request":{"system_instruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"question"}]}]}}`},
		{"no-user", `{"request":{"systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"model","parts":[{"text":"history"}]}]}}`},
		{"empty-contents", `{"request":{"systemInstruction":{"parts":[{"text":"system"}]},"contents":[]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.input)
			got := MoveAntigravitySystemInstructionToUser(input)
			if gjson.GetBytes(got, "request.systemInstruction").Exists() || gjson.GetBytes(got, "request.system_instruction").Exists() {
				t.Fatalf("system instruction remains: %s", got)
			}
			if gjson.GetBytes(got, "request.contents.0.role").String() != "user" || gjson.GetBytes(got, "request.contents.0.parts.0.text").String() != "system" {
				t.Fatalf("system instruction not prepended: %s", got)
			}
			if !bytes.Equal(got, MoveAntigravitySystemInstructionToUser(got)) || string(input) != tc.input {
				t.Fatal("relocation is not idempotent or modified the original input")
			}
			if tc.name == "no-user" && gjson.GetBytes(got, "request.contents.1.parts.0.text").String() != "history" {
				t.Fatal("existing history was lost")
			}
		})
	}
}
