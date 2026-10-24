package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"opsdeck/internal/audit"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// maxParallel bounds the reads of one model turn that run at the same time.
const maxParallel = 6

// A runner executes the tool calls of one agent: the session's own, or a
// subagent working for it. Every call takes the same path: prepare →
// decision (workspace rules, mode, grants, hooks, web taint) → approval →
// run → redact → audit. Reads that need no approval run in parallel.
type runner struct {
	m     *Manager
	s     *Session
	env   *Env
	w     *workspace.Workspace
	tools map[string]Tool
	// parent is the delegate call a subagent runs under; its events carry
	// it so the chat shows them inside that call ("" = the session's agent).
	parent string
	agent  string // subagent name, for the audit journal
	mode   string // a subagent's own mode ("" = the session's current mode)
}

func (r *runner) emit(typ string, data map[string]any, persist bool) Event {
	if r.parent != "" {
		data["parent"] = r.parent
	}
	return r.s.emit(typ, data, persist)
}

// pendingCall is a tool call between its permission decision and its run.
type pendingCall struct {
	block     llm.Block
	call      *Call
	decision  Decision
	inputView any
	result    *llm.Block // set when the call ended before it could run
}

// parallel reports whether the call can run alongside other reads: an
// allowed read that needs nothing from the user.
func (p *pendingCall) parallel() bool {
	return p.result == nil && p.decision.Effect == "allow" && p.call.Risk == RiskRead && p.call.Secret == nil && !p.call.Serial
}

func (r *runner) toolResult(id, out string, isErr bool) llm.Block {
	return llm.Block{Type: "tool_result", ToolUseID: id, Content: truncate(r.m.redactor.Redact(out), toolOutputToModel), IsError: isErr}
}

// runCalls runs one model turn's tool calls in order. A run of consecutive
// reads that need no approval runs in parallel; everything else runs one
// after the other, so a change never sees a stale preview.
func (r *runner) runCalls(ctx context.Context, calls []llm.Block, truncatedTurn bool) []llm.Block {
	results := make([]llm.Block, len(calls))
	interrupted := func(c llm.Block) llm.Block {
		return llm.Block{Type: "tool_result", ToolUseID: c.ID, Content: "Interrupted by the user before running.", IsError: true}
	}
	for i := 0; i < len(calls); {
		if ctx.Err() != nil {
			results[i] = interrupted(calls[i])
			i++
			continue
		}
		p := r.prepare(ctx, calls[i], truncatedTurn)
		if !p.parallel() {
			results[i] = r.execute(ctx, p)
			i++
			continue
		}
		batch := map[int]*pendingCall{i: p}
		j := i + 1
		var next *pendingCall
		for ; j < len(calls) && ctx.Err() == nil; j++ {
			q := r.prepare(ctx, calls[j], truncatedTurn)
			if q.result != nil {
				results[j] = *q.result
				continue
			}
			if q.parallel() {
				batch[j] = q
				continue
			}
			next = q
			break
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxParallel)
		for k, q := range batch {
			wg.Add(1)
			go func(k int, q *pendingCall) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				results[k] = r.execute(ctx, q)
			}(k, q)
		}
		wg.Wait()
		if next != nil {
			results[j] = r.execute(ctx, next)
			j++
		}
		i = j
	}
	return results
}

// prepare builds the call and decides whether it runs, asks or is denied.
func (r *runner) prepare(ctx context.Context, c llm.Block, truncatedTurn bool) *pendingCall {
	m := r.m
	p := &pendingCall{block: c}
	json.Unmarshal([]byte(m.redactor.Redact(string(c.Input))), &p.inputView)
	base := map[string]any{"callId": c.ID, "tool": c.Name, "input": p.inputView}
	fail := func(msg string) *pendingCall {
		r.emit("tool_call", merge(base, map[string]any{"risk": RiskRead}), true)
		r.emit("tool_result", map[string]any{"callId": c.ID, "output": m.redactor.Redact(msg), "isError": true, "durationMs": 0}, true)
		res := r.toolResult(c.ID, msg, true)
		p.result = &res
		return p
	}
	t, ok := r.tools[c.Name]
	if !ok {
		return fail("Unknown tool " + c.Name)
	}
	if truncatedTurn {
		return fail("The response hit the output limit before this tool call was complete; it was not run. Retry with smaller input (e.g. edit_file instead of write_file).")
	}
	call, err := t.Prepare(ctx, r.env, c.Input)
	if err != nil {
		return fail(err.Error())
	}
	p.call = call
	ev := map[string]any{"risk": call.Risk, "target": call.Target, "environment": call.Environment, "reasons": call.Reasons}
	if r.agent != "" {
		ev["agent"] = r.agent
	}
	r.emit("tool_call", merge(base, ev), true)
	p.decision = r.decide(c, call)
	return p
}

