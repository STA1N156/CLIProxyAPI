package dataintegration

import (
	"encoding/json"
	"strconv"
	"testing"
)

func TestEvaluateCompleteOpenAISession(t *testing.T) {
	t.Parallel()
	payload := validOpenAISession(t)

	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.Mask != 63 {
		t.Fatalf("criteria mask = %d, want 63", evaluation.Mask)
	}
	if evaluation.TokenCount == 0 {
		t.Fatalf("token count must be positive")
	}
	if evaluation.EffectiveTurns != 3 {
		t.Fatalf("effective turns = %d, want 3", evaluation.EffectiveTurns)
	}
	if evaluation.ToolCalls != 1 || evaluation.PairedToolCalls != 1 {
		t.Fatalf("tool calls = %d, paired = %d, want 1/1", evaluation.ToolCalls, evaluation.PairedToolCalls)
	}
}

func TestEvaluateSupportsSelectableCriteria(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"messages": [
			{"role":"user","content":"build it"},
			{"role":"assistant","content":"working"},
			{"role":"user","content":"second task"},
			{"role":"assistant","content":"done"}
		]
	}`)

	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	selected, errMask := MaskForKeys([]string{CriterionEffectiveTurns + "," + CriterionFirstRole})
	if errMask != nil {
		t.Fatalf("MaskForKeys() error = %v", errMask)
	}
	if evaluation.Mask&selected != selected {
		t.Fatalf("mask %06b should satisfy selected %06b", evaluation.Mask, selected)
	}
	if evaluation.Mask&bitFor(CriterionToolCall) != 0 {
		t.Fatalf("tool call criterion unexpectedly passed")
	}
	if evaluation.Mask&bitFor(CriterionToolPairing) != 0 {
		t.Fatalf("tool pairing criterion must require at least one tool call")
	}
}

func TestEvaluateMachineRatioBoundary(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"messages": [
			{"role":"user","content":"task"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"second task"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"more"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"scheduled check","metadata":{"type":"no_reply"}},
			{"role":"assistant","content":"answer"}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.MachineUserTurns != 1 || evaluation.UserTurns != 4 {
		t.Fatalf("machine/user = %d/%d, want 1/4", evaluation.MachineUserTurns, evaluation.UserTurns)
	}
	if evaluation.Mask&bitFor(CriterionMachineRatio) != 0 {
		t.Fatalf("25%% machine ratio must not pass the strict <25%% rule")
	}
}

func TestEvaluateRecognizesTaggedMachineTurns(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"messages": [
			{"role":"user","content":"initial task"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"ping","metadata":{"type":"heartbeat"}},
			{"role":"assistant","content":"HEARTBEAT_OK"},
			{"role":"user","content":"more"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"final task"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"again"},
			{"role":"assistant","content":"answer"}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.MachineUserTurns != 1 || evaluation.UserTurns != 5 {
		t.Fatalf("machine/user = %d/%d, want 1/5", evaluation.MachineUserTurns, evaluation.UserTurns)
	}
	if evaluation.Mask&bitFor(CriterionMachineRatio) == 0 {
		t.Fatalf("20%% machine ratio should pass")
	}
}

func TestEvaluateRecognizesContractNoReplyTurns(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"messages": [
			{"role":"user","content":"real task"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":""},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"..."},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"收到！"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"same request"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"same request"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":"another task"},
			{"role":"assistant","content":"NO_REPLY"}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.MachineUserTurns != 5 || evaluation.UserTurns != 7 {
		t.Fatalf("machine/user = %d/%d, want 5/7", evaluation.MachineUserTurns, evaluation.UserTurns)
	}
}

func TestEvaluateRequiresContractHeartbeatAndCronResponses(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"messages": [
			{"role":"user","content":"ping","metadata":{"type":"heartbeat"}},
			{"role":"assistant","content":"HEARTBEAT_OK"},
			{"role":"user","content":"ping again","metadata":{"type":"heartbeat"}},
			{"role":"assistant","content":"substantive answer"},
			{"role":"user","content":"scheduled task","metadata":{"type":"cron"}},
			{"role":"assistant","content":"scheduled response"},
			{"role":"user","content":"scheduled reasoning","metadata":{"type":"cron"}},
			{"role":"assistant","reasoning":"worked it out","content":"response"},
			{"role":"user","content":"scheduled event reasoning","metadata":{"type":"cron"}},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"worked it out"}]},
			{"role":"assistant","content":"response"}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.MachineUserTurns != 2 || evaluation.UserTurns != 5 {
		t.Fatalf("machine/user = %d/%d, want 2/5", evaluation.MachineUserTurns, evaluation.UserTurns)
	}
}

func TestEvaluateExcludesToolResultsAndHarnessFromUserTurns(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"messages": [
			{"role":"user","content":"real task"},
			{"role":"assistant","content":"answer"},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result"}]},
			{"role":"user","type":"message","content":"injected context","metadata":{"type":"harness"}}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.MachineUserTurns != 0 || evaluation.UserTurns != 1 {
		t.Fatalf("machine/user = %d/%d, want 0/1", evaluation.MachineUserTurns, evaluation.UserTurns)
	}
}

func TestEvaluateRejectsInvalidFirstRoleAndOrphanToolResult(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"tools": [{
			"type": "function",
			"name": "lookup",
			"description": "Look up a value",
			"parameters": {"type":"object","properties":{}}
		}],
		"messages": [
			{"content":"missing role"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"result"},
			{"role":"tool","tool_call_id":"orphan","content":"orphan"}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.Mask&bitFor(CriterionFirstRole) != 0 {
		t.Fatalf("missing first role must fail")
	}
	if evaluation.Mask&bitFor(CriterionToolPairing) != 0 {
		t.Fatalf("orphan tool result must fail pairing")
	}
}

func TestEvaluateRequiresJSONToolArguments(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"tools": [{
			"type": "function",
			"name": "lookup_value",
			"description": "Look up a value",
			"parameters": {"type":"object","properties":{}}
		}],
		"messages": [
			{"role":"user","content":"look it up"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup_value"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"result"}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.Mask&bitFor(CriterionToolCall) != 0 {
		t.Fatalf("tool call without JSON arguments must fail")
	}
}

func TestEvaluateRejectsAmbiguousToolNames(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"tools": [{
			"type": "function",
			"name": "tool1",
			"description": "Look up a value",
			"parameters": {"type":"object","properties":{}}
		}],
		"messages": [
			{"role":"user","content":"look it up"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"tool1","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"result"}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.Mask&bitFor(CriterionToolCall) == 0 {
		t.Fatalf("structured tool call should pass")
	}
	if evaluation.Mask&bitFor(CriterionToolSchema) != 0 {
		t.Fatalf("ambiguous tool name must fail schema validation")
	}
}

func TestEvaluateRequiresArgumentsToMatchCompleteSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		tool       string
		arguments  string
		parameters string
	}{
		{
			name:       "TodoWrite wrong field",
			tool:       "TodoWrite",
			arguments:  `{"items":[]}`,
			parameters: `{"type":"object","properties":{"todos":{"type":"array","description":"Todo items","items":{"type":"string"}}},"required":["todos"]}`,
		},
		{
			name:       "Skill wrong field",
			tool:       "Skill",
			arguments:  `{"skill":"documents"}`,
			parameters: `{"type":"object","properties":{"command":{"type":"string","description":"Skill command"}},"required":["command"]}`,
		},
		{
			name:       "Write missing required field",
			tool:       "Write",
			arguments:  `{"path":"file.txt"}`,
			parameters: `{"type":"object","properties":{"path":{"type":"string","description":"File path"},"content":{"type":"string","description":"File content"}},"required":["path","content"]}`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload := []byte(`{
				"tools":[{"type":"function","function":{"name":"` + test.tool + `","description":"Run tool","parameters":` + test.parameters + `}}],
				"messages":[
					{"role":"user","content":"run"},
					{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"` + test.tool + `","arguments":` + strconv.Quote(test.arguments) + `}}]},
					{"role":"tool","tool_call_id":"call_1","content":"ok"}
				]
			}`)
			evaluation, errEvaluate := Evaluate(payload)
			if errEvaluate != nil {
				t.Fatalf("Evaluate() error = %v", errEvaluate)
			}
			if evaluation.Mask&bitFor(CriterionToolSchema) != 0 {
				t.Fatal("arguments that do not match the schema must fail")
			}
		})
	}
}

func TestEvaluateRequiresNestedSchemaDetails(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"tools":[{"type":"function","function":{
			"name":"TodoWrite",
			"description":"Update todos",
			"parameters":{
				"type":"object",
				"properties":{
					"todos":{
						"type":"array",
						"description":"Todo items",
						"items":{"type":"object","properties":{"content":{"type":"string"}}}
					}
				}
			}
		}}],
		"messages":[
			{"role":"user","content":"run"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"TodoWrite","arguments":"{\"todos\":[]}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.Mask&bitFor(CriterionToolSchema) != 0 {
		t.Fatal("nested properties without descriptions must fail completeness")
	}
}

func TestEvaluateExcludesTheWholeFinalTurnFromPairing(t *testing.T) {
	t.Parallel()
	payload := []byte(`{
		"messages": [
			{"role":"user","content":"first task"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup_value","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"result"},
			{"role":"user","content":"final task"},
			{"role":"assistant","tool_calls":[{"id":"call_2","type":"function","function":{"name":"lookup_value","arguments":"{}"}}]},
			{"role":"system","content":"trailer"}
		]
	}`)
	evaluation, errEvaluate := Evaluate(payload)
	if errEvaluate != nil {
		t.Fatalf("Evaluate() error = %v", errEvaluate)
	}
	if evaluation.Mask&bitFor(CriterionToolPairing) == 0 {
		t.Fatalf("unmatched call in the removed final turn must not affect pairing")
	}
}

func validOpenAISession(t *testing.T) []byte {
	t.Helper()
	payload := map[string]any{
		"model": "gpt-5",
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "search_code",
					"description": "Search source code",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{
								"type":        "string",
								"description": "Search query",
							},
						},
					},
				},
			},
		},
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a coding agent."},
			map[string]any{"role": "user", "content": "Find the handler."},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "search_code",
							"arguments": `{"query":"handler"}`,
						},
					},
				},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "result"},
			map[string]any{"role": "user", "content": "Open the matching file."},
			map[string]any{"role": "assistant", "content": "Opened."},
		},
	}
	data, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal payload: %v", errMarshal)
	}
	return data
}
