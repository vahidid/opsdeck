package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opsdeck/internal/testutil/fakellm"
)

// A hook that came with the workspace folder never runs before the user
// trusts it on this machine; once trusted it runs, and the editor gets its
// last result.
func TestUntrustedWorkspaceHookNeverRuns(t *testing.T) {
	h := newHarness(t)
	c := h.c
	marker := filepath.Join(t.TempDir(), "hook-ran")
	h.model([]fakellm.Turn{
		{Text: "Looking.", Tools: []fakellm.ToolCall{{Name: "inventory", Args: map[string]any{}}}},
		{Text: "Done."},
	})
	// as if pulled with the folder: written to workspace.yaml on disk
	yml := filepath.Join(h.wsPath, "workspace.yaml")
	b, _ := os.ReadFile(yml)
	os.WriteFile(yml, []byte(string(b)+"hooks:\n  - name: tracker\n    event: before_tool\n    command: touch "+marker+"\n"), 0o644)
	var ws map[string]any
	c.must("GET", "/api/workspaces/acme", nil, &ws)
	if u := ws["untrustedHooks"].([]any); len(u) != 1 {
		t.Fatalf("untrusted: %v", ws["untrustedHooks"])
	}
	if runs := ws["hookRuns"].([]any); len(runs) != 1 || runs[0] != nil {
		t.Fatalf("runs before any call: %v", runs)
	}

	run := func() {
		sid, st := h.session(map[string]any{"mode": "ask"})
		c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "look around"}, nil)
		st.wait(isType("tool_result"), "inventory result")
		st.wait(idle, "idle")
	}
	run()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("an untrusted workspace hook ran")
	}

	c.must("POST", "/api/workspaces/acme/hooks/trust", nil, &ws)
	run()
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the trusted hook did not run")
	}
	c.must("GET", "/api/workspaces/acme", nil, &ws)
	runs := ws["hookRuns"].([]any)
	last, _ := runs[0].(map[string]any)
	if last == nil || last["outcome"] != "ok" || last["tool"] != "inventory" || last["workspace"] != "acme" || !strings.HasPrefix(last["at"].(string), "20") {
		t.Fatalf("last run: %v", runs)
	}

	// App settings hooks have theirs too
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["agent"] = map[string]any{"hooks": []map[string]any{{"name": "app", "event": "after_tool", "command": "exit 3"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var appRuns []any
	c.must("GET", "/api/agent/hook-runs", nil, &appRuns)
	if len(appRuns) != 1 || appRuns[0] != nil {
		t.Fatalf("app runs: %v", appRuns)
	}
}
