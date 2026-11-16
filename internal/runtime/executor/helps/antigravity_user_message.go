package helps

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var antigravityMessageTimes = cache.NewBoundedLRU[string, string](32768, nil)

// WrapAntigravityUserMessages preserves multimodal parts and stabilizes history timestamps.
func WrapAntigravityUserMessages(payload []byte) []byte {
	session := gjson.GetBytes(payload, "request.sessionId").String()
	gjson.GetBytes(payload, "request.contents").ForEach(func(index, content gjson.Result) bool {
		if content.Get("role").String() != "user" {
			return true
		}
		first, last := -1, -1
		parts := content.Get("parts").Array()
		for i, part := range parts {
			if text := part.Get("text"); text.Type == gjson.String && text.String() != "" {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first < 0 || (strings.HasPrefix(parts[first].Get("text").String(), "<USER_REQUEST>\n") && strings.HasSuffix(parts[last].Get("text").String(), "</ADDITIONAL_METADATA>")) {
			return true
		}
		key := fmt.Sprintf("%s/%d/%x", session, index.Int(), sha256.Sum256([]byte(content.Raw)))
		stamp := antigravityMessageTimes.GetOrAdd(key, func() string { return time.Now().Format(time.RFC3339) })
		prefix := fmt.Sprintf("request.contents.%d.parts.", index.Int())
		for i := first; i <= last; i++ {
			text := parts[i].Get("text")
			if text.Type != gjson.String {
				continue
			}
			value := text.String()
			if i == first {
				value = "<USER_REQUEST>\n" + value
			}
			if i == last {
				value += "\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: " + stamp + ".\n</ADDITIONAL_METADATA>"
			}
			var encoded bytes.Buffer
			encoder := json.NewEncoder(&encoded)
			encoder.SetEscapeHTML(false)
			_ = encoder.Encode(value)
			payload, _ = sjson.SetRawBytes(payload, fmt.Sprintf("%s%d.text", prefix, i), bytes.TrimSpace(encoded.Bytes()))
		}
		return true
	})
	return payload
}
