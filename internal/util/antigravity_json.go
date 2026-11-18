package util

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
)

// AntigravityStableJSON matches the sorted JSON used in Hub session/tool hashes.
func AntigravityStableJSON(value gjson.Result) string {
	raw := value.Raw
	if raw == "" {
		raw = "null"
	}
	var data any
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	if decoder.Decode(&data) != nil {
		return raw
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(data) != nil {
		return raw
	}
	compact := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	spaced := make([]byte, 0, len(compact)+len(compact)/8)
	quoted, escaped := false, false
	for _, char := range compact {
		spaced = append(spaced, char)
		if quoted {
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				quoted = false
			}
		} else if char == '"' {
			quoted = true
		} else if char == ':' || char == ',' {
			spaced = append(spaced, ' ')
		}
	}
	return string(spaced)
}
