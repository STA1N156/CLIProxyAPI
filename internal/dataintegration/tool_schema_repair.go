package dataintegration

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// repairDescriptionsLocked fills only missing parameter descriptions with
// descriptions observed on another real version of the same tool.
func (t *toolSchemaTable) repairDescriptionsLocked(names map[string]struct{}) int {
	repaired := 0
	for name := range names {
		repaired += t.repairToolDescriptionsLocked(name)
	}
	if repaired > 0 {
		t.dirty = true
	}
	return repaired
}

func (t *toolSchemaTable) repairToolDescriptionsLocked(name string) int {
	versions := t.tools[name]
	if len(versions) < 2 {
		return 0
	}
	type candidate struct {
		version    *toolSchemaVersion
		definition map[string]any
	}
	ordered := make([]candidate, 0, len(versions))
	for _, version := range versions {
		definition := decodeToolDefinition(version.definition)
		if definition == nil {
			continue
		}
		ordered = append(ordered, candidate{version: version, definition: definition})
	}
	sort.Slice(ordered, func(left, right int) bool {
		return betterToolSchemaVersion(ordered[left].version, ordered[right].version)
	})

	changed := 0
	for _, item := range ordered {
		version := item.version
		definition := item.definition
		present := false
		for _, current := range versions {
			if current == version {
				present = true
				break
			}
		}
		if !present {
			continue
		}
		donors := make([]map[string]any, 0, len(ordered)-1)
		for _, donor := range ordered {
			if donor.version != version {
				donors = append(donors, donor.definition)
			}
		}
		if fillMissingParameterDescriptions(definition, donors) == 0 {
			continue
		}
		encoded, hash, errEncode := encodeToolDefinition(definition)
		if errEncode != nil {
			continue
		}
		delete(versions, version.hash)
		version.hash = hash
		version.definition = encoded
		version.schema, _ = firstMap(definition, "parameters", "input_schema", "parametersJsonSchema")
		version.complete = completeRawToolDefinition(definition)
		if existing := versions[hash]; existing != nil {
			mergeToolSchemaMetadata(existing, version)
		} else {
			versions[hash] = version
		}
		changed++
	}
	return changed
}

func (t *toolSchemaTable) repairPayloadDefinitionsLocked(
	payload []byte,
	arguments map[string][]map[string]any,
) ([]byte, error) {
	if len(arguments) == 0 {
		return payload, nil
	}
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil || root == nil {
		return payload, nil
	}

	changed := false
	for _, definition := range payloadToolDefinitionMaps(root) {
		name := toolDefinitionName(definition)
		callArguments, called := arguments[name]
		if !called || completeRawToolDefinition(definition) {
			continue
		}
		repaired := cloneToolDefinition(definition)
		if repaired == nil {
			continue
		}
		changedFields := recoverKnownToolSchema(repaired)
		changedFields += fillMissingParameterDescriptions(repaired, t.decodedDefinitionsLocked(name))
		if changedFields == 0 || !completeRawToolDefinition(repaired) {
			continue
		}
		schema, _ := firstMap(repaired, "parameters", "input_schema", "parametersJsonSchema")
		if !toolArgumentsMatchSchema(schema, callArguments) {
			continue
		}
		clear(definition)
		for key, value := range repaired {
			definition[key] = value
		}
		changed = true
	}
	if !changed {
		return payload, nil
	}
	return json.Marshal(root)
}

func recoverKnownToolSchema(definition map[string]any) int {
	if toolDefinitionName(definition) != "Build" ||
		!strings.Contains(firstString(definition, "description"), "call this with no arguments") {
		return 0
	}
	if _, exists := firstMap(definition, "parameters", "input_schema", "parametersJsonSchema"); exists {
		return 0
	}
	definition["parameters"] = map[string]any{
		"type":       "OBJECT",
		"properties": map[string]any{},
	}
	return 1
}

func (t *toolSchemaTable) decodedDefinitionsLocked(name string) []map[string]any {
	versions := t.tools[name]
	ordered := make([]*toolSchemaVersion, 0, len(versions))
	for _, version := range versions {
		ordered = append(ordered, version)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return betterToolSchemaVersion(ordered[left], ordered[right])
	})
	definitions := make([]map[string]any, 0, len(ordered))
	for _, version := range ordered {
		if definition := decodeToolDefinition(version.definition); definition != nil {
			definitions = append(definitions, definition)
		}
	}
	return definitions
}

func fillMissingParameterDescriptions(target map[string]any, donors []map[string]any) int {
	targetSchema, ok := firstMap(target, "parameters", "input_schema", "parametersJsonSchema")
	if !ok {
		return 0
	}
	donorSchemas := make([]map[string]any, 0, len(donors))
	for _, donor := range donors {
		if schema, exists := firstMap(donor, "parameters", "input_schema", "parametersJsonSchema"); exists {
			donorSchemas = append(donorSchemas, schema)
		}
	}
	return fillMissingSchemaDescriptions(targetSchema, donorSchemas)
}

