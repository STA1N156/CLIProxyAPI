package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

// hashAntigravityRoot shares the model/system/first-two-message anchor between
// credential affinity and upstream IDs. Caller scope keeps tenants isolated.
func hashAntigravityRoot(root canonicalRoot) string {
	var messages, features []string
	parts := 0
	for _, part := range root.User {
		if part.Kind == "message_boundary" {
			if len(features) > 0 {
				messages = append(messages, strings.Join(features, "|"))
			}
			features, parts = nil, 0
			continue
		}
		parts++
		if parts > 4 {
			continue
		}
		switch part.Kind {
		case "text":
			features = append(features, "text:"+util.StripAntigravityBillingHeader(part.Value))
		case "json":
			value := gjson.Parse(part.Value)
			call := value.Get("functionCall")
			if !call.IsObject() {
				call = value.Get("function_call")
			}
			if !call.IsObject() && value.Get("type").String() == "tool_use" {
				features = append(features, "fc:"+value.Get("name").String()+":"+util.AntigravityStableJSON(value.Get("input")))
				continue
			}
			if !call.IsObject() && value.Get("type").String() == "function_call" {
				features = append(features, "fc:"+value.Get("name").String()+":"+util.AntigravityStableJSON(gjson.Parse(value.Get("arguments").String())))
				continue
			}
			if call.IsObject() {
				features = append(features, "fc:"+call.Get("name").String()+":"+util.AntigravityStableJSON(call.Get("args")))
			}
		}
	}
	if len(features) > 0 {
		messages = append(messages, strings.Join(features, "|"))
	}
	system := strings.TrimSpace(util.StripAntigravityBillingHeader(strings.Join(root.Instructions, "\n")))
	if system == "" && len(messages) == 0 {
		return ""
	}
	material := root.Model + "\x1e" + system + "\x1e" + strings.Join(messages, "\x1f")
	if root.CallerScope != "" {
		material = root.CallerScope + "\x00" + material
	}
	sum := sha256.Sum256([]byte(material))
	return identityPrefix + hex.EncodeToString(sum[:])
}

func antigravityMessageValue(message map[string]any) any {
	calls, _ := message["tool_calls"].([]any)
	if len(calls) == 0 {
		return contentValue(message)
	}
	parts := []any{contentValue(message)}
	for _, raw := range calls {
		call, _ := raw.(map[string]any)
		function, _ := call["function"].(map[string]any)
		if function == nil {
			continue
		}
		var args any
		if text, ok := function["arguments"].(string); ok {
			decoder := json.NewDecoder(strings.NewReader(text))
			decoder.UseNumber()
			_ = decoder.Decode(&args)
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"name": function["name"], "args": args}})
	}
	return parts
}