// decide applies the permission policy: workspace rules, the mode,
// session grants and credentials that confirm on use.
func (r *runner) decide(c llm.Block, call *Call) Decision {
	s := r.s
	s.mu.Lock()
	mode := s.info.Mode
	if r.mode != "" {
		mode = r.mode
	}
	granted := map[string]bool{}
	for k := range s.granted {
		granted[k] = true
	}
	s.mu.Unlock()
	d := Decide(mode, r.w, c.Name, call, granted, r.m.deps.Vault, r.m.policy())
	if call.Secret != nil && d.Effect == "allow" {
		// the tool needs a password only the user can give
		d = Decision{"ask", firstNonEmpty(call.Secret.Reason, "needs a password from you")}
	}
	return d
}

// lateDecision adds, right before the call runs, what depends on the calls
// before it: web content read in this turn, the hooks, and whether anyone
// can approve.
func (r *runner) lateDecision(ctx context.Context, p *pendingCall) Decision {
	s, d := r.s, p.decision
	s.mu.Lock()
	taint, headless := s.taint, s.info.Headless
	s.mu.Unlock()
	if d.Effect == "allow" && taint != "" && p.call.Risk != RiskRead {
		// Instructions planted in a web page must not run unseen.
		d = Decision{"ask", "follows web content read in this turn (" + taint + "); web pages can carry instructions"}
	}
	d = r.m.beforeHooks(ctx, r, p.block, p.call, d)
	if headless && d.Effect == "ask" {
		d = Decision{"deny", "headless run: nobody can approve this (" + firstNonEmpty(d.Reason, "needs approval") + "); allow it with a workspace permission rule or run in a mode that allows it"}
	}
	return d
}

// execute asks for approval when needed and runs the call.
func (r *runner) execute(ctx context.Context, p *pendingCall) llm.Block {
	if p.result != nil {
		return *p.result
	}
	m, s, c, call := r.m, r.s, p.block, p.call
	d := r.lateDecision(ctx, p)
	info := s.Info()
	auditBase := audit.Entry{Workspace: r.w.Name(), Actor: "agent:" + info.ID, Action: "tool." + c.Name, Target: call.Target,
		Detail: map[string]any{"risk": call.Risk, "match": m.redactor.Redact(call.Match), "session": info.ID}}
	if r.agent != "" {
		auditBase.Detail["subagent"] = r.agent
	}
	approvedBy := "policy (" + d.Reason + ")"
	switch d.Effect {
	case "deny":
		e := auditBase
		e.Result = "denied"
		e.Detail["reason"] = d.Reason
		m.deps.Audit.Record(e)
		msg := "Denied: " + d.Reason
		r.emit("tool_result", map[string]any{"callId": c.ID, "output": msg, "isError": true, "durationMs": 0}, true)
		return r.toolResult(c.ID, msg, true)
	case "ask":
		req := map[string]any{"callId": c.ID, "tool": c.Name, "input": p.inputView,
			"risk": call.Risk, "target": call.Target, "environment": call.Environment, "reason": d.Reason,
			"reasons": call.Reasons, "preview": m.redactor.Redact(call.Preview), "grantLabel": call.GrantLabel}
		if len(call.Hosts) > 1 && call.Risk != RiskRead {
			req["hosts"] = call.Hosts
		}
		var check func(string) error
		if call.Secret != nil {
			check = call.Secret.Check
			if check == nil {
				check = func(string) error { return nil }
			}
			req["secret"] = map[string]any{"label": call.Secret.Label, "rememberLabel": call.Secret.RememberLabel}
		}
		a := r.waitApproval(ctx, req, check)
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
			r.emit("tool_result", map[string]any{"callId": c.ID, "output": msg, "isError": true, "durationMs": 0}, true)
			return r.toolResult(c.ID, msg, true)
		}
		s.mu.Lock()
		if a.decision == "always" {
			s.granted[grantKey(c.Name, call)] = true
		}
		// the user has seen what follows the web content
		s.taint = ""
		s.mu.Unlock()
		if a.canary {
			call.canary = true
		}
		if call.Secret != nil && call.Secret.Use != nil && a.secret != "" {
			call.Secret.Use(a.secret, a.decision == "always")
			e := auditBase
			e.Action, e.Result = "secret.entered", "ok"
			e.Detail = map[string]any{"for": c.Name, "label": call.Secret.Label, "remembered": a.decision == "always", "session": info.ID}
			m.deps.Audit.Record(e)
		}
		approvedBy = "user"
		if a.canary {
			approvedBy = "user (canary first)"
		}
	}

	sc := &callScope{callID: c.ID}
	sc.progress = func(text string) {
		r.emit("tool_progress", map[string]any{"callId": c.ID, "text": m.redactor.Redact(text)}, false)
	}
	sc.confirm = func(ctx context.Context, st Step) (bool, string) {
		if s.Info().Headless {
			return false, "headless run: nobody can approve the next step"
		}
		req := map[string]any{"callId": c.ID, "tool": c.Name, "input": p.inputView, "stage": "next",
			"risk": firstNonEmpty(st.Risk, call.Risk), "target": call.Target, "environment": call.Environment,
			"reason": st.Reason, "title": st.Title, "preview": m.redactor.Redact(st.Preview)}
		a := r.waitApproval(ctx, req, nil)
		e := auditBase
		e.Action = "tool." + c.Name + ".step"
		e.Result = "approved"
		if a.decision == "deny" {
			e.Result = "denied"
		}
		e.Detail = map[string]any{"step": st.Title, "session": info.ID}
		m.deps.Audit.Record(e)
		return a.decision != "deny", a.note
	}
	runCtx := withCallScope(ctx, sc)
	runCtx = withAsk(runCtx, func(ctx context.Context, q Question) (Answer, error) { return r.ask(ctx, c.ID, q) })
	runCtx = withTodo(runCtx, func(items []TodoItem) {
		r.emit("todo", map[string]any{"callId": c.ID, "items": items}, true)
	})
	runCtx = context.WithValue(runCtx, delegateKey{}, delegateFunc(func(ctx context.Context, def workspace.AgentDef, task string) (string, error) {
		return r.delegate(ctx, c.ID, def, task)
	}))
	runCtx = context.WithValue(runCtx, bgKey{}, bgStarter(func(tool, target, command, notifyOn string, limit time.Duration, run bgRun) (*BgTask, error) {
		return r.startBackground(ctx, c.ID, tool, target, command, notifyOn, limit, run)
	}))
	start := time.Now()
	out, err := call.Run(runCtx)
	dur := time.Since(start).Milliseconds()
	isErr := err != nil
	if err != nil {
		if out != "" {
			out += "\n"
		}
		out += "error: " + err.Error()
	}
	out = m.redactor.Redact(out)
	if call.Tainting != "" && !isErr {
		s.mu.Lock()
		s.taint = call.Tainting
		s.mu.Unlock()
	}
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
	ev := map[string]any{"callId": c.ID, "output": truncate(out, 200000), "isError": isErr, "durationMs": dur}
	toModel := truncate(out, toolOutputToModel)
	if len(out) > toolOutputToModel {
		// Keep the whole output: the model reads the rest with read_output.
		if id, err := s.saveOutput(out); err == nil {
			ev["outputId"], ev["outputSize"] = id, len(out)
			toModel = truncate(out, toolOutputToModel-300) + fmt.Sprintf("\n\n[The full output (%d bytes) is saved as %s: read other parts with read_output id=%s and offset/limit or grep.]", len(out), id, id)
		}
	}
	if sc.view != nil {
		if v := r.redactView(sc.view); v != nil {
			ev["view"] = v
		}
	}
	r.emit("tool_result", ev, true)
	m.afterHooks(r, c, call, out, isErr, dur)
	return llm.Block{Type: "tool_result", ToolUseID: c.ID, Content: toModel, IsError: isErr}
}

