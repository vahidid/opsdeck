package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"opsdeck/internal/audit"
	"opsdeck/internal/connect"
	"opsdeck/internal/extagent"
	"opsdeck/internal/llm"
	"opsdeck/internal/mcpx"
	"opsdeck/internal/workspace"
)

const (
	maxSteps          = 100
	compactThreshold  = 150000 // estimated tokens
	toolOutputToModel = 60000  // chars
)

// ErrBusy is returned when a session is already running a turn.
var ErrBusy = errors.New("the session is already working; wait or interrupt it")

// Send adds a user message and runs the agent in the background.
func (m *Manager) Send(ws, id, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty message")
	}
	s, err := m.Get(ws, id)
	if err != nil {
		return err
	}
	w, err := m.deps.Workspaces.Get(ws)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(connect.WithActor(context.Background(), connect.Actor{Kind: "agent", Session: id}))
	s.cancel, s.runCtx = cancel, ctx
	s.mu.Unlock()
	go m.run(ctx, s, w, text)
	return nil
}

func (m *Manager) env(w *workspace.Workspace) *Env {
	return &Env{WS: w, Hub: m.deps.Hub, Docker: m.deps.Docker, Kube: m.deps.Kube, Git: m.deps.Git, Ansible: m.deps.Ansible, Vault: m.deps.Vault, MCP: m.deps.MCP,
		MemoryPath: w.MemoryPath(), UserScripts: m.deps.UserScripts, Local: m.local()}
}

func (m *Manager) local() *LocalPolicy {
	if m.deps.Local == nil {
		return nil
	}
	return m.deps.Local()
}

func (m *Manager) run(ctx context.Context, s *Session, w *workspace.Workspace, text string) {
	defer func() {
		if r := recover(); r != nil {
			s.emit("error", map[string]any{"message": fmt.Sprintf("internal error: %v", r)}, true)
		}
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
			s.cancel = nil
		}
		for id, p := range s.pending {
			close(p.ch)
			delete(s.pending, id)
		}
		s.mu.Unlock()
		s.setState("idle")
		s.save()
	}()

	text = m.redactor.Redact(text)
	s.mu.Lock()
	first := len(s.messages) == 0
	s.messages = append(s.messages, llm.Message{Role: "user", Content: []llm.Block{{Type: "text", Text: text}}})
	if first {
		title := strings.Join(strings.Fields(text), " ")
		if r := []rune(title); len(r) > 70 {
			title = string(r[:70]) + "…"
		}
		s.info.Title = title
	}
	mode, modelID := s.info.Mode, s.info.Model
	s.mu.Unlock()
	s.emit("user", map[string]any{"text": text}, true)
	s.setState("running")
	s.save()

	router := m.deps.Router()
	if p, model, ok := router.Lookup(modelID); ok && extagent.IsKind(p.Type) {
		m.runExternal(ctx, s, w, text, p, model)
		return
	}
	provider, model, err := router.Resolve(modelID)
	if err != nil {
		s.emit("error", map[string]any{"message": err.Error()}, true)
		return
	}
	env := m.env(w)
	env.SessionID, env.Model = s.info.ID, modelID
	m.deps.MCP.Ensure(ctx, w)
	tools, toolMap := m.toolset(w)

	if err := m.maybeCompact(ctx, s, provider, model); err != nil && ctx.Err() == nil {
		s.emit("error", map[string]any{"message": "compaction failed: " + err.Error()}, true)
	}

	for step := 0; step < maxSteps; step++ {
		if ctx.Err() != nil {
			return
		}
		m.deps.Vault.Touch()
		s.mu.Lock()
		mode = s.info.Mode // the user may switch mode mid-run
		history := append([]llm.Message(nil), s.messages...)
		s.mu.Unlock()

		msgID := uuid.NewString()[:8]
		req := llm.Request{Model: model, System: m.redactor.Redact(buildSystemPrompt(env, mode)), Messages: history, Tools: tools}
		resp, err := provider.Chat(ctx, req, func(delta string) {
			s.emit("assistant_delta", map[string]any{"messageId": msgID, "text": delta}, false)
		})
		if err != nil {
			if ctx.Err() == nil {
				s.emit("error", map[string]any{"message": err.Error()}, true)
			}
			return
		}

		s.mu.Lock()
		s.info.Usage.InputTokens += resp.Usage.InputTokens
		s.info.Usage.OutputTokens += resp.Usage.OutputTokens
		s.info.Usage.CacheRead += resp.Usage.CacheRead
		if len(resp.Content) > 0 {
			s.messages = append(s.messages, llm.Message{Role: "assistant", Content: resp.Content})
		}
		s.mu.Unlock()

		var textParts []string
		var calls []llm.Block
		for _, b := range resp.Content {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					textParts = append(textParts, b.Text)
				}
			case "tool_use":
				calls = append(calls, b)
			}
		}
		if len(textParts) > 0 {
			s.emit("assistant", map[string]any{"messageId": msgID, "text": strings.Join(textParts, "\n\n")}, true)
		}
		if resp.StopReason == "refusal" {
			s.emit("error", map[string]any{"message": "The model declined to continue: " + resp.Refusal}, true)
			return
		}
		if len(calls) == 0 {
			if resp.StopReason == "max_tokens" {
				s.emit("error", map[string]any{"message": "The response hit the output limit."}, true)
			}
			s.save()
			return
		}

		results := make([]llm.Block, 0, len(calls))
		for _, c := range calls {
			if ctx.Err() != nil {
				results = append(results, llm.Block{Type: "tool_result", ToolUseID: c.ID, Content: "Interrupted by the user before running.", IsError: true})
				continue
			}
			results = append(results, m.runTool(ctx, s, env, w, toolMap, c, resp.StopReason == "max_tokens"))
		}
		s.mu.Lock()
		s.messages = append(s.messages, llm.Message{Role: "user", Content: results})
		s.mu.Unlock()
		s.save()
	}
	s.emit("error", map[string]any{"message": fmt.Sprintf("Stopped after %d steps; send a message to continue.", maxSteps)}, true)
}

