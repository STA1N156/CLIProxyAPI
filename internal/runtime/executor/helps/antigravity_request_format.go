package helps

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeAntigravityThinking moves legacy thinking fields before model validation.
func NormalizeAntigravityThinking(payload []byte) []byte {
	const target = "request.generationConfig.thinkingConfig"
	for _, source := range []string{"request.thinkingConfig", "request.thinking_config", "thinkingConfig", "thinking_config"} {
		value := util.GetGJSONBytesNoCopy(payload, source)
		if !value.Exists() {
			continue
		}
		if value.IsObject() && !util.GetGJSONBytesNoCopy(payload, target).Exists() {
			payload, _ = sjson.SetRawBytes(payload, target, []byte(value.Raw))
		}
		payload, _ = sjson.DeleteBytes(payload, source)
	}
	for _, field := range [][2]string{{"thinking_budget", "thinkingBudget"}, {"thinking_level", "thinkingLevel"}, {"include_thoughts", "includeThoughts"}} {
		legacy := target + "." + field[0]
		if value := util.GetGJSONBytesNoCopy(payload, legacy); value.Exists() {
			current := target + "." + field[1]
			if !util.GetGJSONBytesNoCopy(payload, current).Exists() {
				payload, _ = sjson.SetRawBytes(payload, current, []byte(value.Raw))
			}
			payload, _ = sjson.DeleteBytes(payload, legacy)
		}
	}
	return payload
}

// NormalizeAntigravitySystemInstruction preserves instruction parts and fills the role.
func NormalizeAntigravitySystemInstruction(payload []byte) []byte {
	const canonical = "request.systemInstruction"
	const legacy = "request.system_instruction"
	if value := util.GetGJSONBytesNoCopy(payload, legacy); value.Exists() {
		if !util.GetGJSONBytesNoCopy(payload, canonical).IsObject() && value.IsObject() {
			payload, _ = sjson.SetRawBytes(payload, canonical, []byte(value.Raw))
		}
		payload, _ = sjson.DeleteBytes(payload, legacy)
	}
	if value := util.GetGJSONBytesNoCopy(payload, canonical); value.IsObject() && value.Get("role").String() == "" {
		payload, _ = sjson.SetBytes(payload, canonical+".role", "user")
	}
	return payload
}

// AntigravityRequestType classifies generation requests from their tools and history.
func AntigravityRequestType(payload []byte, model string) (string, bool) {
	if strings.Contains(strings.ToLower(model), "image") {
		return "image_gen", false
	}
	if util.GetGJSONBytesNoCopy(payload, "requestType").String() == "web_search" {
		return "web_search", true
	}
	hasTools := false
	util.GetGJSONBytesNoCopy(payload, "request.tools").ForEach(func(_, tool gjson.Result) bool {
		if tool.IsObject() {
			tool.ForEach(func(key, value gjson.Result) bool {
				switch key.String() {
				case "functionDeclarations", "function_declarations":
					hasTools = value.Get("#").Int() > 0
				default:
					hasTools = true
				}
				return !hasTools
			})
		}
		return !hasTools
	})
	if hasTools {
		return "agent", true
	}
	hasHistory := false
	util.GetGJSONBytesNoCopy(payload, "request.contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			hasHistory = part.Get("functionCall").Exists() || part.Get("function_call").Exists() ||
				part.Get("functionResponse").Exists() || part.Get("function_response").Exists()
			return !hasHistory
		})
		return !hasHistory
	})
	if hasHistory {
		return "agent", false
	}
	return "chat", false
}

