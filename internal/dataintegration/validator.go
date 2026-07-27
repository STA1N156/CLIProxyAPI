package dataintegration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

const (
	criterionCount         = 6
	minimumEffectiveTurns  = 2
	storageRequirementMask = uint8(1<<0 | 1<<1)
	validatorVersion       = 3
	minValidatorVersion    = 2
)

const (
	CriterionEffectiveTurns = "effective_turns"
	CriterionFirstRole      = "first_role"
	CriterionToolCall       = "tool_call"
	CriterionToolSchema     = "tool_schema"
	CriterionToolPairing    = "tool_pairing"
	CriterionMachineRatio   = "machine_ratio"
)

// Criterion describes one selectable data requirement.
type Criterion struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Bit   uint8  `json:"-"`
}

// Criteria is the stable list shown by the management panel.
var Criteria = []Criterion{
	{Key: CriterionEffectiveTurns, Label: "每条 session 有效交互轮次 ≥ 2 轮（硬性要求）", Bit: 1 << 0},
	{Key: CriterionFirstRole, Label: "首条消息 role 不得为 assistant/tool（硬性要求）", Bit: 1 << 1},
	{Key: CriterionToolCall, Label: "每条 session 至少一次结构化工具调用", Bit: 1 << 2},
	{Key: CriterionToolSchema, Label: "所有调用工具均有完整 schema", Bit: 1 << 3},
	{Key: CriterionToolPairing, Label: "去掉尾轮待执行调用后，工具配对率为 100%", Bit: 1 << 4},
	{Key: CriterionMachineRatio, Label: "机器轮占 user 轮比例 < 25%", Bit: 1 << 5},
}

// Evaluation contains the six validation results and useful counters.
type Evaluation struct {
	ValidatorVersion int    `json:"validator_version"`
	Mask             uint8  `json:"criteria_mask"`
	TokenCount       uint64 `json:"token_count"`
	EffectiveTurns   int    `json:"effective_turns"`
	ToolCalls        int    `json:"tool_calls"`
	PairedToolCalls  int    `json:"paired_tool_calls"`
	MachineUserTurns int    `json:"machine_user_turns"`
	UserTurns        int    `json:"user_turns"`
}

type normalizedMessage struct {
	role          string
	content       string
	calls         []toolCall
	results       []toolResult
	resultCarrier bool
	harness       bool
	reasoning     bool
	machineKind   string
}

type toolCall struct {
	id         string
	name       string
	structured bool
}

type toolResult struct {
	id   string
	name string
}

type toolDefinition struct {
	name     string
	complete bool
}

// Evaluate checks a JSON request against all six selectable requirements.
func Evaluate(payload []byte) (Evaluation, error) {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return Evaluation{}, fmt.Errorf("decode session payload: %w", errDecode)
	}
	if root == nil {
		return Evaluation{}, fmt.Errorf("session payload must be a JSON object")
	}

	messages := extractMessages(root)
	definitions := extractToolDefinitions(root)

	evaluation := Evaluation{ValidatorVersion: validatorVersion}
	evaluation.TokenCount = estimateTokenCount(payload)
	evaluation.ToolCalls = countToolCalls(messages)
	evaluation.PairedToolCalls = countPairedToolCalls(messages, false)
	evaluation.EffectiveTurns = countEffectiveTurns(messages)
	evaluation.MachineUserTurns, evaluation.UserTurns = countMachineUserTurns(messages)

	if evaluation.EffectiveTurns >= minimumEffectiveTurns {
		evaluation.Mask |= bitFor(CriterionEffectiveTurns)
	}
	if len(messages) > 0 && validFirstRole(messages[0].role) {
		evaluation.Mask |= bitFor(CriterionFirstRole)
	}
	if evaluation.ToolCalls > 0 {
		evaluation.Mask |= bitFor(CriterionToolCall)
	}
	if evaluation.ToolCalls > 0 && calledToolsHaveCompleteDefinitions(messages, definitions) {
		evaluation.Mask |= bitFor(CriterionToolSchema)
	}
	if toolPairingComplete(messages) {
		evaluation.Mask |= bitFor(CriterionToolPairing)
	}
	if evaluation.UserTurns > 0 && float64(evaluation.MachineUserTurns)/float64(evaluation.UserTurns) < 0.25 {
		evaluation.Mask |= bitFor(CriterionMachineRatio)
	}

	return evaluation, nil
}