// toolset returns built-in tools plus the workspace's MCP tools.
func (m *Manager) toolset(w *workspace.Workspace) ([]llm.Tool, map[string]Tool) {
	all := Tools()
	if m.local() != nil {
		all = append(all, LocalTools()...)
	}
	for _, mt := range m.deps.MCP.Tools(w) {
		mt := mt
		all = append(all, Tool{
			Def: llm.Tool{Name: mt.FullName, Description: "[MCP " + mt.Server + "] " + mt.Description, Schema: mt.Schema},
			Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
				risk := RiskChange
				if mt.ReadOnly {
					risk = RiskRead
				} else if mt.Destructive {
					risk = RiskDestructive
				}
				return &Call{Risk: risk, Target: "mcp/" + mt.Server, Match: mt.Name, Preview: mt.Server + " → " + mt.Name + "\n" + string(input),
					Run: func(ctx context.Context) (string, error) {
						out, isErr, err := env.MCP.Call(ctx, env.WS, mt.Server, mt.Name, input)
						if err == nil && isErr {
							err = errors.New(strings.TrimSpace(out))
						}
						return out, err
					}}, nil
			},
		})
	}
	defs := make([]llm.Tool, 0, len(all))
	byName := map[string]Tool{}
	for _, t := range all {
		defs = append(defs, t.Def)
		byName[t.Def.Name] = t
	}
	return defs, byName
}

