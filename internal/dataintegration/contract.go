package dataintegration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	ExportLayoutRaw      = "raw"
	ExportLayoutContract = "contract"
)

var contractMessageFields = map[string]struct{}{
	"messages":     {},
	"conversation": {},
	"trajectory":   {},
}

// ExportOptions controls the per-session files written into a ZIP.
type ExportOptions struct {
	Format       string
	Layout       string
	MessageField string
}

func normalizeExportOptions(options ExportOptions) (ExportOptions, error) {
	options.Format = strings.ToLower(strings.TrimSpace(options.Format))
	if options.Format != "json" && options.Format != "jsonl" {
		return ExportOptions{}, fmt.Errorf("format must be json or jsonl")
	}
	options.Layout = strings.ToLower(strings.TrimSpace(options.Layout))
	if options.Layout == "" {
		options.Layout = ExportLayoutRaw
	}
	if options.Layout != ExportLayoutRaw && options.Layout != ExportLayoutContract {
		return ExportOptions{}, fmt.Errorf("layout must be raw or contract")
	}
	options.MessageField = strings.ToLower(strings.TrimSpace(options.MessageField))
	if options.MessageField == "" {
		options.MessageField = "messages"
	}
	if _, exists := contractMessageFields[options.MessageField]; !exists {
		return ExportOptions{}, fmt.Errorf("message_field must be messages, conversation, or trajectory")
	}
	return options, nil
}

func toContractPayload(payload []byte, messageField string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var root map[string]any
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return nil, fmt.Errorf("decode session for contract export: %w", errDecode)
	}

	sequence, source := messageSequence(root)
	if sequence == nil {
		return nil, fmt.Errorf("session has no messages, conversation, trajectory, or contents")
	}
	var messages []any
	if source == "contents" {
		messages = convertGeminiContents(root["systemInstruction"], sequence)
	} else {
		messages = normalizeMessages(sequence)
		if content, ok := geminiContent(root["systemInstruction"]); ok && !hasSystemMessage(messages) {
			messages = append([]any{map[string]any{"role": "system", "content": content}}, messages...)
		}
	}

	for _, field := range []string{"messages", "conversation", "trajectory", "contents", "systemInstruction"} {
		delete(root, field)
	}
	root[messageField] = messages
	normalizeContractAliases(root)
	normalizeContractTools(root)

	encoded, errEncode := json.Marshal(root)
	if errEncode != nil {
		return nil, fmt.Errorf("encode contract session: %w", errEncode)
	}
	return encoded, nil
}

func messageSequence(root map[string]any) ([]any, string) {
	for _, field := range []string{"messages", "conversation", "trajectory", "contents"} {
		if sequence, ok := root[field].([]any); ok && len(sequence) > 0 {
			return sequence, field
		}
	}
	return nil, ""
}

func convertGeminiContents(systemInstruction any, contents []any) []any {
	messages := make([]any, 0, len(contents)+1)
	if content, ok := geminiContent(systemInstruction); ok {
		messages = append(messages, map[string]any{"role": "system", "content": content})
	}
	for _, item := range contents {
		content, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := normalizeRole(stringValue(content["role"]))
		parts, _ := content["parts"].([]any)
		textParts := make([]any, 0, len(parts))
		thinkingParts := make([]any, 0, len(parts))
		toolCalls := make([]any, 0)
		toolResults := make([]any, 0)

		for _, rawPart := range parts {
			part, okPart := rawPart.(map[string]any)
			if !okPart {
				textParts = append(textParts, rawPart)
				continue
			}
			if isTrue(part["thought"]) {
				if text, exists := part["text"]; exists {
					thinkingParts = append(thinkingParts, text)
				}
				continue
			}
			if functionCall, exists := part["functionCall"].(map[string]any); exists {
				toolCalls = append(toolCalls, contractToolCall(functionCall, part["thoughtSignature"]))
				continue
			}
			if functionResponse, exists := part["functionResponse"].(map[string]any); exists {
				toolResults = append(toolResults, contractToolResult(functionResponse, part["thoughtSignature"]))
				continue
			}
			textParts = append(textParts, part)
		}

		if len(textParts) > 0 || len(toolCalls) > 0 || len(thinkingParts) > 0 {
			message := map[string]any{"role": role}
			for key, value := range content {
				if key != "role" && key != "parts" {
					message[key] = value
				}
			}
			if value, exists := collapseContent(textParts); exists {
				message["content"] = value
			} else {
				message["content"] = ""
			}
			if value, exists := collapseContent(thinkingParts); exists {
				message["thinking"] = mergeValues(message["thinking"], value)
			}
			if len(toolCalls) > 0 {
				message["tool_calls"] = toolCalls
			}
			normalizeContractAliases(message)
			messages = append(messages, message)
		}
		messages = append(messages, toolResults...)
	}
	return messages
}

func geminiContent(value any) (any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	parts, _ := object["parts"].([]any)
	return collapseContent(parts)
}

func collapseContent(parts []any) (any, bool) {
	values := make([]any, 0, len(parts))
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if ok && len(part) == 1 {
			if text, exists := part["text"]; exists {
				values = append(values, text)
				continue
			}
		}
		if rawPart != nil {
			values = append(values, rawPart)
		}
	}
	if len(values) == 0 {
		return nil, false
	}
	if len(values) == 1 {
		return values[0], true
	}
	return values, true
}

