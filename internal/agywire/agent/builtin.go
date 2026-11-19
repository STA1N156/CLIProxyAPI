package agent

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/agywire/schema"
)

func mapBuiltin(call FunctionCall, tools []schema.MCPTool) (string, json.RawMessage, bool) {
	aliases := map[string][]string{
		"run_command":  {"bash", "shell", "run_command", "run_terminal_cmd", "run_terminal_command", "execute_command", "exec_command"},
		"read_file":    {"read", "read_file", "readfile", "view_file"},
		"write_file":   {"write", "write_file", "create_file"},
		"edit_file":    {"edit", "edit_file", "replace", "replace_in_file"},
		"search_web":   {"websearch", "web_search", "search_web"},
		"ask_question": {"askuserquestion", "ask_question", "question", "request_user_input"},
		"list_dir":     {"ls", "list_dir", "list_directory"},
		"find_by_name": {"glob", "find_by_name"},
		"grep_search":  {"grep", "grep_search", "search"},
	}
	var tool *schema.MCPTool
	for i := range tools {
		for _, name := range aliases[call.Name] {
			if strings.EqualFold(tools[i].Name, name) {
				tool = &tools[i]
				break
			}
		}
		if tool != nil {
			break
		}
	}
	if tool == nil {
		return "", nil, false
	}
	var input map[string]any
	if json.Unmarshal(call.Args, &input) != nil {
		return "", nil, false
	}
	var spec struct {
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(tool.InputSchema, &spec)
	fields := [][]string{
		{"CommandLine", "command", "cmd"}, {"Path", "file_path", "filePath", "path"},
		{"Content", "content"}, {"Target", "old_string", "oldString", "old_text"}, {"Replacement", "new_string", "newString", "new_text"},
		{"Query", "query", "pattern", "search_term"}, {"SearchPath", "path", "directory"}, {"DirectoryPath", "path", "directory"},
		{"Pattern", "pattern", "glob"}, {"SearchDirectory", "path", "directory"},
		{"StartLine", "start_line", "offset"}, {"EndLine", "end_line"}, {"Question", "question"},
		{"toolSummary", "description"},
	}
	out := map[string]any{}
	for name := range spec.Properties {
		if value, ok := input[name]; ok {
			out[name] = value
		}
	}
	for _, keys := range fields {
		var value any
		for _, key := range keys {
			if v, ok := input[key]; ok {
				value = v
				break
			}
		}
		if value == nil {
			continue
		}
		for _, key := range keys {
			if _, ok := spec.Properties[key]; ok {
				out[key] = value
				break
			}
		}
	}
	raw, err := json.Marshal(out)
	return tool.Name, raw, err == nil
}