// MaskForKeys converts query values into a selectable criteria mask.
func MaskForKeys(values []string) (uint8, error) {
	var mask uint8
	for _, value := range values {
		for _, key := range strings.Split(value, ",") {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			bit := bitFor(key)
			if bit == 0 {
				return 0, fmt.Errorf("unknown criterion %q", key)
			}
			mask |= bit
		}
	}
	return mask, nil
}

// KeysForMask returns criteria keys in their stable display order.
func KeysForMask(mask uint8) []string {
	keys := make([]string, 0, criterionCount)
	for _, criterion := range Criteria {
		if mask&criterion.Bit != 0 {
			keys = append(keys, criterion.Key)
		}
	}
	return keys
}

func bitFor(key string) uint8 {
	for _, criterion := range Criteria {
		if criterion.Key == key {
			return criterion.Bit
		}
	}
	return 0
}

func compatibleValidatorVersion(version int) bool {
	return version >= minValidatorVersion && version <= validatorVersion
}

func extractMessages(root map[string]any) []normalizedMessage {
	for _, key := range []string{"messages", "conversation", "trajectory", "input", "contents"} {
		rawMessages, ok := root[key].([]any)
		if !ok {
			continue
		}
		messages := make([]normalizedMessage, 0, len(rawMessages))
		for _, raw := range rawMessages {
			if item, okItem := raw.(map[string]any); okItem {
				messages = append(messages, normalizeMessage(item))
			}
		}
		if len(messages) > 0 {
			return messages
		}
	}
	return nil
}

func normalizeMessage(raw map[string]any) normalizedMessage {
	role := lowerString(raw["role"])
	messageType := lowerString(raw["type"])
	switch role {
	case "model":
		role = "assistant"
	case "function":
		role = "tool"
	case "human":
		role = "user"
	}
	if role == "" {
		switch messageType {
		case "function_call", "tool_use":
			role = "assistant"
		case "function_call_output", "tool_result":
			role = "tool"
		}
	}

	message := normalizedMessage{
		role:        role,
		content:     extractText(raw["content"]),
		calls:       extractToolCalls(raw),
		results:     extractToolResults(raw),
		reasoning:   hasReasoning(raw),
		machineKind: extractMachineKind(raw),
	}
	if message.content == "" {
		message.content = extractText(raw["parts"])
	}
	message.resultCarrier = len(message.results) > 0 && !hasNonToolContent(raw)
	message.harness = message.machineKind == "harness" || message.machineKind == "harness_injection" || message.machineKind == "metadata"
	return message
}

func extractToolCalls(raw map[string]any) []toolCall {
	var calls []toolCall
	appendCall := func(value map[string]any) {
		id := firstString(value, "id", "call_id", "tool_call_id")
		name := firstString(value, "name", "tool_name")
		arguments, hasArguments := firstValue(value, "arguments", "args", "input", "parameters")
		if function, okFunction := value["function"].(map[string]any); okFunction {
			if name == "" {
				name = firstString(function, "name")
			}
			if !hasArguments {
				arguments, hasArguments = firstValue(function, "arguments", "args", "input", "parameters")
			}
		}
		if id != "" || name != "" {
			calls = append(calls, toolCall{
				id:         id,
				name:       name,
				structured: name != "" && validToolArguments(arguments, hasArguments),
			})
		}
	}

	if rawCalls, ok := raw["tool_calls"].([]any); ok {
		for _, rawCall := range rawCalls {
			if callMap, okCall := rawCall.(map[string]any); okCall {
				appendCall(callMap)
			}
		}
	}
	if functionCall, ok := raw["function_call"].(map[string]any); ok {
		appendCall(functionCall)
	}

	messageType := lowerString(raw["type"])
	if messageType == "function_call" || messageType == "tool_use" {
		appendCall(raw)
	} else if strings.HasSuffix(messageType, "_call") && messageType != "function_call_output" {
		arguments, hasArguments := firstValue(raw, "arguments", "args", "input", "parameters")
		calls = append(calls, toolCall{
			id:         firstString(raw, "id", "call_id"),
			name:       strings.TrimSuffix(messageType, "_call"),
			structured: validToolArguments(arguments, hasArguments),
		})
	}

	for _, field := range []string{"content", "parts"} {
		blocks, ok := raw[field].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			blockMap, okBlock := block.(map[string]any)
			if !okBlock {
				continue
			}
			blockType := lowerString(blockMap["type"])
			if blockType == "tool_use" || blockType == "function_call" {
				appendCall(blockMap)
			}
			if functionCall, okFunction := blockMap["functionCall"].(map[string]any); okFunction {
				appendCall(functionCall)
			}
		}
	}
	return calls
}

