package helps

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode"

	agythinking "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/antigravity"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var antigravitySafetySettings = func() []byte {
	settings := make([]map[string]string, 0, 10)
	for _, name := range []string{"HARASSMENT", "HATE_SPEECH", "SEXUALLY_EXPLICIT", "DANGEROUS_CONTENT", "CIVIC_INTEGRITY", "IMAGE_HATE", "IMAGE_DANGEROUS_CONTENT", "IMAGE_HARASSMENT", "IMAGE_SEXUALLY_EXPLICIT", "JAILBREAK"} {
		settings = append(settings, map[string]string{"category": "HARM_CATEGORY_" + name, "threshold": "OFF"})
	}
	raw, _ := json.Marshal(settings)
	return raw
}()

// NormalizeAntigravityHubRequest mirrors agy2api's final model/request normalizer.
func NormalizeAntigravityHubRequest(payload []byte, model string) ([]byte, string) {
	payload = NormalizeAntigravityThinking(payload)
	payload = NormalizeAntigravitySystemInstruction(payload)
	lower := strings.ToLower(model)
	if strings.Contains(lower, "image") {
		return normalizeAntigravityImage(payload, lower), "gemini-3.1-flash-image"
	}
	payload = agythinking.NormalizeHubThinking(payload, model)
	if strings.Contains(lower, "claude-opus-4-6") {
		model = "claude-opus-4-6-thinking"
	} else if strings.Contains(lower, "claude-sonnet-4-6-thinking") {
		model = "claude-sonnet-4-6"
	}
	claude := strings.Contains(lower, "claude")
	payload, toolHistory := normalizeAntigravityHistory(payload, claude)
	if claude && (toolHistory || strings.EqualFold(util.GetGJSONBytesNoCopy(payload, "request.toolConfig.functionCallingConfig.mode").String(), "ANY")) {
		payload = DeleteJSONField(payload, "request.generationConfig.thinkingConfig")
	}
	const maxPath = "request.generationConfig.maxOutputTokens"
	if value := util.GetGJSONBytesNoCopy(payload, maxPath); !value.Exists() || value.Type == gjson.Null || value.Int() == 64000 || value.Int() == 65536 {
		limit := 65536
		if claude {
			limit = 64000
		}
		payload, _ = sjson.SetBytes(payload, maxPath, limit)
	}
	for _, key := range []string{"presencePenalty", "frequencyPenalty", "stopSequences"} {
		payload = DeleteJSONField(payload, "request.generationConfig."+key)
	}
	payload, _ = sjson.SetRawBytes(payload, "request.safetySettings", antigravitySafetySettings)
	return payload, model
}

func normalizeAntigravityHistory(payload []byte, claude bool) ([]byte, bool) {
	contents := util.GetGJSONBytesNoCopy(payload, "request.contents")
	if !contents.IsArray() {
		return payload, false
	}
	items := translatorcommon.NewRawArrayItems(contents.Get("#").Int())
	changed, toolHistory := false, false
	contents.ForEach(func(_, content gjson.Result) bool {
		if !content.IsObject() || !content.Get("parts").IsArray() {
			items = append(items, []byte(content.Raw))
			return true
		}
		parts := translatorcommon.NewRawArrayItems(content.Get("parts.#").Int())
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if !part.IsObject() {
				changed = true
				return true
			}
			raw := []byte(part.Raw)
			if text := part.Get("text"); text.Exists() {
				value := antigravityText(text)
				if value == "" {
					raw = DeleteJSONField(raw, "text")
				} else if text.Type != gjson.String || value != text.String() {
					raw, _ = sjson.SetBytes(raw, "text", value)
				}
			}
			valid := false
			gjson.ParseBytes(raw).ForEach(func(key, value gjson.Result) bool {
				if key.String() != "thought" && value.Type != gjson.Null && value.Raw != `""` && value.Raw != "{}" && value.Raw != "[]" {
					valid = true
				}
				return !valid
			})
			if !valid {
				changed = true
				return true
			}
			call := part.Get("functionCall").Exists() || part.Get("function_call").Exists()
			toolHistory = toolHistory || call
			if !claude && (call || part.Get("thought").Bool() || part.Get("thoughtSignature").Exists() || part.Get("thought_signature").Exists()) {
				raw = DeleteJSONField(raw, "thought_signature")
				raw = SetStringIfDifferent(raw, "thoughtSignature", "skip_thought_signature_validator")
			}
			changed = changed || string(raw) != part.Raw
			parts = append(parts, raw)
			return true
		})
		if len(parts) == 0 {
			changed = true
			return true
		}
		raw, _ := sjson.SetRawBytes([]byte(content.Raw), "parts", translatorcommon.JoinRawArray(parts))
		items = append(items, raw)
		return true
	})
	for len(items) > 0 && gjson.GetBytes(items[len(items)-1], "role").String() == "model" {
		items = items[:len(items)-1]
		changed = true
	}
	if claude && !toolHistory {
		for i := len(items) - 1; i >= 0; i-- {
			if gjson.GetBytes(items[i], "role").String() != "model" {
				continue
			}
			first := gjson.GetBytes(items[i], "parts.0")
			if !first.Get("thought").Exists() && !first.Get("thoughtSignature").Exists() {
				parts := [][]byte{[]byte(`{"text":"...","thoughtSignature":"skip_thought_signature_validator"}`)}
				gjson.GetBytes(items[i], "parts").ForEach(func(_, part gjson.Result) bool { parts = append(parts, []byte(part.Raw)); return true })
				items[i], _ = sjson.SetRawBytes(items[i], "parts", translatorcommon.JoinRawArray(parts))
				changed = true
			}
			break
		}
	}
	if changed {
		payload, _ = sjson.SetRawBytes(payload, "request.contents", translatorcommon.JoinRawArray(items))
	}
	return payload, toolHistory
}

