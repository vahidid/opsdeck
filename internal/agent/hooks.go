package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"opsdeck/internal/audit"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// Hook events.
const (
	HookBeforeTool = "before_tool" // can block a call or make it ask
	HookAfterTool  = "after_tool"  // logging, notifications
	HookTurnEnd    = "turn_end"    // the agent finished (or stopped) a turn
)

// Hook is a command on this computer run around the agent's work. It gets
// the event as JSON on stdin. A before_tool hook blocks the call when it
// exits with 2 or prints {"decision":"deny","reason":"…"}, makes it ask
// with {"decision":"ask"}, and (an App settings hook only) lets an asking
// call run with {"decision":"allow"}.
type Hook struct {
	Name        string
	Event       string
	Command     string
	Tool        string // tool name pattern (* wildcard), "" = any
	Risk        string
	Environment []string
	Timeout     time.Duration
	// Workspace hooks come from workspace.yaml and run only once the user
	// trusted them on this machine; they cannot let a call run unasked.
	FromWorkspace bool
}

// HookHash identifies a workspace hook definition for trust.
func HookHash(ws string, h workspace.Hook) string {
	b, _ := json.Marshal(h)
	sum := sha256.Sum256(append([]byte("hook\x00"+ws+"\x00"), b...))
	return hex.EncodeToString(sum[:])
}

func (h Hook) matches(event, tool string, c *Call) bool {
	if h.Event != event {
		return false
	}
	if h.Tool != "" && h.Tool != "*" && !commandGlob(h.Tool, tool) {
		return false
	}
	if c != nil {
		if h.Risk != "" && h.Risk != c.Risk {
			return false
		}
		if len(h.Environment) > 0 {
			ok := false
			for _, e := range h.Environment {
				ok = ok || e == c.Environment
			}
			if !ok {
				return false
			}
		}
	}
	return true
}

func (m *Manager) hooks(w *workspace.Workspace, event, tool string, c *Call) []Hook {
	if m.deps.Hooks == nil {
		return nil
	}
	var out []Hook
	for _, h := range m.deps.Hooks(w) {
		if h.matches(event, tool, c) {
			out = append(out, h)
		}
	}
	return out
}