// redactView passes a view through the redactor (as JSON) like any output.
func (r *runner) redactView(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if json.Unmarshal([]byte(r.m.redactor.Redact(string(b))), &out) != nil {
		return nil
	}
	return out
}

// waitApproval shows an approval card and waits for the user's answer (a
// denial when the turn is interrupted).
func (r *runner) waitApproval(ctx context.Context, req map[string]any, check func(string) error) approval {
	s := r.s
	id := uuid.NewString()[:8]
	ch := make(chan approval, 1)
	pa := &pendingApproval{ch: ch, check: check}
	req["approvalId"] = id
	s.mu.Lock()
	s.pending[id] = pa
	s.mu.Unlock()
	s.refreshState()
	r.emit("approval_request", req, true)
	s.save()
	var a approval
	var answered bool
	select {
	case a, answered = <-ch:
	case <-ctx.Done():
	}
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
	if !answered {
		a = approval{decision: "deny", note: "interrupted"}
	}
	res := map[string]any{"approvalId": id, "decision": a.decision, "note": a.note}
	if a.canary {
		res["canary"] = true
	}
	r.emit("approval_resolved", res, true)
	if ctx.Err() == nil {
		s.refreshState()
	}
	return a
}

// ---------------------------------------------------------------------------
// full outputs

var outputIDRe = regexp.MustCompile(`^o-[a-f0-9]{8}$`)

// outputDir holds the full outputs of the session's long tool results.
func (s *Session) outputDir() string { return strings.TrimSuffix(s.path, ".json") + ".outputs" }

// saveOutput keeps a long (already redacted) output and returns its id.
func (s *Session) saveOutput(out string) (string, error) {
	if len(out) > workspace.MaxArtifactSize {
		out = out[:workspace.MaxArtifactSize] + "\n... [cut at 5 MB]\n"
	}
	id := "o-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	dir := s.outputDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return id, os.WriteFile(filepath.Join(dir, id+".txt"), []byte(out), 0o600)
}

// Output returns a saved full output.
func (s *Session) Output(id string) (string, error) {
	if !outputIDRe.MatchString(id) {
		return "", fmt.Errorf("invalid output id %q", id)
	}
	b, err := os.ReadFile(filepath.Join(s.outputDir(), id+".txt"))
	if err != nil {
		return "", fmt.Errorf("output %s not found", id)
	}
	return string(b), nil
}
