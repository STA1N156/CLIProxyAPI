package dataintegration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	toolSchemaTableFileName = "tool-schema-registry.json"
	toolSchemaTableVersion  = 1
	toolSetCacheLimit       = 4096
)

type persistedToolSchemaTable struct {
	Version   int                               `json:"version"`
	UpdatedAt time.Time                         `json:"updated_at"`
	Tools     map[string]persistedToolSchemaSet `json:"tools"`
}

type persistedToolSchemaSet struct {
	Versions []persistedToolSchemaVersion `json:"versions"`
}

type persistedToolSchemaVersion struct {
	SchemaHash             string          `json:"schema_hash"`
	Definition             json.RawMessage `json:"definition"`
	ContractSchemaComplete bool            `json:"contract_schema_complete"`
	ObservedCount          uint64          `json:"observed_count"`
	FirstSeen              time.Time       `json:"first_seen,omitempty"`
	LastSeen               time.Time       `json:"last_seen,omitempty"`
}

type toolSchemaVersion struct {
	hash       string
	definition json.RawMessage
	schema     map[string]any
	complete   bool
	observed   uint64
	firstSeen  time.Time
	lastSeen   time.Time
}

// toolSchemaTable is independent from session shards, so clearing collected
// sessions never removes the real schemas already observed from clients.
type toolSchemaTable struct {
	mu       sync.RWMutex
	tools    map[string]map[string]*toolSchemaVersion
	dirty    bool
	cacheMu  sync.Mutex
	toolSets map[uint64]map[string]struct{}
}

func newToolSchemaTable() *toolSchemaTable {
	return &toolSchemaTable{
		tools:    make(map[string]map[string]*toolSchemaVersion),
		toolSets: make(map[uint64]map[string]struct{}),
	}
}

func (t *toolSchemaTable) load(path string) error {
	if t == nil {
		return nil
	}
	data, errRead := os.ReadFile(path)
	if errors.Is(errRead, os.ErrNotExist) {
		return nil
	}
	if errRead != nil {
		return fmt.Errorf("read tool schema table: %w", errRead)
	}
	var stored persistedToolSchemaTable
	if errDecode := json.Unmarshal(data, &stored); errDecode != nil {
		return fmt.Errorf("decode tool schema table: %w", errDecode)
	}
	if stored.Version != toolSchemaTableVersion {
		return fmt.Errorf("unsupported tool schema table version %d", stored.Version)
	}

	loaded := make(map[string]map[string]*toolSchemaVersion)
	for name, set := range stored.Tools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for _, storedVersion := range set.Versions {
			var definition map[string]any
			decoder := json.NewDecoder(bytes.NewReader(storedVersion.Definition))
			decoder.UseNumber()
			if errDecode := decoder.Decode(&definition); errDecode != nil {
				continue
			}
			definitionName := firstString(definition, "name")
			if definitionName == "" {
				definitionName = lowerString(definition["type"])
			}
			if definitionName != name {
				continue
			}
			schema, hasSchema := firstMap(definition, "parameters", "input_schema", "parametersJsonSchema")
			if !hasSchema {
				continue
			}
			encoded, hash, errEncode := encodeToolDefinition(definition)
			if errEncode != nil {
				continue
			}
			versions := loaded[name]
			if versions == nil {
				versions = make(map[string]*toolSchemaVersion)
				loaded[name] = versions
			}
			version := versions[hash]
			if version == nil {
				version = &toolSchemaVersion{
					hash:       hash,
					definition: encoded,
					schema:     schema,
					complete:   completeRawToolDefinition(definition),
					firstSeen:  storedVersion.FirstSeen,
					lastSeen:   storedVersion.LastSeen,
				}
				versions[hash] = version
			}
			version.observed += storedVersion.ObservedCount
		}
	}

	t.mu.Lock()
	t.tools = loaded
	names := make(map[string]struct{}, len(loaded))
	for name := range loaded {
		names[name] = struct{}{}
	}
	repaired := t.repairDescriptionsLocked(names)
	t.dirty = repaired > 0 || t.compactLocked() > 0
	t.mu.Unlock()
	return nil
}

func (t *toolSchemaTable) observeAndEnrich(payload []byte, observedAt time.Time) ([]byte, error) {
	return t.processPayload(payload, observedAt, true)
}

func (t *toolSchemaTable) enrich(payload []byte) ([]byte, error) {
	return t.processPayload(payload, time.Time{}, false)
}