func validToolArguments(value any, exists bool) bool {
	if !exists {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		return true
	case string:
		var arguments map[string]any
		return json.Unmarshal([]byte(typed), &arguments) == nil && arguments != nil
	default:
		return false
	}
}

func extractToolResults(raw map[string]any) []toolResult {
	var results []toolResult
	appendResult := func(value map[string]any) {
		id := firstString(value, "tool_call_id", "tool_use_id", "call_id", "id")
		name := firstString(value, "name", "tool_name")
		results = append(results, toolResult{id: id, name: name})
	}

	messageType := lowerString(raw["type"])
	role := lowerString(raw["role"])
	rawResult := messageType == "function_call_output" || messageType == "tool_result" || role == "tool" || role == "function"
	if rawResult && (firstString(raw, "tool_call_id", "tool_use_id", "call_id", "id") != "" ||
		firstString(raw, "name", "tool_name") != "") {
		appendResult(raw)
	}

	for _, field := range []string{"content", "parts"} {
		blocks, ok := raw[field].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			blockMap, okBlock := block.(map[string]any)
			if !okBlock {
				continue
			}
			blockType := lowerString(blockMap["type"])
			if blockType == "tool_result" || blockType == "function_call_output" {
				appendResult(blockMap)
			}
			if functionResponse, okFunction := blockMap["functionResponse"].(map[string]any); okFunction {
				appendResult(functionResponse)
			}
		}
	}
	if rawResult && len(results) == 0 {
		appendResult(raw)
	}
	return results
}

func extractToolDefinitions(root map[string]any) map[string]toolDefinition {
	definitions := make(map[string]toolDefinition)
	for _, field := range []string{"tools", "tool_definitions", "functions"} {
		rawDefinitions, ok := root[field].([]any)
		if !ok {
			continue
		}
		for _, rawDefinition := range rawDefinitions {
			definitionMap, okDefinition := rawDefinition.(map[string]any)
			if !okDefinition {
				continue
			}
			if functionDeclarations, okDeclarations := definitionMap["functionDeclarations"].([]any); okDeclarations {
				for _, declaration := range functionDeclarations {
					if declarationMap, okDeclaration := declaration.(map[string]any); okDeclaration {
						addToolDefinition(definitions, declarationMap)
					}
				}
				continue
			}
			if function, okFunction := definitionMap["function"].(map[string]any); okFunction {
				addToolDefinition(definitions, function)
				continue
			}
			addToolDefinition(definitions, definitionMap)
		}
	}
	return definitions
}

func addToolDefinition(definitions map[string]toolDefinition, raw map[string]any) {
	name := firstString(raw, "name")
	if name == "" {
		name = lowerString(raw["type"])
	}
	if name == "" {
		return
	}
	description := firstString(raw, "description")
	schema, okSchema := firstMap(raw, "parameters", "input_schema", "parametersJsonSchema")
	definitions[name] = toolDefinition{
		name:     name,
		complete: semanticToolName(name) && description != "" && okSchema && completeParameterSchema(schema),
	}
}

func semanticToolName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "", "tool", "tool1", "helper", "action", "function":
		return false
	}
	if !strings.HasPrefix(name, "tool") || len(name) == len("tool") {
		return true
	}
	for _, r := range name[len("tool"):] {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
}