func (m *Manager) runTool(ctx context.Context, s *Session, env *Env, w *workspace.Workspace, toolMap map[string]Tool, c llm.Block, truncatedTurn bool) llm.Block {
	result := func(out string, isErr bool) llm.Block {
		return llm.Block{Type: "tool_result", ToolUseID: c.ID, Content: truncate(m.redactor.Redact(out), toolOutputToModel), IsError: isErr}
	}
	var inputView any
	json.Unmarshal([]byte(m.redactor.Redact(string(c.Input))), &inputView)
	base := map[string]any{"callId": c.ID, "tool": c.Name, "input": inputView}

	t, ok := toolMap[c.Name]
	if !ok {
		s.emit("tool_call", merge(base, map[string]any{"risk": RiskRead}), true)
		s.emit("tool_result", map[string]any{"callId": c.ID, "output": "unknown tool", "isError": true, "durationMs": 0}, true)
		return result("Unknown tool "+c.Name, true)
	}
	if truncatedTurn {
		s.emit("tool_call", merge(base, map[string]any{"risk": RiskRead}), true)
		msg := "The response hit the output limit before this tool call was complete; it was not run. Retry with smaller input (e.g. edit_file instead of write_file)."
		s.emit("tool_result", map[string]any{"callId": c.ID, "output": msg, "isError": true, "durationMs": 0}, true)
		return result(msg, true)
	}
	call, err := t.Prepare(ctx, env, c.Input)
	if err != nil {
		s.emit("tool_call", merge(base, map[string]any{"risk": RiskRead}), true)
		s.emit("tool_result", map[string]any{"callId": c.ID, "output": m.redactor.Redact(err.Error()), "isError": true, "durationMs": 0}, true)
		return result(err.Error(), true)
	}
	s.emit("tool_call", merge(base, map[string]any{"risk": call.Risk, "target": call.Target, "environment": call.Environment, "reasons": call.Reasons}), true)

	s.mu.Lock()
	mode := s.info.Mode
	granted := map[string]bool{}
	for k := range s.granted {
		granted[k] = true
	}
	s.mu.Unlock()
	d := Decide(mode, w, c.Name, call, granted, m.deps.Vault)
	if call.Secret != nil && d.Effect == "allow" {
		// the tool needs a password only the user can give
		d = Decision{"ask", firstNonEmpty(call.Secret.Reason, "needs a password from you")}
	}
	actor := "agent:" + s.info.ID
	auditBase := audit.Entry{Workspace: w.Name(), Actor: actor, Action: "tool." + c.Name, Target: call.Target,
		Detail: map[string]any{"risk": call.Risk, "match": m.redactor.Redact(call.Match), "session": s.info.ID}}

	approvedBy := "policy (" + d.Reason + ")"
	switch d.Effect {
	case "deny":
		e := auditBase
		e.Result = "denied"
		e.Detail["reason"] = d.Reason
		m.deps.Audit.Record(e)
		msg := "Denied: " + d.Reason
		s.emit("tool_result", map[string]any{"callId": c.ID, "output": msg, "isError": true, "durationMs": 0}, true)
		return result(msg, true)
	case "ask":
		approvalID := uuid.NewString()[:8]
		ch := make(chan approval, 1)
		pa := &pendingApproval{ch: ch}
		req := map[string]any{"approvalId": approvalID, "callId": c.ID, "tool": c.Name, "input": inputView,
			"risk": call.Risk, "target": call.Target, "environment": call.Environment, "reason": d.Reason,
			"reasons": call.Reasons, "preview": m.redactor.Redact(call.Preview), "grantLabel": call.GrantLabel}
		if call.Secret != nil {
			pa.check = call.Secret.Check
			if pa.check == nil {
				pa.check = func(string) error { return nil }
			}
			req["secret"] = map[string]any{"label": call.Secret.Label, "rememberLabel": call.Secret.RememberLabel}
		}
		s.mu.Lock()
		s.pending[approvalID] = pa
		s.mu.Unlock()
		s.setState("waiting_approval")
		s.emit("approval_request", req, true)
		s.save()
		var a approval
		var answered bool
		select {
		case a, answered = <-ch:
		case <-ctx.Done():
		}
		s.mu.Lock()
		delete(s.pending, approvalID)
		s.mu.Unlock()
		if !answered {
			a = approval{decision: "deny", note: "interrupted"}
		}
		s.emit("approval_resolved", map[string]any{"approvalId": approvalID, "decision": a.decision, "note": a.note}, true)
		if ctx.Err() == nil {
			s.setState("running")
		}
		if a.decision == "deny" {
			e := auditBase
			e.Result = "denied"
			e.Detail["reason"] = "user denied"
			if a.note != "" {
				e.Detail["note"] = a.note
			}
			m.deps.Audit.Record(e)
			msg := "The user denied this action."
			if a.note != "" {
				msg += " Their note: " + a.note
			}
			s.emit("tool_result", map[string]any{"callId": c.ID, "output": msg, "isError": true, "durationMs": 0}, true)
			return result(msg, true)
		}
		if a.decision == "always" {
			s.mu.Lock()
			s.granted[grantKey(c.Name, call)] = true
			s.mu.Unlock()
		}
		if call.Secret != nil && call.Secret.Use != nil && a.secret != "" {
			call.Secret.Use(a.secret, a.decision == "always")
			e := auditBase
			e.Action, e.Result = "secret.entered", "ok"
			e.Detail = map[string]any{"for": c.Name, "label": call.Secret.Label, "remembered": a.decision == "always", "session": s.info.ID}
			m.deps.Audit.Record(e)
		}
		approvedBy = "user"
	}

	start := time.Now()
	out, err := call.Run(ctx)
	dur := time.Since(start).Milliseconds()
	isErr := err != nil
	if err != nil {
		if out != "" {
			out += "\n"
		}
		out += "error: " + err.Error()
	}
	out = m.redactor.Redact(out)
	e := auditBase
	e.Detail["approvedBy"] = approvedBy
	e.Detail["durationMs"] = dur
	e.Result = "ok"
	if isErr {
		e.Result = "error"
		e.Detail["error"] = m.redactor.Redact(err.Error())
	}
	if strings.HasPrefix(out, "exit code: ") {
		var code int
		fmt.Sscanf(out, "exit code: %d", &code)
		e.Detail["exitCode"] = code
	}
	m.deps.Audit.Record(e)
	s.emit("tool_result", map[string]any{"callId": c.ID, "output": truncate(out, 200000), "isError": isErr, "durationMs": dur}, true)
	return result(out, isErr)
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func estimateTokens(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			n += len(b.Text) + len(b.Content) + len(b.Input) + len(b.Signature)/4
		}
	}
	return n / 4
}