// SplitAntigravityFunctionTools preserves declaration order and built-in tools.
func SplitAntigravityFunctionTools(payload []byte) []byte {
	tools := util.GetGJSONBytesNoCopy(payload, "request.tools")
	if !tools.IsArray() {
		return payload
	}
	// Empty schemas are valid no-argument functions.
	tools.ForEach(func(toolIndex, tool gjson.Result) bool {
		field := "functionDeclarations"
		if !tool.Get(field).IsArray() {
			field = "function_declarations"
		}
		tool.Get(field).ForEach(func(index, declaration gjson.Result) bool {
			if declaration.IsObject() && !declaration.Get("parameters").IsObject() {
				path := "request.tools." + toolIndex.String() + "." + field + "." + index.String() + ".parameters"
				payload, _ = sjson.SetRawBytes(payload, path, []byte(`{"type":"OBJECT","properties":{}}`))
			}
			return true
		})
		return true
	})
	tools = util.GetGJSONBytesNoCopy(payload, "request.tools")
	items := translatorcommon.NewRawArrayItems(tools.Get("#").Int())
	changed := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		field := "functionDeclarations"
		declarations := tool.Get(field)
		if declarations.Get("#").Int() == 0 && tool.Get("function_declarations").IsArray() {
			field = "function_declarations"
			declarations = tool.Get(field)
		}
		if !declarations.IsArray() {
			items = append(items, []byte(tool.Raw))
			return true
		}
		fields := tool.Map()
		if len(fields) == 1 && field == "functionDeclarations" && declarations.Get("#").Int() == 1 {
			items = append(items, []byte(tool.Raw))
			return true
		}
		changed = true
		declarations.ForEach(func(_, declaration gjson.Result) bool {
			raw := []byte(`{"functionDeclarations":[`)
			raw = append(raw, declaration.Raw...)
			items = append(items, append(raw, ']', '}'))
			return true
		})
		builtIn, _ := sjson.Delete(tool.Raw, "functionDeclarations")
		builtIn, _ = sjson.Delete(builtIn, "function_declarations")
		if len(gjson.Parse(builtIn).Map()) > 0 {
			items = append(items, []byte(builtIn))
		}
		return true
	})
	if !changed {
		return payload
	}
	result, _ := sjson.SetRawBytes(payload, "request.tools", translatorcommon.JoinRawArray(items))
	return result
}

// UppercaseAntigravitySchemaTypes changes schema keywords without touching values.
func UppercaseAntigravitySchemaTypes(raw string) string {
	var schema map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&schema) != nil || !uppercaseAntigravitySchemaNode(schema) {
		return raw
	}
	encoded, errMarshal := json.Marshal(schema)
	if errMarshal != nil {
		return raw
	}
	return string(encoded)
}

func uppercaseAntigravitySchemaNode(node any) bool {
	changed := false
	switch value := node.(type) {
	case map[string]any:
		if kind, ok := value["type"].(string); ok && kind != strings.ToUpper(kind) {
			value["type"] = strings.ToUpper(kind)
			changed = true
		}
		for _, key := range []string{"properties", "patternProperties", "$defs", "definitions"} {
			properties, _ := value[key].(map[string]any)
			for _, property := range properties {
				changed = uppercaseAntigravitySchemaNode(property) || changed
			}
		}
		for _, key := range []string{"items", "prefixItems", "additionalProperties", "allOf", "anyOf", "oneOf"} {
			changed = uppercaseAntigravitySchemaNode(value[key]) || changed
		}
	case []any:
		for _, item := range value {
			changed = uppercaseAntigravitySchemaNode(item) || changed
		}
	}
	return changed
}

// NormalizeAntigravityClaudeTools pairs missing IDs with their corresponding calls.
func NormalizeAntigravityClaudeTools(payload []byte) []byte {
	type update struct{ path, id string }
	var updates []update
	var pending map[string][]string
	util.GetGJSONBytesNoCopy(payload, "request.contents").ForEach(func(contentIndex, content gjson.Result) bool {
		content.Get("parts").ForEach(func(partIndex, part gjson.Result) bool {
			call, response := part.Get("functionCall"), part.Get("functionResponse")
			if !call.IsObject() && !response.IsObject() {
				return true
			}
			field, dataField, node := "functionCall", "args", call
			if !call.IsObject() {
				field, dataField, node = "functionResponse", "response", response
			}
			name, id := node.Get("name").String(), node.Get("id").String()
			missing := id == ""
			queue := pending[name]
			if !call.IsObject() && len(queue) > 0 {
				for index, candidate := range queue {
					if missing || id == candidate {
						id = candidate
						pending[name] = append(queue[:index], queue[index+1:]...)
						break
					}
				}
			}
			if id == "" {
				id = antigravityToolID(contentIndex.Int(), partIndex.Int(), name, node.Get(dataField))
			}
			if call.IsObject() {
				if pending == nil {
					pending = make(map[string][]string)
				}
				pending[name] = append(queue, id)
			}
			if missing {
				path := "request.contents." + contentIndex.String() + ".parts." + partIndex.String() + "." + field + ".id"
				updates = append(updates, update{path, id})
			}
			return true
		})
		return true
	})
	for _, change := range updates {
		payload, _ = sjson.SetBytes(payload, change.path, change.id)
	}
	return payload
}

func antigravityToolID(contentIndex, partIndex int64, name string, value gjson.Result) string {
	raw := util.AntigravityStableJSON(value)
	material := strconv.FormatInt(contentIndex, 10) + ":" + strconv.FormatInt(partIndex, 10) + ":" + name + ":" + raw
	digest := sha256.Sum256([]byte(material))
	return fmt.Sprintf("toolu_%x", digest[:16])
}