func (t *toolSchemaTable) observeStoredDefinitions(payload []byte, observedAt time.Time) {
	if t == nil {
		return
	}
	fields, _, _ := quickPayloadParts(payload)
	definitions := decodeToolDefinitions(fields)
	if len(definitions) == 0 {
		return
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	t.mu.Lock()
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if name := toolDefinitionName(definition); name != "" &&
			t.observeLocked(definition, observedAt.UTC()) {
			names[name] = struct{}{}
		}
	}
	if t.repairDescriptionsLocked(names) > 0 {
		t.dirty = true
	}
	t.mu.Unlock()
}

func (t *toolSchemaTable) processPayload(payload []byte, observedAt time.Time, observe bool) ([]byte, error) {
	if t == nil {
		return payload, nil
	}

	toolFields, messages, gemini := quickPayloadParts(payload)
	current, definitions := t.currentToolSet(toolFields, observe)
	called := quickCalledToolNames(messages)
	if len(called) == 0 && len(definitions) == 0 {
		return payload, nil
	}

	if len(definitions) > 0 {
		if observedAt.IsZero() {
			observedAt = time.Now().UTC()
		}
		t.mu.Lock()
		observedNames := make(map[string]struct{}, len(definitions))
		for _, definition := range definitions {
			if name := toolDefinitionName(definition); name != "" &&
				t.observeLocked(definition, observedAt.UTC()) {
				observedNames[name] = struct{}{}
			}
		}
		if t.repairDescriptionsLocked(observedNames) > 0 {
			t.dirty = true
		}
		t.mu.Unlock()
	}

	arguments := calledToolArguments(payload)
	t.mu.RLock()
	repairedPayload, errRepair := t.repairPayloadDefinitionsLocked(payload, arguments)
	t.mu.RUnlock()
	if errRepair != nil {
		return nil, fmt.Errorf("encode schema-repaired session: %w", errRepair)
	}
	payload = repairedPayload

	missing := make([]string, 0, len(called))
	for name := range called {
		if _, exists := current[name]; !exists {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return payload, nil
	}

	var recovered map[string]json.RawMessage
	t.mu.RLock()
	for _, name := range missing {
		if raw := t.bestCompatibleLocked(name, arguments[name]); len(raw) > 0 {
			if recovered == nil {
				recovered = make(map[string]json.RawMessage)
			}
			recovered[name] = raw
		}
	}
	t.mu.RUnlock()
	if len(recovered) == 0 {
		return payload, nil
	}

	toolFields, _, gemini = quickPayloadParts(payload)
	enriched, errEncode := mergeRecoveredToolDefinitions(payload, toolFields, gemini, recovered)
	if errEncode != nil {
		return nil, fmt.Errorf("encode schema-enriched session: %w", errEncode)
	}
	return enriched, nil
}

type quickToolFields struct {
	values  [3]gjson.Result
	present [3]bool
}

func quickPayloadParts(payload []byte) (quickToolFields, gjson.Result, bool) {
	var tools quickToolFields
	var messageFields [5]gjson.Result
	var messagePresent [5]bool
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
		return tools, gjson.Result{}, false
	}
	root.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "tools":
			tools.values[0], tools.present[0] = value, true
		case "tool_definitions":
			tools.values[1], tools.present[1] = value, true
		case "functions":
			tools.values[2], tools.present[2] = value, true
		case "messages":
			messageFields[0], messagePresent[0] = value, true
		case "conversation":
			messageFields[1], messagePresent[1] = value, true
		case "trajectory":
			messageFields[2], messagePresent[2] = value, true
		case "input":
			messageFields[3], messagePresent[3] = value, true
		case "contents":
			messageFields[4], messagePresent[4] = value, true
		}
		return true
	})
	for index, value := range messageFields {
		if messagePresent[index] && hasObjectItem(value) {
			return tools, value, messagePresent[4] && messageFields[4].IsArray()
		}
	}
	return tools, gjson.Result{}, messagePresent[4] && messageFields[4].IsArray()
}

func hasObjectItem(value gjson.Result) bool {
	if !value.IsArray() {
		return false
	}
	found := false
	value.ForEach(func(_, item gjson.Result) bool {
		if item.IsObject() {
			found = true
			return false
		}
		return true
	})
	return found
}

func (t *toolSchemaTable) currentToolSet(fields quickToolFields, observe bool) (map[string]struct{}, []map[string]any) {
	fingerprint, hasTools := toolFieldsFingerprint(fields)
	if observe && hasTools {
		t.cacheMu.Lock()
		if names, exists := t.toolSets[fingerprint]; exists {
			t.cacheMu.Unlock()
			return names, nil
		}
		t.cacheMu.Unlock()
	}

	names := quickToolDefinitionNames(fields)
	if !observe || !hasTools {
		return names, nil
	}
	definitions := decodeToolDefinitions(fields)

	t.cacheMu.Lock()
	if cached, exists := t.toolSets[fingerprint]; exists {
		t.cacheMu.Unlock()
		return cached, nil
	}
	if len(t.toolSets) >= toolSetCacheLimit {
		clear(t.toolSets)
	}
	t.toolSets[fingerprint] = names
	t.cacheMu.Unlock()
	return names, definitions
}

