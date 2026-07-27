package dataintegration

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

func schemaAllowsType(schema map[string]any, expected string) bool {
	raw, exists := schema["type"]
	if !exists {
		return false
	}
	switch typed := raw.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), expected)
	case []any:
		for _, value := range typed {
			if text, ok := value.(string); ok && strings.EqualFold(strings.TrimSpace(text), expected) {
				return true
			}
		}
	}
	return false
}

func schemaAcceptsValue(schema map[string]any, value any, root map[string]any) bool {
	if reference := firstString(schema, "$ref"); reference != "" {
		resolved, ok := resolveLocalSchemaReference(root, reference)
		return ok && schemaAcceptsValue(resolved, value, root)
	}
	if options, ok := schema["allOf"].([]any); ok {
		for _, option := range options {
			optionMap, okOption := option.(map[string]any)
			if !okOption || !schemaAcceptsValue(optionMap, value, root) {
				return false
			}
		}
	}
	if options, ok := schema["anyOf"].([]any); ok {
		matched := false
		for _, option := range options {
			optionMap, okOption := option.(map[string]any)
			if okOption && schemaAcceptsValue(optionMap, value, root) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if options, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, option := range options {
			optionMap, okOption := option.(map[string]any)
			if okOption && schemaAcceptsValue(optionMap, value, root) {
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}
	if forbidden, ok := schema["not"].(map[string]any); ok && schemaAcceptsValue(forbidden, value, root) {
		return false
	}
	if constant, exists := schema["const"]; exists && !sameJSONValue(constant, value) {
		return false
	}
	if enumeration, ok := schema["enum"].([]any); ok {
		matched := false
		for _, allowed := range enumeration {
			if sameJSONValue(allowed, value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	types := schemaTypes(schema)
	if len(types) == 0 {
		return true
	}
	for _, schemaType := range types {
		if valueMatchesSchemaType(schemaType, schema, value, root) {
			return true
		}
	}
	return false
}

func schemaTypes(schema map[string]any) []string {
	switch typed := schema["type"].(type) {
	case string:
		return []string{strings.ToLower(strings.TrimSpace(typed))}
	case []any:
		types := make([]string, 0, len(typed))
		for _, value := range typed {
			if text, ok := value.(string); ok {
				types = append(types, strings.ToLower(strings.TrimSpace(text)))
			}
		}
		return types
	default:
		return nil
	}
}

func valueMatchesSchemaType(schemaType string, schema map[string]any, value any, root map[string]any) bool {
	switch schemaType {
	case "null":
		return value == nil
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		properties, _ := schema["properties"].(map[string]any)
		if required, okRequired := schema["required"].([]any); okRequired {
			for _, rawName := range required {
				name, okName := rawName.(string)
				if !okName {
					return false
				}
				if _, exists := object[name]; !exists {
					return false
				}
			}
		}
		for name, propertyValue := range object {
			rawProperty, exists := properties[name]
			if !exists {
				if additional, okAdditional := schema["additionalProperties"].(bool); okAdditional && !additional {
					return false
				}
				if additionalSchema, okAdditional := schema["additionalProperties"].(map[string]any); okAdditional &&
					!schemaAcceptsValue(additionalSchema, propertyValue, root) {
					return false
				}
				continue
			}
			property, okProperty := rawProperty.(map[string]any)
			if !okProperty || !schemaAcceptsValue(property, propertyValue, root) {
				return false
			}
		}
		return true
	case "array":
		array, ok := value.([]any)
		if !ok {
			return false
		}
		if minimum, okMinimum := integerKeyword(schema["minItems"]); okMinimum && len(array) < minimum {
			return false
		}
		if maximum, okMaximum := integerKeyword(schema["maxItems"]); okMaximum && len(array) > maximum {
			return false
		}
		if items, okItems := schema["items"].(map[string]any); okItems {
			for _, item := range array {
				if !schemaAcceptsValue(items, item, root) {
					return false
				}
			}
		}
		return true
	case "string":
		text, ok := value.(string)
		if !ok {
			return false
		}
		if minimum, okMinimum := integerKeyword(schema["minLength"]); okMinimum && len([]rune(text)) < minimum {
			return false
		}
		if maximum, okMaximum := integerKeyword(schema["maxLength"]); okMaximum && len([]rune(text)) > maximum {
			return false
		}
		if pattern := firstString(schema, "pattern"); pattern != "" {
			compiled, errCompile := regexp.Compile(pattern)
			if errCompile != nil || !compiled.MatchString(text) {
				return false
			}
		}
		return true
	case "integer":
		number, ok := numericValue(value)
		return ok && math.Trunc(number) == number
	case "number":
		_, ok := numericValue(value)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}

func resolveLocalSchemaReference(root map[string]any, reference string) (map[string]any, bool) {
	if !strings.HasPrefix(reference, "#/") {
		return nil, false
	}
	var current any = root
	for _, encoded := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		key := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[key]
		if !ok {
			return nil, false
		}
	}
	resolved, ok := current.(map[string]any)
	return resolved, ok
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func integerKeyword(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := strconv.Atoi(typed.String())
		return number, err == nil
	case float64:
		return int(typed), typed >= 0 && math.Trunc(typed) == typed
	case int:
		return typed, typed >= 0
	default:
		return 0, false
	}
}

func sameJSONValue(left, right any) bool {
	leftJSON, errLeft := json.Marshal(left)
	rightJSON, errRight := json.Marshal(right)
	return errLeft == nil && errRight == nil && string(leftJSON) == string(rightJSON)
}