func completeParameterSchema(schema map[string]any) bool {
	schemaType := strings.ToLower(firstString(schema, "type"))
	if schemaType != "object" {
		return false
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	for _, rawProperty := range properties {
		property, okProperty := rawProperty.(map[string]any)
		if !okProperty || firstString(property, "description") == "" || !propertyHasType(property) {
			return false
		}
	}
	return true
}

func propertyHasType(property map[string]any) bool {
	if firstString(property, "type") != "" {
		return true
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "$ref", "enum"} {
		if _, exists := property[key]; exists {
			return true
		}
	}
	return false
}

func countToolCalls(messages []normalizedMessage) int {
	total := 0
	for _, message := range messages {
		for _, call := range message.calls {
			if call.structured {
				total++
			}
		}
	}
	return total
}

func calledToolsHaveCompleteDefinitions(messages []normalizedMessage, definitions map[string]toolDefinition) bool {
	for _, message := range messages {
		for _, call := range message.calls {
			definition, ok := definitions[call.name]
			if !ok || !definition.complete {
				return false
			}
		}
	}
	return true
}

func countPairedToolCalls(messages []normalizedMessage, includeFinalCalls bool) int {
	callLimit := pairingLimit(messages, includeFinalCalls)

	resultsByID := make(map[string]int)
	resultsByName := make(map[string]int)
	for _, message := range messages[:callLimit] {
		for _, result := range message.results {
			if result.id != "" {
				resultsByID[result.id]++
			} else if result.name != "" {
				resultsByName[result.name]++
			}
		}
	}

	paired := 0
	for index, message := range messages {
		if index >= callLimit {
			break
		}
		for _, call := range message.calls {
			if call.id != "" && resultsByID[call.id] > 0 {
				resultsByID[call.id]--
				paired++
				continue
			}
			if call.name != "" && resultsByName[call.name] > 0 {
				resultsByName[call.name]--
				paired++
			}
		}
	}
	return paired
}

func toolPairingComplete(messages []normalizedMessage) bool {
	limit := pairingLimit(messages, false)
	callCount := 0
	resultCount := 0
	for _, message := range messages[:limit] {
		resultCount += len(message.results)
		callCount += len(message.calls)
	}
	paired := countPairedToolCalls(messages, false)
	return callCount > 0 && callCount == paired && resultCount == paired
}

func pairingLimit(messages []normalizedMessage, includeFinal bool) int {
	if includeFinal {
		return len(messages)
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].role == "other" {
			return index
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.role == "user" && !message.resultCarrier && !message.harness {
			return index
		}
	}
	return len(messages)
}

func countEffectiveTurns(messages []normalizedMessage) int {
	turns := 0
	waitingForAssistant := false
	for _, message := range messages {
		switch message.role {
		case "user":
			if !message.resultCarrier && !message.harness {
				waitingForAssistant = true
			}
		case "assistant":
			if waitingForAssistant {
				turns++
				waitingForAssistant = false
			}
		}
	}

	for index, message := range messages {
		if len(message.calls) == 0 {
			continue
		}
		for _, call := range message.calls {
			if call.structured && toolCallHasLaterResult(call, messages[index+1:]) {
				turns++
				break
			}
		}
	}
	return turns
}

func toolCallHasLaterResult(call toolCall, messages []normalizedMessage) bool {
	for _, message := range messages {
		for _, result := range message.results {
			if call.id != "" && result.id == call.id {
				return true
			}
			if call.id == "" && call.name != "" && result.name == call.name {
				return true
			}
		}
	}
	return false
}

func countMachineUserTurns(messages []normalizedMessage) (int, int) {
	machine := 0
	users := 0
	previousUser := ""
	for index, message := range messages {
		if message.role != "user" || message.resultCarrier || message.harness {
			continue
		}
		users++
		content := strings.TrimSpace(message.content)
		kind := message.machineKind
		if kind == "" {
			kind = machineKindFromContent(content)
		}
		assistant, hasAssistant := nextAssistant(messages, index+1)
		switch {
		case kind == "heartbeat" && hasAssistant && isIdleAssistant(assistant, true):
			machine++
		case kind == "cron" && hasAssistant && len(assistant.calls) == 0 && !assistant.reasoning:
			machine++
		case kind == "no_reply" || isNoReplyUserContent(content, previousUser) ||
			hasAssistant && isIdleAssistant(assistant, false):
			machine++
		}
		previousUser = content
	}
	return machine, users
}

func extractMachineKind(raw map[string]any) string {
	for _, key := range []string{"machine_type", "type", "kind", "source", "event_type"} {
		if value := recognizedMachineKind(lowerString(raw[key])); value != "" {
			return value
		}
	}
	if value, ok := raw["no_reply"].(bool); ok && value {
		return "no_reply"
	}
	if metadata, ok := raw["metadata"].(map[string]any); ok {
		for _, key := range []string{"machine_type", "type", "kind", "source"} {
			if value := recognizedMachineKind(lowerString(metadata[key])); value != "" {
				return value
			}
		}
		if value, ok := metadata["no_reply"].(bool); ok && value {
			return "no_reply"
		}
	}
	return ""
}

func validFirstRole(role string) bool {
	switch role {
	case "system", "developer", "user":
		return true
	default:
		return false
	}
}

func recognizedMachineKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "cron", "heartbeat", "no_reply", "no-reply":
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(kind)), "-", "_")
	case "harness", "harness_injection", "harness-injection", "metadata":
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(kind)), "-", "_")
	default:
		return ""
	}
}