// hookEvent is what a hook reads on stdin.
type hookEvent struct {
	Event       string `json:"event"`
	Workspace   string `json:"workspace"`
	Session     string `json:"session"`
	Title       string `json:"title,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Model       string `json:"model,omitempty"`
	Subagent    string `json:"subagent,omitempty"`
	Tool        string `json:"tool,omitempty"`
	CallID      string `json:"callId,omitempty"`
	Input       any    `json:"input,omitempty"`
	Risk        string `json:"risk,omitempty"`
	Target      string `json:"target,omitempty"`
	Environment string `json:"environment,omitempty"`
	Decision    string `json:"decision,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Output      string `json:"output,omitempty"`
	IsError     bool   `json:"isError,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	Message     string `json:"message,omitempty"` // turn_end: the agent's last message
	State       string `json:"state,omitempty"`
}

type hookResult struct {
	exit     int
	stdout   string
	stderr   string
	err      error
	decision string // allow | ask | deny from the JSON answer
	reason   string
}

func runHook(ctx context.Context, h Hook, w *workspace.Workspace, ev hookEvent) hookResult {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	in, _ := json.Marshal(ev)
	cmd := shellCommand(ctx, h.Command)
	if w != nil {
		cmd.Dir = w.Path
	}
	cmd.Env = append(localEnv(), "OPSDECK_HOOK_EVENT="+ev.Event, "OPSDECK_WORKSPACE="+ev.Workspace, "OPSDECK_SESSION="+ev.Session, "OPSDECK_TOOL="+ev.Tool)
	cmd.Stdin = strings.NewReader(string(in) + "\n")
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr capped
	stdout.max, stderr.max = 64<<10, 16<<10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := hookResult{stdout: strings.TrimSpace(stdout.buf.String()), stderr: strings.TrimSpace(stderr.buf.String())}
	if err != nil {
		if ee, ok := err.(interface{ ExitCode() int }); ok && ee.ExitCode() >= 0 {
			res.exit = ee.ExitCode()
		} else {
			res.err = err
		}
		if ctx.Err() != nil {
			res.err = fmt.Errorf("timed out after %s", timeout)
		}
	}
	if strings.HasPrefix(res.stdout, "{") {
		var ans struct{ Decision, Reason string }
		if json.Unmarshal([]byte(res.stdout), &ans) == nil {
			res.decision, res.reason = strings.ToLower(ans.Decision), ans.Reason
		}
	}
	return res
}

func (r *runner) hookBase(event string) hookEvent {
	info := r.s.Info()
	return hookEvent{Event: event, Workspace: r.w.Name(), Session: info.ID, Title: info.Title, Mode: info.Mode, Model: info.Model, Subagent: r.agent}
}

// beforeHooks runs the before_tool hooks of a call and applies their answer.
func (m *Manager) beforeHooks(ctx context.Context, r *runner, c llm.Block, call *Call, d Decision) Decision {
	if d.Effect == "deny" {
		return d
	}
	for _, h := range m.hooks(r.w, HookBeforeTool, c.Name, call) {
		ev := r.hookBase(HookBeforeTool)
		ev.Tool, ev.CallID, ev.Risk, ev.Target, ev.Environment, ev.Decision, ev.Reason = c.Name, c.ID, call.Risk, call.Target, call.Environment, d.Effect, d.Reason
		json.Unmarshal([]byte(m.redactor.Redact(string(c.Input))), &ev.Input)
		res := runHook(ctx, h, r.w, ev)
		outcome, msg := "ok", firstNonEmpty(res.reason, res.stderr)
		switch {
		case res.err != nil:
			outcome, msg = "error", res.err.Error()
		case res.exit == 2 || res.decision == "deny" || res.decision == "block":
			outcome = "deny"
			d = Decision{"deny", "blocked by hook " + h.Name + strings.TrimSuffix(": "+firstNonEmpty(msg, "no reason given"), ": ")}
		case res.exit != 0:
			outcome, msg = "error", fmt.Sprintf("exit code %d %s", res.exit, msg)
		case res.decision == "ask" && d.Effect == "allow":
			outcome = "ask"
			d = Decision{"ask", "hook " + h.Name + strings.TrimSuffix(": "+firstNonEmpty(msg, "asks for approval"), ": ")}
		case res.decision == "allow" && d.Effect == "ask" && !h.FromWorkspace && call.Secret == nil:
			outcome = "allow"
			d = Decision{"allow", "allowed by hook " + h.Name}
		}
		r.hookEvent(c.ID, h, outcome, msg)
		if d.Effect == "deny" {
			return d
		}
	}
	return d
}

// afterHooks runs the after_tool hooks of a call without waiting for them.
func (m *Manager) afterHooks(r *runner, c llm.Block, call *Call, out string, isErr bool, dur int64) {
	hooks := m.hooks(r.w, HookAfterTool, c.Name, call)
	if len(hooks) == 0 {
		return
	}
	ev := r.hookBase(HookAfterTool)
	ev.Tool, ev.CallID, ev.Risk, ev.Target, ev.Environment = c.Name, c.ID, call.Risk, call.Target, call.Environment
	ev.Output, ev.IsError, ev.DurationMs = truncate(out, 32000), isErr, dur
	json.Unmarshal([]byte(m.redactor.Redact(string(c.Input))), &ev.Input)
	go func() {
		for _, h := range hooks {
			res := runHook(context.Background(), h, r.w, ev)
			if res.err != nil || res.exit != 0 {
				r.hookEvent(c.ID, h, "error", firstNonEmpty(errText(res.err), fmt.Sprintf("exit code %d %s", res.exit, res.stderr)))
			}
		}
	}()
}

// turnEndHooks runs the turn_end hooks without waiting for them.
func (m *Manager) turnEndHooks(s *Session, w *workspace.Workspace, lastMessage, errMsg string) {
	hooks := m.hooks(w, HookTurnEnd, "", nil)
	if len(hooks) == 0 {
		return
	}
	r := &runner{m: m, s: s, w: w}
	ev := r.hookBase(HookTurnEnd)
	ev.Message, ev.State = truncate(m.redactor.Redact(lastMessage), 16000), "done"
	if errMsg != "" {
		ev.State, ev.Reason = "error", m.redactor.Redact(errMsg)
	}
	go func() {
		for _, h := range hooks {
			res := runHook(context.Background(), h, w, ev)
			if res.err != nil || res.exit != 0 {
				r.hookEvent("", h, "error", firstNonEmpty(errText(res.err), fmt.Sprintf("exit code %d %s", res.exit, res.stderr)))
			}
		}
	}()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// hookEvent shows what a hook did on the call's card and records it.
func (r *runner) hookEvent(callID string, h Hook, outcome, msg string) {
	msg = r.m.redactor.Redact(truncate(msg, 2000))
	data := map[string]any{"hook": h.Name, "event": h.Event, "outcome": outcome}
	if callID != "" {
		data["callId"] = callID
	}
	if msg != "" {
		data["message"] = msg
	}
	r.emit("hook", data, true)
	result := "ok"
	if outcome == "error" {
		result = "error"
	}
	r.m.deps.Audit.Record(audit.Entry{Workspace: r.w.Name(), Actor: "hook:" + h.Name, Action: "hook." + h.Event, Target: callID, Result: result,
		Detail: map[string]any{"outcome": outcome, "message": msg, "session": r.s.Info().ID}})
}