func fillMissingSchemaDescriptions(target map[string]any, donors []map[string]any) int {
	filled := 0
	targetProperties, _ := target["properties"].(map[string]any)
	for propertyName, rawProperty := range targetProperties {
		property, ok := rawProperty.(map[string]any)
		if !ok {
			continue
		}
		donorProperties := make([]map[string]any, 0, len(donors))
		for _, donor := range donors {
			properties, _ := donor["properties"].(map[string]any)
			donorProperty, _ := properties[propertyName].(map[string]any)
			if donorProperty != nil && compatibleDescriptionNode(property, donorProperty) {
				donorProperties = append(donorProperties, donorProperty)
			}
		}
		if firstString(property, "description") == "" {
			for _, donorProperty := range donorProperties {
				if description := firstString(donorProperty, "description"); description != "" {
					property["description"] = description
					filled++
					break
				}
			}
		}
		filled += fillMissingSchemaDescriptions(property, donorProperties)
	}

	if targetItems, ok := target["items"].(map[string]any); ok {
		donorItems := make([]map[string]any, 0, len(donors))
		for _, donor := range donors {
			if items, exists := donor["items"].(map[string]any); exists &&
				compatibleDescriptionNode(targetItems, items) {
				donorItems = append(donorItems, items)
			}
		}
		filled += fillMissingSchemaDescriptions(targetItems, donorItems)
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		targetOptions, _ := target[keyword].([]any)
		for index, rawOption := range targetOptions {
			option, ok := rawOption.(map[string]any)
			if !ok {
				continue
			}
			donorOptions := make([]map[string]any, 0, len(donors))
			for _, donor := range donors {
				options, _ := donor[keyword].([]any)
				if index >= len(options) {
					continue
				}
				donorOption, _ := options[index].(map[string]any)
				if donorOption != nil && compatibleDescriptionNode(option, donorOption) {
					donorOptions = append(donorOptions, donorOption)
				}
			}
			filled += fillMissingSchemaDescriptions(option, donorOptions)
		}
	}
	return filled
}

func compatibleDescriptionNode(target, donor map[string]any) bool {
	targetTypes := schemaTypes(target)
	donorTypes := schemaTypes(donor)
	if len(targetTypes) > 0 || len(donorTypes) > 0 {
		sort.Strings(targetTypes)
		sort.Strings(donorTypes)
		return strings.Join(targetTypes, ",") == strings.Join(donorTypes, ",")
	}
	targetSignature, _ := json.Marshal(schemaWithoutAnnotations(target))
	donorSignature, _ := json.Marshal(schemaWithoutAnnotations(donor))
	return bytes.Equal(targetSignature, donorSignature)
}

func payloadToolDefinitionMaps(root map[string]any) []map[string]any {
	definitions := make([]map[string]any, 0)
	for _, field := range []string{"tools", "tool_definitions", "functions"} {
		items, _ := root[field].([]any)
		for _, rawItem := range items {
			item, _ := rawItem.(map[string]any)
			if item == nil {
				continue
			}
			if declarations, ok := item["functionDeclarations"].([]any); ok {
				for _, rawDeclaration := range declarations {
					if declaration, okDeclaration := rawDeclaration.(map[string]any); okDeclaration {
						definitions = append(definitions, declaration)
					}
				}
				continue
			}
			if function, ok := item["function"].(map[string]any); ok {
				definitions = append(definitions, function)
				continue
			}
			definitions = append(definitions, item)
		}
	}
	return definitions
}

func toolDefinitionName(definition map[string]any) string {
	if name := firstString(definition, "name"); name != "" {
		return name
	}
	return lowerString(definition["type"])
}

func decodeToolDefinition(raw json.RawMessage) map[string]any {
	var definition map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&definition) != nil {
		return nil
	}
	return definition
}

func cloneToolDefinition(definition map[string]any) map[string]any {
	encoded, errEncode := json.Marshal(definition)
	if errEncode != nil {
		return nil
	}
	return decodeToolDefinition(encoded)
}

func toolArgumentsMatchSchema(schema map[string]any, arguments []map[string]any) bool {
	if schema == nil || len(arguments) == 0 {
		return false
	}
	for _, argument := range arguments {
		if argument == nil || !schemaAcceptsValue(schema, argument, schema) {
			return false
		}
	}
	return true
}

func mergeToolSchemaMetadata(target, source *toolSchemaVersion) {
	if target == nil || source == nil {
		return
	}
	target.observed += source.observed
	if earlierNonZero(source.firstSeen, target.firstSeen) {
		target.firstSeen = source.firstSeen
	}
	if source.lastSeen.After(target.lastSeen) {
		target.lastSeen = source.lastSeen
	}
}
