package helps

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOrderAntigravityJSONPreservesCustomObjects(t *testing.T) {
	input := []byte(`{"userAgent":"antigravity","model":"m","request":{"sessionId":"s","generationConfig":{"thinkingConfig":{"thinkingBudget":8192,"includeThoughts":true},"maxOutputTokens":65536},"labels":{"trajectory_id":"t","cascade_id":"c"},"tools":[{"functionDeclarations":[{"parameters":{"required":["z"],"properties":{"z":{"type":"string"},"a":{"type":"number"}},"type":"object"},"description":"d","name":"tool"}]}],"contents":[{"parts":[{"functionCall":{"args":{"z":1,"a":2},"name":"tool"}}],"role":"model"}]},"requestId":"r","project":"p","custom":{"z":1,"a":2}}`)
	output := OrderAntigravityJSON(input)
	var before, after any
	if err := json.Unmarshal(input, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("ordering changed request values")
	}
	for _, want := range []string{
		`{"project":"p","requestId":"r","request":{"contents":[{"role":"model","parts":`,
		`"labels":{"cascade_id":"c","trajectory_id":"t"}`,
		`"generationConfig":{"maxOutputTokens":65536,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":8192}}`,
		`"functionCall":{"args":{"z":1,"a":2},"name":"tool"}`,
		`"parameters":{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"number"}},"required":["z"]}`,
		`"custom":{"z":1,"a":2}`,
	} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("missing %s in %s", want, output)
		}
	}
}