func toolFieldsFingerprint(fields quickToolFields) (uint64, bool) {
	hashValue := xxhash.New()
	found := false
	for index, value := range fields.values {
		if !fields.present[index] {
			continue
		}
		found = true
		_, _ = hashValue.Write([]byte{byte(index)})
		_, _ = hashValue.WriteString(value.Raw)
	}
	if !found {
		return 0, false
	}
	return hashValue.Sum64(), true
}

func quickToolDefinitionNames(fields quickToolFields) map[string]struct{} {
	names := make(map[string]struct{})
	forEachToolDefinition(fields, func(value gjson.Result) {
		if name := quickToolName(value); name != "" {
			names[name] = struct{}{}
		}
	})
	return names
}

func decodeToolDefinitions(fields quickToolFields) []map[string]any {
	definitions := make([]map[string]any, 0)
	forEachToolDefinition(fields, func(value gjson.Result) {
		var definition map[string]any
		decoder := json.NewDecoder(strings.NewReader(value.Raw))
		decoder.UseNumber()
		if decoder.Decode(&definition) == nil && definition != nil {
			definitions = append(definitions, definition)
		}
	})
	return definitions
}

func forEachToolDefinition(fields quickToolFields, visit func(gjson.Result)) {
	for index, field := range fields.values {
		if !fields.present[index] || !field.IsArray() {
			continue
		}
		field.ForEach(func(_, item gjson.Result) bool {
			if !item.IsObject() {
				return true
			}
			if declarations := item.Get("functionDeclarations"); declarations.IsArray() {
				declarations.ForEach(func(_, declaration gjson.Result) bool {
					if declaration.IsObject() {
						visit(declaration)
					}
					return true
				})
				return true
			}
			if function := item.Get("function"); function.IsObject() {
				visit(function)
				return true
			}
			visit(item)
			return true
		})
	}
}

func quickToolName(value gjson.Result) string {
	if name := strings.TrimSpace(value.Get("name").String()); name != "" {
		return name
	}
	return strings.ToLower(strings.TrimSpace(value.Get("type").String()))
}

func quickCalledToolNames(messages gjson.Result) map[string]struct{} {
	called := make(map[string]struct{})
	if !messages.IsArray() {
		return called
	}
	addCall := func(value gjson.Result) {
		name := firstResultString(value, "name", "tool_name")
		if name == "" {
			name = firstResultString(value.Get("function"), "name")
		}
		if name != "" {
			called[name] = struct{}{}
		}
	}
	messages.ForEach(func(_, message gjson.Result) bool {
		if !message.IsObject() {
			return true
		}
		message.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
			if call.IsObject() {
				addCall(call)
			}
			return true
		})
		if functionCall := message.Get("function_call"); functionCall.IsObject() {
			addCall(functionCall)
		}
		messageType := strings.ToLower(strings.TrimSpace(message.Get("type").String()))
		switch {
		case messageType == "function_call" || messageType == "tool_use":
			addCall(message)
		case strings.HasSuffix(messageType, "_call") && messageType != "function_call_output":
			called[strings.TrimSuffix(messageType, "_call")] = struct{}{}
		}
		for _, field := range []string{"content", "parts"} {
			message.Get(field).ForEach(func(_, block gjson.Result) bool {
				if !block.IsObject() {
					return true
				}
				blockType := strings.ToLower(strings.TrimSpace(block.Get("type").String()))
				if blockType == "tool_use" || blockType == "function_call" {
					addCall(block)
				}
				if functionCall := block.Get("functionCall"); functionCall.IsObject() {
					addCall(functionCall)
				}
				return true
			})
		}
		return true
	})
	return called
}

func firstResultString(value gjson.Result, keys ...string) string {
	if !value.IsObject() {
		return ""
	}
	for _, key := range keys {
		if text := strings.TrimSpace(value.Get(key).String()); text != "" {
			return text
		}
	}
	return ""
}

