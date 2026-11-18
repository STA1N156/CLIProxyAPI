package antigravity

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeHubThinking applies agy2api's model profiles before/after the canonical
// thinking pipeline. Route models choose their effort through their model ID.
func NormalizeHubThinking(body []byte, model string) []byte {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "image") {
		return body
	}
	normalized := strings.ReplaceAll(lower, ".", "-")
	route := strings.Contains(lower, "gemini-3") || strings.Contains(lower, "gemini-4") || strings.HasSuffix(lower, "-agent") ||
		strings.Contains(normalized, "claude-opus-5-5") || strings.Contains(normalized, "claude-sonnet-5-5")
	budget := strings.Contains(lower, "think")
	const path = "request.generationConfig.thinkingConfig"
	if route || budget {
		body, _ = sjson.DeleteBytes(body, path+".thinkingLevel")
		if route {
			body, _ = sjson.DeleteBytes(body, path+".thinkingBudget")
		} else {
			body, _ = sjson.SetBytes(body, path+".thinkingBudget", 1024)
		}
		body, _ = sjson.SetBytes(body, path+".includeThoughts", true)
	}
	if strings.Contains(lower, "claude") {
		if value := gjson.GetBytes(body, path+".thinkingBudget"); value.Type == gjson.Number && value.Num > 0 && value.Num < 1024 {
			body, _ = sjson.SetBytes(body, path+".thinkingBudget", 1024)
		}
	}
	return body
}
