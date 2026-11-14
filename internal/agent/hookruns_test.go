package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/workspace"
)

func waitFile(t *testing.T, path string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(path)
		var ev map[string]any
		if json.Unmarshal(b, &ev) == nil {
			return ev
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not written", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// turn_end hooks get the agent's last message (redacted) and how the turn
// ended.
func TestTurnEndHook(t *testing.T) {
	dir := t.TempDir()
	r, s := testRunner(t, ModeAsk)
	var hooks []Hook
	r.m.deps.Hooks = func(*workspace.Workspace) []Hook { return hooks }
	done := filepath.Join(dir, "done.json")
	hooks = []Hook{{Name: "post", Event: HookTurnEnd, Command: "cat > " + done, Key: "k-turn"}}
	r.m.turnEndHooks(s, r.w, "All three hosts are patched.", "")
	ev := waitFile(t, done)
	if ev["event"] != HookTurnEnd || ev["message"] != "All three hosts are patched." || ev["state"] != "done" || ev["session"] != "s1" {
		t.Fatalf("turn_end payload: %v", ev)
	}
	failed := filepath.Join(dir, "failed.json")
	hooks[0].Command = "cat > " + failed
	r.m.turnEndHooks(s, r.w, "", "model API: rate limited")
	if ev := waitFile(t, failed); ev["state"] != "error" || ev["reason"] != "model API: rate limited" {
		t.Fatalf("turn_end payload after an error: %v", ev)
	}
	// before_tool, after_tool and turn_end hooks don't fire for other events
	if hooks[0].matches(HookBeforeTool, "exec", nil) || hooks[0].matches(HookAfterTool, "exec", nil) {
		t.Fatal("a turn_end hook matches tool events")
	}
}

// A workspace hook can block or make a call ask; it can never let a call
// run unasked (TestHooks); and every run's result is kept for the editor.
func TestWorkspaceHookBlocksAndAsksAndRunsAreKept(t *testing.T) {
	r, s := testRunner(t, ModeFull,
		fakeTool("restart", RiskChange, func(ctx context.Context) (string, error) { return "restarted", nil }),
		fakeTool("status", RiskRead, func(ctx context.Context) (string, error) { return "all good", nil }),
	)
	var hooks []Hook
	r.m.deps.Hooks = func(*workspace.Workspace) []Hook { return hooks }
	ch, unsub := s.Subscribe()
	defer unsub()
	approvals := make(chan Event, 4)
	go func() {
		for ev := range ch {
			if ev["type"] == "approval_request" {
				approvals <- ev
			}
		}
	}()

	hooks = []Hook{{Name: "freeze", Event: HookBeforeTool, Risk: RiskChange, Command: `echo "freeze" >&2; exit 2`, FromWorkspace: true, Key: "k-freeze"}}
	if res := r.runCalls(context.Background(), calls("restart"), false); !res[0].IsError || !strings.Contains(res[0].Content, "blocked by hook freeze") {
		t.Fatalf("workspace hook did not block: %q", res[0].Content)
	}
	hooks = []Hook{{Name: "review", Event: HookBeforeTool, Command: `echo '{"decision":"ask","reason":"four eyes"}'`, FromWorkspace: true, Key: "k-review"}}
	go func() { ev := <-approvals; s.Resolve(ev["approvalId"].(string), "deny", "", "", false) }()
	if res := r.runCalls(context.Background(), calls("restart"), false); !res[0].IsError {
		t.Fatalf("a workspace ask hook did not make Full mode ask: %q", res[0].Content)
	}
	hooks = []Hook{{Name: "log", Event: HookAfterTool, Command: "cat >/dev/null", Key: "k-log"}}
	r.runCalls(context.Background(), calls("status"), false)
	deadline := time.Now().Add(5 * time.Second)
	var runs []*HookRun
	for time.Now().Before(deadline) {
		if runs = r.m.HookRuns([]string{"k-freeze", "k-review", "k-log", "k-never"}); runs[2] != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if runs[0] == nil || runs[0].Outcome != "deny" || runs[0].Message != "freeze" || runs[0].Tool != "restart" || runs[0].Workspace != "t" {
		t.Fatalf("freeze run: %+v", runs[0])
	}
	if runs[1] == nil || runs[1].Outcome != "ask" || runs[2] == nil || runs[2].Outcome != "ok" || runs[3] != nil {
		t.Fatalf("runs: %+v %+v %+v", runs[1], runs[2], runs[3])
	}
}

// A secret in a hook's message is masked even where the message is cut
// (the last run keeps 500 characters, the chat event 2000): the whole text
// is redacted before it is cut.
func TestHookMessagesRedactBeforeCutting(t *testing.T) {
	const secret = "Zq9vX2kLmP4tR7wY1nB8cD3fG6hJ0sA5"
	r, s := testRunner(t, ModeAsk, fakeTool("restart", RiskChange, func(ctx context.Context) (string, error) { return "ok", nil }))
	r.m.redactor = NewRedactor(func() map[string]string { return map[string]string{secret: "vault://t/db-password"} })
	dir := t.TempDir()
	var hooks []Hook
	r.m.deps.Hooks = func(*workspace.Workspace) []Hook { return hooks }
	for i, msg := range []string{
		strings.Repeat("a", 320) + secret + strings.Repeat("b", 400),   // across the 500 cut (last run)
		strings.Repeat("a", 1320) + secret + strings.Repeat("b", 1000), // across the 2000 cut (chat event)
	} {
		f := filepath.Join(dir, "msg"+string(rune('0'+i)))
		os.WriteFile(f, []byte(msg), 0o600)
		hooks = []Hook{{Name: "leaky", Event: HookBeforeTool, Command: "cat " + f + " >&2; exit 2", Key: "k-leak"}}
		r.runCalls(context.Background(), calls("restart"), false)
		run := r.m.HookRuns([]string{"k-leak"})[0]
		if run == nil || strings.Contains(run.Message, secret[:6]) {
			t.Fatalf("message %d: the last run shows part of the secret: %+v", i, run)
		}
	}
	for _, ev := range s.Events() {
		if ev["type"] != "hook" {
			continue
		}
		if m, _ := ev["message"].(string); strings.Contains(m, secret[:6]) {
			t.Fatalf("the chat event shows part of the secret: %.200s", m)
		}
	}
}