func (t *toolSchemaTable) observeLocked(definition map[string]any, observedAt time.Time) bool {
	name := firstString(definition, "name")
	if name == "" {
		name = lowerString(definition["type"])
	}
	if name == "" {
		return false
	}
	recoverKnownToolSchema(definition)
	schema, hasSchema := firstMap(definition, "parameters", "input_schema", "parametersJsonSchema")
	if !hasSchema {
		return false
	}
	encoded, hash, errEncode := encodeToolDefinition(definition)
	if errEncode != nil {
		return false
	}
	versions := t.tools[name]
	if versions == nil {
		versions = make(map[string]*toolSchemaVersion)
		t.tools[name] = versions
	}
	version := versions[hash]
	added := version == nil
	if version == nil {
		version = &toolSchemaVersion{
			hash:       hash,
			definition: encoded,
			schema:     schema,
			complete:   completeRawToolDefinition(definition),
			firstSeen:  observedAt,
		}
		versions[hash] = version
		t.dirty = true
	}
	version.observed++
	version.lastSeen = observedAt
	return added
}

func (t *toolSchemaTable) bestCompatibleLocked(name string, arguments []map[string]any) json.RawMessage {
	var best *toolSchemaVersion
	for _, version := range t.tools[name] {
		if !version.complete {
			continue
		}
		compatible := true
		for _, argument := range arguments {
			if argument == nil || !schemaAcceptsValue(version.schema, argument, version.schema) {
				compatible = false
				break
			}
		}
		if !compatible {
			continue
		}
		if best == nil ||
			version.observed > best.observed ||
			version.observed == best.observed && version.hash < best.hash {
			best = version
		}
	}
	if best == nil {
		return nil
	}
	return append(json.RawMessage(nil), best.definition...)
}

func calledToolArguments(payload []byte) map[string][]map[string]any {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return nil
	}
	arguments := make(map[string][]map[string]any)
	for _, message := range extractMessages(root) {
		for _, call := range message.calls {
			if call.name != "" {
				arguments[call.name] = append(arguments[call.name], call.arguments)
			}
		}
	}
	return arguments
}

func (t *toolSchemaTable) counts() (int, int) {
	if t == nil {
		return 0, 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	versions := 0
	for _, toolVersions := range t.tools {
		versions += len(toolVersions)
	}
	return len(t.tools), versions
}

func (t *toolSchemaTable) clearAll() (int, int) {
	if t == nil {
		return 0, 0
	}
	t.mu.Lock()
	tools := len(t.tools)
	versions := 0
	for _, toolVersions := range t.tools {
		versions += len(toolVersions)
	}
	t.tools = make(map[string]map[string]*toolSchemaVersion)
	t.dirty = true
	t.mu.Unlock()

	t.cacheMu.Lock()
	clear(t.toolSets)
	t.cacheMu.Unlock()
	return tools, versions
}

func (t *toolSchemaTable) completeCounts() (int, int) {
	if t == nil {
		return 0, 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	tools := 0
	versions := 0
	for _, toolVersions := range t.tools {
		complete := false
		for _, version := range toolVersions {
			if version.complete {
				complete = true
				versions++
			}
		}
		if complete {
			tools++
		}
	}
	return tools, versions
}

func (t *toolSchemaTable) isDirty() bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.dirty
}

func (t *toolSchemaTable) write(path string) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	names := make(map[string]struct{}, len(t.tools))
	for name := range t.tools {
		names[name] = struct{}{}
	}
	if t.repairDescriptionsLocked(names) > 0 {
		t.dirty = true
	}
	if t.compactLocked() > 0 {
		t.dirty = true
	}
	if !t.dirty {
		t.mu.Unlock()
		return nil
	}
	stored := t.persistedLocked(time.Now().UTC())
	t.dirty = false
	t.mu.Unlock()

	data, errEncode := json.MarshalIndent(stored, "", "  ")
	if errEncode != nil {
		t.markDirty()
		return fmt.Errorf("encode tool schema table: %w", errEncode)
	}
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.markDirty()
		return fmt.Errorf("write tool schema table: %w", errWrite)
	}
	return nil
}

func (t *toolSchemaTable) compact() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	removed := t.compactLocked()
	if removed > 0 {
		t.dirty = true
	}
	return removed
}

func (t *toolSchemaTable) compactLocked() int {
	removed := 0
	for name, versions := range t.tools {
		bySignature := make(map[string]*toolSchemaVersion)
		for _, version := range versions {
			if version.schema == nil {
				continue
			}
			signature := toolSchemaSignature(version.schema)
			current := bySignature[signature]
			if betterToolSchemaVersion(version, current) {
				bySignature[signature] = version
			}
		}
		compacted := make(map[string]*toolSchemaVersion)
		for _, version := range bySignature {
			compacted[version.hash] = version
		}
		removed += len(versions) - len(compacted)
		if len(compacted) == 0 {
			delete(t.tools, name)
			continue
		}
		t.tools[name] = compacted
	}
	return removed
}

