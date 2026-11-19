package agent

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/agywire/schema"
)

func TestReferenceBuiltinMapping(t *testing.T) {
	var tools []schema.MCPTool
	if err := json.Unmarshal([]byte(`[
	{"name":"Bash","inputSchema":{"properties":{"command":{},"description":{}}}},
	{"name":"Read","inputSchema":{"properties":{"file_path":{},"offset":{},"limit":{}}}},
	{"name":"Edit","inputSchema":{"properties":{"filePath":{},"oldString":{},"newString":{}}}},
	{"name":"AskUserQuestion","inputSchema":{"properties":{"questions":{"items":{"properties":{"options":{"items":{"type":"object"}}}}}}}}
	]`), &tools); err != nil {
		t.Fatal(err)
	}
	m := newBuiltinMapper(tools, "/tmp/account/home")
	cases := []struct{ name, args, tool, want string }{
		{"read_file", `{"Path":"/tmp/account/home/a.txt","StartLine":4,"EndLine":8}`, "Read", `{"file_path":"a.txt","offset":4,"limit":5}`},
		{"write_file", `{"Path":"a","Content":""}`, "Edit", `{"filePath":"a","oldString":"","newString":""}`},
		{"list_dir", `{"DirectoryPath":"/tmp/account/home/src"}`, "Bash", `{"command":"ls -la 'src'","description":"List directory src"}`},
		{"run_command", `{"CommandLine":"ls"}`, "Bash", `{"command":"ls","description":"Run: ls"}`},
		{"ask_question", `{"questions":[{"question":"which?","options":["a"],"is_multi_select":true}]}`, "AskUserQuestion", `{"questions":[{"question":"which?","header":"Question","multiSelect":true,"options":[{"label":"a","description":""}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, raw, ok := m.translate(FunctionCall{Name: tc.name, Args: json.RawMessage(tc.args)})
			var want any
			_ = json.Unmarshal([]byte(tc.want), &want)
			expected, _ := json.Marshal(want)
			if !ok || name != tc.tool || string(raw) != string(expected) {
				t.Fatalf("got %s %s, want %s %s", name, raw, tc.tool, expected)
			}
		})
	}
	if _, _, ok := m.translate(FunctionCall{Name: "manage_task", Args: json.RawMessage(`{}`)}); ok {
		t.Fatal("unmapped host tool must remain unavailable")
	}
}
