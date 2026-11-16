package helps

import (
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestWrapAntigravityUserMessages(t *testing.T) {
	input := []byte(`{"request":{"sessionId":"wrap-test","contents":[{"role":"user","parts":[{"text":"hello"},{"inlineData":{"data":"image"}}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"functionResponse":{"name":"tool","response":{"text":"keep"}}}]}]}}`)
	first := WrapAntigravityUserMessages(input)
	text := gjson.GetBytes(first, "request.contents.0.parts.0.text").String()
	prefix := "<USER_REQUEST>\nhello\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: "
	suffix := ".\n</ADDITIONAL_METADATA>"
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		t.Fatalf("unexpected wrapper: %s", text)
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix)); err != nil {
		t.Fatal(err)
	}
	if string(first) != string(WrapAntigravityUserMessages(first)) || string(first) != string(WrapAntigravityUserMessages(input)) {
		t.Fatal("wrapping or retry changed existing message")
	}
	for _, path := range []string{"request.contents.0.parts.1", "request.contents.1", "request.contents.2"} {
		if gjson.GetBytes(first, path).Raw != gjson.GetBytes(input, path).Raw {
			t.Fatalf("changed non-text data at %s", path)
		}
	}
}
