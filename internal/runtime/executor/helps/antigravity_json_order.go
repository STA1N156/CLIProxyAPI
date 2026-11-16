package helps

import (
	"sort"

	"github.com/tidwall/gjson"
)

// Only protobuf message fields are reordered. Tool arguments and schema properties
// remain raw JSON so caller-defined objects retain their values and ordering.
var antigravityJSONOrder = map[string][]string{
	"":                          {"project", "requestId", "request", "model", "userAgent", "requestType"},
	"request":                   {"contents", "systemInstruction", "tools", "labels", "generationConfig", "sessionId"},
	"request.contents[]":        {"role", "parts"},
	"request.systemInstruction": {"role", "parts"},
	"request.generationConfig":  {"maxOutputTokens", "thinkingConfig"},
	"request.generationConfig.thinkingConfig":           {"includeThoughts", "thinkingLevel", "thinkingBudget"},
	"request.tools[]":                                   {"functionDeclarations"},
	"request.tools[].functionDeclarations[]":            {"name", "description", "parameters"},
	"request.tools[].functionDeclarations[].parameters": {"type", "properties", "required"},
}

// OrderAntigravityJSON matches the fixed field order observed in CLI requests.
func OrderAntigravityJSON(payload []byte) []byte {
	return appendAntigravityJSON(make([]byte, 0, len(payload)), gjson.ParseBytes(payload), "")
}

func appendAntigravityJSON(out []byte, value gjson.Result, path string) []byte {
	order, known := antigravityJSONOrder[path]
	if value.IsArray() {
		if _, ok := antigravityJSONOrder[path+"[]"]; ok {
			out = append(out, '[')
			first := true
			value.ForEach(func(_, item gjson.Result) bool {
				if !first {
					out = append(out, ',')
				}
				first = false
				out = appendAntigravityJSON(out, item, path+"[]")
				return true
			})
			return append(out, ']')
		}
	}
	if !value.IsObject() || (!known && path != "request.labels") {
		return append(out, value.Raw...)
	}
	type field struct{ key, value gjson.Result }
	fields := make(map[string]field)
	keys := make([]string, 0)
	value.ForEach(func(key, item gjson.Result) bool {
		fields[key.String()] = field{key, item}
		keys = append(keys, key.String())
		return true
	})
	if path == "request.labels" {
		sort.Strings(keys)
		order = keys
	}
	out = append(out, '{')
	first := true
	write := func(key string) {
		item, ok := fields[key]
		if !ok {
			return
		}
		delete(fields, key)
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, item.key.Raw...)
		out = append(out, ':')
		child := key
		if path != "" {
			child = path + "." + key
		}
		out = appendAntigravityJSON(out, item.value, child)
	}
	for _, key := range order {
		write(key)
	}
	for _, key := range keys {
		write(key)
	}
	return append(out, '}')
}