func machineKindFromContent(content string) string {
	value := strings.ToLower(strings.TrimSpace(content))
	value = strings.Trim(value, "<>[](){}")
	return recognizedMachineKind(value)
}

func nextAssistant(messages []normalizedMessage, start int) (normalizedMessage, bool) {
	hasPriorReasoning := false
	for _, message := range messages[start:] {
		if message.role == "assistant" {
			message.reasoning = message.reasoning || hasPriorReasoning
			return message, true
		}
		if message.role == "user" && !message.resultCarrier && !message.harness {
			break
		}
		hasPriorReasoning = hasPriorReasoning || message.reasoning
	}
	return normalizedMessage{}, false
}

func isIdleAssistant(message normalizedMessage, requireSentinel bool) bool {
	if len(message.calls) > 0 || message.reasoning {
		return false
	}
	content := strings.ToLower(strings.TrimSpace(message.content))
	if content == "" {
		return !requireSentinel
	}
	content = strings.Trim(content, "<>[](){}")
	switch content {
	case "heartbeat_ok", "heartbeat-ok", "no_reply", "no-reply", "pong":
		return true
	default:
		return false
	}
}

func isNoReplyUserContent(content, previous string) bool {
	content = strings.TrimSpace(content)
	if content == "" || onlyPunctuation(content) {
		return true
	}
	if previous != "" && strings.EqualFold(content, previous) {
		return true
	}
	normalized := strings.ToLower(strings.TrimFunc(content, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r)
	}))
	switch normalized {
	case "ok", "okay", "yes", "yep", "yeah", "sure", "continue", "done",
		"好", "好的", "行", "可以", "继续", "收到", "明白", "知道了", "确认":
		return true
	default:
		return false
	}
}

func onlyPunctuation(content string) bool {
	for _, r := range content {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) {
			return false
		}
	}
	return true
}

func hasReasoning(raw map[string]any) bool {
	switch lowerString(raw["type"]) {
	case "reasoning", "thinking", "analysis":
		return true
	}
	for _, key := range []string{"reasoning", "thinking", "analysis"} {
		if strings.TrimSpace(extractText(raw[key])) != "" {
			return true
		}
	}
	for _, field := range []string{"content", "parts"} {
		blocks, ok := raw[field].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			blockMap, okBlock := block.(map[string]any)
			if !okBlock {
				continue
			}
			switch lowerString(blockMap["type"]) {
			case "reasoning", "thinking", "analysis":
				if strings.TrimSpace(extractText(blockMap)) != "" {
					return true
				}
			}
		}
	}
	return false
}

func hasNonToolContent(raw map[string]any) bool {
	content, exists := raw["content"]
	if !exists {
		content = raw["parts"]
	}
	blocks, ok := content.([]any)
	if !ok {
		return strings.TrimSpace(extractText(content)) != ""
	}
	for _, block := range blocks {
		blockMap, okBlock := block.(map[string]any)
		if !okBlock {
			if strings.TrimSpace(extractText(block)) != "" {
				return true
			}
			continue
		}
		blockType := lowerString(blockMap["type"])
		if blockType == "tool_result" || blockType == "function_call_output" {
			continue
		}
		if _, isFunctionResponse := blockMap["functionResponse"]; isFunctionResponse {
			continue
		}
		if strings.TrimSpace(extractText(blockMap)) != "" {
			return true
		}
	}
	return false
}

func extractText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(extractText(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "input_text", "output_text"} {
			if text := extractText(typed[key]); text != "" {
				return text
			}
		}
		if _, isToolResult := typed["tool_use_id"]; isToolResult {
			return ""
		}
		if _, isFunctionResponse := typed["functionResponse"]; isFunctionResponse {
			return ""
		}
		return extractText(typed["content"])
	default:
		return ""
	}
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func lowerString(value any) string {
	text, _ := value.(string)
	return strings.ToLower(strings.TrimSpace(text))
}

func firstMap(values map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		if value, ok := values[key].(map[string]any); ok {
			return value, true
		}
	}
	return nil, false
}

func firstValue(values map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value, true
		}
	}
	return nil, false
}