func toolSchemaSignature(schema map[string]any) string {
	encoded, _ := json.Marshal(schemaWithoutAnnotations(schema))
	return string(encoded)
}

func schemaWithoutAnnotations(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(typed))
		for key, child := range typed {
			switch key {
			case "description", "title", "$comment", "examples", "default", "deprecated", "readOnly", "writeOnly":
				continue
			}
			cleanedChild := schemaWithoutAnnotations(child)
			if key == "required" || key == "type" {
				if list := stringList(cleanedChild); list != nil {
					sort.Strings(list)
					cleanedChild = list
				}
			}
			cleaned[key] = cleanedChild
		}
		return cleaned
	case []any:
		cleaned := make([]any, len(typed))
		for index, child := range typed {
			cleaned[index] = schemaWithoutAnnotations(child)
		}
		return cleaned
	default:
		return typed
	}
}

func stringList(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, len(raw))
	for index, item := range raw {
		text, okText := item.(string)
		if !okText {
			return nil
		}
		result[index] = text
	}
	return result
}

func betterToolSchemaVersion(candidate, current *toolSchemaVersion) bool {
	if candidate == nil {
		return false
	}
	if current != nil && candidate.complete != current.complete {
		return candidate.complete
	}
	if current == nil || candidate.observed != current.observed {
		return current == nil || candidate.observed > current.observed
	}
	if !candidate.lastSeen.Equal(current.lastSeen) {
		return candidate.lastSeen.After(current.lastSeen)
	}
	return candidate.hash < current.hash
}

func (t *toolSchemaTable) markDirty() {
	t.mu.Lock()
	t.dirty = true
	t.mu.Unlock()
}

func encodeToolDefinition(definition map[string]any) (json.RawMessage, string, error) {
	encoded, errEncode := json.Marshal(definition)
	if errEncode != nil {
		return nil, "", errEncode
	}
	hashValue := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(hashValue[:]), nil
}

func completeRawToolDefinition(definition map[string]any) bool {
	definitions := make(map[string]toolDefinition)
	addToolDefinition(definitions, definition)
	name := firstString(definition, "name")
	if name == "" {
		name = lowerString(definition["type"])
	}
	value, exists := definitions[name]
	return exists && value.complete
}

func mergeRecoveredToolDefinitions(
	payload []byte,
	fields quickToolFields,
	gemini bool,
	recovered map[string]json.RawMessage,
) ([]byte, error) {
	names := make([]string, 0, len(recovered))
	for name := range recovered {
		names = append(names, name)
	}
	sort.Strings(names)
	definitions := make([]json.RawMessage, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, recovered[name])
	}

	tools := fields.values[0]
	if gemini {
		if tools.IsArray() {
			for index, item := range tools.Array() {
				if declarations := item.Get("functionDeclarations"); declarations.IsArray() {
					return sjson.SetRawBytes(
						payload,
						fmt.Sprintf("tools.%d.functionDeclarations", index),
						appendJSONArray(declarations.Raw, definitions),
					)
				}
			}
		}
		declarationArray := appendJSONArray("", definitions)
		wrapper := append([]byte(`{"functionDeclarations":`), declarationArray...)
		wrapper = append(wrapper, '}')
		return sjson.SetRawBytes(payload, "tools", appendJSONArray(tools.Raw, []json.RawMessage{wrapper}))
	}

	wrappers := make([]json.RawMessage, 0, len(definitions))
	for _, definition := range definitions {
		wrapper := append([]byte(`{"type":"function","function":`), definition...)
		wrappers = append(wrappers, append(wrapper, '}'))
	}
	return sjson.SetRawBytes(payload, "tools", appendJSONArray(tools.Raw, wrappers))
}

func appendJSONArray(existing string, additions []json.RawMessage) []byte {
	existing = strings.TrimSpace(existing)
	if len(existing) >= 2 && existing[0] == '[' && existing[len(existing)-1] == ']' {
		existing = strings.TrimSpace(existing[1 : len(existing)-1])
	} else {
		existing = ""
	}
	size := len(existing) + 2
	for _, addition := range additions {
		size += len(addition) + 1
	}
	result := make([]byte, 0, size)
	result = append(result, '[')
	if existing != "" {
		result = append(result, existing...)
	}
	for _, addition := range additions {
		if len(result) > 1 {
			result = append(result, ',')
		}
		result = append(result, addition...)
	}
	return append(result, ']')
}