func contractToolCall(functionCall map[string]any, thoughtSignature any) map[string]any {
	call := map[string]any{"type": "function"}
	function := make(map[string]any)
	if name := stringValue(functionCall["name"]); name != "" {
		function["name"] = name
	}
	if arguments, exists := functionCall["args"]; exists {
		if encoded, errEncode := json.Marshal(arguments); errEncode == nil {
			function["arguments"] = string(encoded)
		}
	}
	call["function"] = function
	if id := stringValue(functionCall["id"]); id != "" {
		call["id"] = id
	}
	for key, value := range functionCall {
		if key != "name" && key != "args" && key != "id" {
			call[key] = value
		}
	}
	if hasValue(thoughtSignature) {
		call["thoughtSignature"] = thoughtSignature
	}
	return call
}

func contractToolResult(functionResponse map[string]any, thoughtSignature any) map[string]any {
	result := map[string]any{"role": "tool"}
	if name := stringValue(functionResponse["name"]); name != "" {
		result["name"] = name
	}
	if id := stringValue(functionResponse["id"]); id != "" {
		result["tool_call_id"] = id
	}
	if response, exists := functionResponse["response"]; exists {
		if object, ok := response.(map[string]any); ok && len(object) == 1 {
			if content, existsContent := object["content"]; existsContent {
				result["content"] = content
			} else {
				result["content"] = response
			}
		} else {
			result["content"] = response
		}
	}
	if _, exists := result["content"]; !exists {
		result["content"] = ""
	}
	if hasValue(thoughtSignature) {
		result["thoughtSignature"] = thoughtSignature
	}
	return result
}

func normalizeMessages(sequence []any) []any {
	messages := make([]any, 0, len(sequence))
	for _, item := range sequence {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		message["role"] = normalizeRole(stringValue(message["role"]))
		if _, exists := message["content"]; !exists {
			if parts, okParts := message["parts"].([]any); okParts {
				if content, okContent := collapseContent(parts); okContent {
					message["content"] = content
				}
				delete(message, "parts")
			} else if message["role"] == "assistant" && hasValue(message["tool_calls"]) {
				message["content"] = ""
			}
		}
		if !hasValue(message["reasoning"]) && hasValue(message["reasoning_content"]) {
			message["reasoning"] = message["reasoning_content"]
			delete(message, "reasoning_content")
		}
		messages = append(messages, message)
	}
	return messages
}

func normalizeContractAliases(root map[string]any) {
	moveAlias(root, "model_name", "modelName")
	moveAlias(root, "task_type", "taskType")
	moveAlias(root, "reasoning", "reasoning_content")

	if metadata, ok := root["metadata"].(map[string]any); ok {
		if !hasValue(root["task_type"]) {
			if hasValue(metadata["task_type"]) {
				root["task_type"] = metadata["task_type"]
			} else if hasValue(metadata["taskType"]) {
				root["task_type"] = metadata["taskType"]
			}
		}
		if !hasValue(root["domain"]) && hasValue(metadata["domain"]) {
			root["domain"] = metadata["domain"]
		}
	}
	for _, field := range []string{"metadata", "task_type", "domain", "thinking", "reasoning"} {
		if !hasValue(root[field]) {
			delete(root, field)
		}
	}
}

func normalizeContractTools(root map[string]any) {
	tools, ok := root["tools"].([]any)
	if !ok {
		return
	}
	normalized := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, okTool := item.(map[string]any)
		if !okTool {
			normalized = append(normalized, item)
			continue
		}
		declarations, hasDeclarations := tool["functionDeclarations"].([]any)
		if !hasDeclarations {
			normalized = append(normalized, tool)
			continue
		}
		for _, declaration := range declarations {
			function, okFunction := declaration.(map[string]any)
			if !okFunction {
				continue
			}
			normalizeSchemaTypes(function["parameters"])
			normalized = append(normalized, map[string]any{
				"type":     "function",
				"function": function,
			})
		}
		remaining := make(map[string]any)
		for key, value := range tool {
			if key != "functionDeclarations" {
				remaining[key] = value
			}
		}
		if len(remaining) > 0 {
			normalized = append(normalized, remaining)
		}
	}
	root["tools"] = normalized
}

func hasSystemMessage(messages []any) bool {
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if ok && normalizeRole(stringValue(message["role"])) == "system" {
			return true
		}
	}
	return false
}

func mergeValues(existing, added any) any {
	if !hasValue(existing) {
		return added
	}
	if !hasValue(added) {
		return existing
	}
	return []any{existing, added}
}

func normalizeSchemaTypes(value any) {
	switch typed := value.(type) {
	case map[string]any:
		if schemaType, ok := typed["type"].(string); ok {
			typed["type"] = strings.ToLower(schemaType)
		}
		for _, child := range typed {
			normalizeSchemaTypes(child)
		}
	case []any:
		for _, child := range typed {
			normalizeSchemaTypes(child)
		}
	}
}

func moveAlias(root map[string]any, target, source string) {
	if !hasValue(root[target]) && hasValue(root[source]) {
		root[target] = root[source]
	}
	delete(root, source)
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "model":
		return "assistant"
	case "function":
		return "tool"
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func isTrue(value any) bool {
	enabled, _ := value.(bool)
	return enabled
}

func hasValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}
