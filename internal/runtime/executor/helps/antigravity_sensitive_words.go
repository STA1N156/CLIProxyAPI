package helps

import (
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var antigravitySensitivePatterns = cache.NewBoundedLRU[string, *regexp.Regexp](32, nil)

// FilterAntigravitySensitiveWords removes literal, case-sensitive matches from
// instruction and message text, leaving tool arguments and signatures intact.
func FilterAntigravitySensitiveWords(payload []byte, words []string) []byte {
	key := strings.Join(words, "\x00")
	pattern := antigravitySensitivePatterns.GetOrAdd(key, func() *regexp.Regexp {
		var escaped []string
		for _, word := range words {
			if word = strings.TrimSpace(word); word != "" {
				escaped = append(escaped, regexp.QuoteMeta(word))
			}
		}
		if len(escaped) == 0 {
			return nil
		}
		compiled, _ := regexp.Compile(strings.Join(escaped, "|"))
		return compiled
	})
	if pattern == nil {
		return payload
	}
	replace := func(path string, value gjson.Result) {
		if value.Type == gjson.String {
			if text := value.String(); pattern.MatchString(text) {
				payload, _ = sjson.SetBytes(payload, path, pattern.ReplaceAllLiteralString(text, ""))
			}
		}
	}
	for _, path := range []string{"request.systemInstruction", "request.system_instruction"} {
		instruction := gjson.GetBytes(payload, path)
		replace(path, instruction)
		instruction.Get("parts").ForEach(func(index, part gjson.Result) bool {
			replace(path+".parts."+index.String()+".text", part.Get("text"))
			return true
		})
	}
	gjson.GetBytes(payload, "request.contents").ForEach(func(index, content gjson.Result) bool {
		content.Get("parts").ForEach(func(partIndex, part gjson.Result) bool {
			replace("request.contents."+index.String()+".parts."+partIndex.String()+".text", part.Get("text"))
			return true
		})
		return true
	})
	return payload
}