// maybeCompact summarizes older history at the start of a turn when the
// conversation grows too large. It only runs between user turns, so no tool
// loop is in flight, and it drops thinking blocks from the history it keeps.
func (m *Manager) maybeCompact(ctx context.Context, s *Session, p llm.Provider, model string) error {
	s.mu.Lock()
	msgs := append([]llm.Message(nil), s.messages...)
	s.mu.Unlock()
	if estimateTokens(msgs) < compactThreshold || len(msgs) < 6 {
		return nil
	}
	// Keep the last few turns, starting at a user message that carries text.
	cut := -1
	for i := len(msgs) - 1; i > 0; i-- {
		if msgs[i].Role == "user" && len(msgs[i].Content) > 0 && msgs[i].Content[0].Type == "text" && len(msgs)-i >= 4 {
			cut = i
			break
		}
	}
	if cut <= 0 {
		cut = len(msgs) - 1 // keep only the new user message
	}
	old := append([]llm.Message(nil), msgs[:cut]...)
	old = append(old, llm.Message{Role: "user", Content: []llm.Block{{Type: "text", Text: "Summarize the conversation so far so the work can continue in a fresh context."}}})
	resp, err := p.Chat(ctx, llm.Request{Model: model, MaxTokens: 8000,
		System:   "You summarize an infrastructure operations session. Include: the user's goals; facts discovered about the systems (hosts, versions, paths, configs); every change made (exact commands/files and results); current state; open questions and next steps. Be complete but compact. Never include secrets.",
		Messages: old}, nil)
	if err != nil {
		return err
	}
	var summary strings.Builder
	for _, b := range resp.Content {
		if b.Type == "text" {
			summary.WriteString(b.Text)
		}
	}
	kept := []llm.Message{{Role: "user", Content: []llm.Block{{Type: "text", Text: "[Summary of the earlier part of this session]\n" + summary.String()}}}}
	for _, msg := range msgs[cut:] {
		var blocks []llm.Block
		for _, b := range msg.Content {
			if b.Type != "thinking" && b.Type != "redacted_thinking" {
				blocks = append(blocks, b)
			}
		}
		if len(blocks) > 0 {
			kept = append(kept, llm.Message{Role: msg.Role, Content: blocks})
		}
	}
	s.mu.Lock()
	s.messages = kept
	s.mu.Unlock()
	s.emit("compaction", map[string]any{"summary": summary.String()}, true)
	return nil
}

// ensure mcpx import is used for type references in toolset closures.
var _ = mcpx.ToolName