func antigravityText(value gjson.Result) string {
	if value.Type == gjson.String {
		return strings.TrimRightFunc(value.String(), unicode.IsSpace)
	}
	if value.IsArray() {
		var parts []string
		value.ForEach(func(_, item gjson.Result) bool {
			if item.IsObject() && item.Get("text").Exists() {
				item = item.Get("text")
			}
			if item.Type != gjson.Null {
				parts = append(parts, item.String())
			}
			return true
		})
		return strings.Join(parts, " ")
	}
	if value.Type == gjson.Null {
		return "None"
	}
	if value.Type == gjson.True {
		return "True"
	}
	if value.Type == gjson.False {
		return "False"
	}
	return value.String()
}

func normalizeAntigravityImage(payload []byte, model string) []byte {
	config := []byte(`{}`)
	size := util.GetGJSONBytesNoCopy(payload, "request.size").String()
	if size == "" {
		size = util.GetGJSONBytesNoCopy(payload, "size").String()
	}
	if size != "" {
		pieces := strings.FieldsFunc(size, func(r rune) bool { return r == 'x' || r == 'X' || r == '*' || r == '×' })
		if len(pieces) == 2 {
			w, _ := strconv.Atoi(strings.TrimSpace(pieces[0]))
			h, _ := strconv.Atoi(strings.TrimSpace(pieces[1]))
			if w > 0 && h > 0 {
				best, delta := "", math.Inf(1)
				for _, ratio := range [][2]int{{1, 1}, {2, 3}, {3, 2}, {3, 4}, {4, 3}, {4, 5}, {5, 4}, {9, 16}, {16, 9}, {21, 9}} {
					if diff := math.Abs(float64(w)/float64(h) - float64(ratio[0])/float64(ratio[1])); diff < delta {
						best, delta = strconv.Itoa(ratio[0])+":"+strconv.Itoa(ratio[1]), diff
					}
				}
				resolution := "1K"
				if max(w, h) > 2560 {
					resolution = "4K"
				} else if max(w, h) > 1280 {
					resolution = "2K"
				}
				config, _ = sjson.SetBytes(config, "aspectRatio", best)
				config, _ = sjson.SetBytes(config, "imageSize", resolution)
			}
		}
	} else {
		for _, suffix := range []string{"21x9", "16x9", "9x16", "4x3", "3x4", "1x1"} {
			if strings.Contains(model, "-"+suffix) {
				config, _ = sjson.SetBytes(config, "aspectRatio", strings.ReplaceAll(suffix, "x", ":"))
				break
			}
		}
		if strings.Contains(model, "-4k") {
			config, _ = sjson.SetBytes(config, "imageSize", "4K")
		} else if strings.Contains(model, "-2k") {
			config, _ = sjson.SetBytes(config, "imageSize", "2K")
		}
	}
	gen, _ := sjson.SetRawBytes([]byte(`{"candidateCount":1}`), "imageConfig", config)
	payload, _ = sjson.SetRawBytes(payload, "request.generationConfig", gen)
	for _, key := range []string{"systemInstruction", "tools", "toolConfig", "size"} {
		payload = DeleteJSONField(payload, "request."+key)
	}
	return DeleteJSONField(payload, "size")
}
