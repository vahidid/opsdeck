package extagent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// geminiBuiltins are Gemini CLI's own tools; all of them are denied.
var geminiBuiltins = []string{
	"run_shell_command", "read_file", "read_many_files", "write_file", "replace", "glob", "grep_search",
	"list_directory", "web_fetch", "google_web_search", "save_memory", "write_todos", "invoke_agent",
	"activate_skill", "get_internal_docs", "take_snapshot", "list_mcp_resources", "read_mcp_resource",
	"enter_plan_mode", "exit_plan_mode", "ask_user", "complete_task", "update_topic",
	"tracker_add_dependency", "tracker_create_task", "tracker_get_task", "tracker_list_tasks",
	"tracker_update_task", "tracker_visualize",
}

// writeGeminiSettings writes a private settings file (passed with
// GEMINI_CLI_SYSTEM_SETTINGS_PATH, which takes precedence over user and
// project settings; Gemini ignores it if a parent directory is group- or
// world-writable, so it lives in the workspace's private .opsdeck folder): only opsdeck's MCP tools are allowed (core allowlist +
// explicit excludes), the opsdeck server is the only MCP server and is
// trusted (opsdeck runs its own approval), and long approval waits are fine.
func writeGeminiSettings(req Request) (string, error) {
	core := []string{}
	for _, n := range req.ToolNames {
		core = append(core, "mcp_opsdeck_"+n)
	}
	s := map[string]any{
		"tools": map[string]any{
			"core":    core, // explicit names: a wildcard is denied by Gemini's policy engine
			"exclude": geminiBuiltins,
		},
		"mcp": map[string]any{"allowed": []string{"opsdeck"}},
		"mcpServers": map[string]any{"opsdeck": map[string]any{
			"httpUrl": req.MCPURL,
			"headers": map[string]string{"Authorization": "Bearer " + req.MCPToken},
			"trust":   true,
			"timeout": 3600000,
		}},
		"general":   map[string]any{"checkpointing": map[string]bool{"enabled": false}},
		"telemetry": map[string]bool{"enabled": false},
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	path := filepath.Join(req.WorkDir, "gemini-settings.json")
	return path, os.WriteFile(path, b, 0o600)
}
