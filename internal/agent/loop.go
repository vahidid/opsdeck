package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

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
	if s.Info().Client != "" {
		return ErrClientSession
	}
	w, err := m.deps.Workspaces.Get(ws)
	if err != nil {
		return err
	}
	if err := checkModel(w, s.Info().Model); err != nil {
		return err
	}
	in := turnInput{Text: text, Display: text}
	if prompt, name, ok := expandCommand(w, text); ok {
		in.Text, in.Command = prompt, name
	}
	return m.start(s, w, in)
}

// turnInput is what starts a turn.
type turnInput struct {
	Text         string // for the model
	Display      string // shown in the chat (the command as typed)
	Command      string // the workspace command it came from
	Notification bool   // from opsdeck (a background task), not the user
}

// start runs a turn in the background: for a message of the user, or for a
// notification the agent should act on (a background task finished).
func (m *Manager) start(s *Session, w *workspace.Workspace, in turnInput) error {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(connect.WithActor(context.Background(), connect.Actor{Kind: "agent", Session: s.info.ID}))
	s.cancel, s.runCtx = cancel, ctx
	s.mu.Unlock()
	go m.run(ctx, s, w, in)
	return nil
}

func (m *Manager) env(w *workspace.Workspace) *Env {
	return &Env{WS: w, Hub: m.deps.Hub, Docker: m.deps.Docker, Kube: m.deps.Kube, DB: m.deps.DB, Git: m.deps.Git, DNS: m.deps.DNS, Deploy: m.deps.Deploy, ACME: m.deps.ACME, Ansible: m.deps.Ansible, Vault: m.deps.Vault, MCP: m.deps.MCP,
		MemoryPath: w.MemoryPath(), UserScripts: m.deps.UserScripts, Local: m.local(), Policy: m.policy(),
		Web: m.webClientFor(w), WebLevel: m.webAccess(w), Redact: m.redactor.Redact, Agents: m.agents(w)}
}

// sessionEnv is the tool environment of a session's turn.
func (m *Manager) sessionEnv(s *Session, w *workspace.Workspace) *Env {
	env := m.env(w)
	info := s.Info()
	env.SessionID, env.Model = info.ID, info.Model
	env.Outputs = s.Output
	env.Tasks = func(id string) *BgTask { return m.bg.get(w.Name(), info.ID, id) }
	env.AllTasks = func() []*BgTask { return m.bg.list(w.Name(), info.ID) }
	return env
}

func (m *Manager) agents(w *workspace.Workspace) []workspace.AgentDef {
	if !m.subagentsAvailable(w) {
		return nil
	}
	return subagentDefs(w)
}

// recallFor attaches the workspace knowledge matching the user's message to
// it (for the model), shows what was found in the chat, and returns the
// attached text.
func (m *Manager) recallFor(s *Session, env *Env, text string) string {
	s.mu.Lock()
	history := append([]llm.Message(nil), s.messages...)
	s.mu.Unlock()
	r, block := recall(env, text, history[:len(history)-1])
	if r == nil {
		return ""
	}
	block = "\n\n" + m.redactor.Redact(block)
	s.mu.Lock()
	last := &s.messages[len(s.messages)-1]
	last.Content = append(last.Content, llm.Block{Type: "text", Text: block})
	s.mu.Unlock()
	s.emit("recall", map[string]any{"items": r.Items, "preloaded": r.Preloaded}, true)
	return block
}

func (m *Manager) policy() Policy {
	if m.deps.Policy == nil {
		return Policy{}
	}
	return m.deps.Policy()
}

func (m *Manager) local() *LocalPolicy {
	if m.deps.Local == nil {
		return nil
	}
	return m.deps.Local()
}

func (m *Manager) run(ctx context.Context, s *Session, w *workspace.Workspace, in turnInput) {
	text, notification := in.Text, in.Notification
	display := m.redactor.Redact(firstNonEmpty(in.Display, in.Text))
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
		msg, errMsg := s.lastOutcome()
		m.turnEndHooks(s, w, msg, errMsg)
		go m.deliverInbox(s, w)
	}()

	text = m.redactor.Redact(text)
	s.mu.Lock()
	first := len(s.messages) == 0
	content := text
	if notification {
		content = "[opsdeck notification — not a message from the user]\n" + text
	} else if len(s.context) > 0 {
		// what the user ran with ! since the last message
		content = strings.Join(s.context, "\n\n") + "\n\n" + text
		s.context = nil
	}
	s.messages = append(s.messages, llm.Message{Role: "user", Content: []llm.Block{{Type: "text", Text: content}}})
	if !notification {
		s.taint = "" // a new message from the user: they are back in the loop
	}
	if first && !notification {
		title := strings.Join(strings.Fields(display), " ")
		if r := []rune(title); len(r) > 70 {
			title = string(r[:70]) + "…"
		}
		s.info.Title = title
	}
	mode, modelID := s.info.Mode, s.info.Model
	msgIndex := len(s.messages) - 1
	s.mu.Unlock()
	if notification {
		s.emit("notification", map[string]any{"text": text}, true)
	} else {
		ev := map[string]any{"text": display, "msgIndex": msgIndex}
		if in.Command != "" {
			ev["command"], ev["prompt"] = in.Command, text
		}
		s.emit("user", ev, true)
	}
	s.setState("running")
	s.save()
	if err := checkModel(w, modelID); err != nil {
		s.emit("error", map[string]any{"message": err.Error()}, true)
		return
	}

	router := m.deps.Router()
	if p, model, ok := router.Lookup(modelID); ok && extagent.IsKind(p.Type) {
		m.runExternal(ctx, s, w, content, p, model)
		return
	}
	provider, model, err := router.Resolve(modelID)
	if err != nil {
		s.emit("error", map[string]any{"message": err.Error()}, true)
		return
	}
	env := m.sessionEnv(s, w)
	env.Model = modelID
	if !notification {
		m.recallFor(s, env, text)
	}
	m.deps.MCP.Ensure(ctx, w)

	if err := m.maybeCompact(ctx, s, provider, model); err != nil && ctx.Err() == nil {
		s.emit("error", map[string]any{"message": "compaction failed: " + err.Error()}, true)
	}

	r := &runner{m: m, s: s, env: env, w: w}
	for step := 0; step < maxSteps; step++ {
		if ctx.Err() != nil {
			return
		}
		m.deps.Vault.Touch()
		s.mu.Lock()
		mode = s.info.Mode // the user may switch mode mid-run
		history := append([]llm.Message(nil), s.messages...)
		s.mu.Unlock()
		// per step: a resource added by the agent brings its tools along
		tools, toolMap := m.toolset(w)
		r.tools, env.Tools = toolMap, toolNames(toolMap)

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

		results := r.runCalls(ctx, calls, resp.StopReason == "max_tokens")
		s.mu.Lock()
		s.messages = append(s.messages, llm.Message{Role: "user", Content: results})
		s.mu.Unlock()
		s.save()
	}
	s.emit("error", map[string]any{"message": fmt.Sprintf("Stopped after %d steps; send a message to continue.", maxSteps)}, true)
}

// toolset returns the tools the workspace can use: built-in ones for what
// it has (no database tool without databases), this computer's when local
// access is on, the web's when allowed, and its MCP servers' tools.
func (m *Manager) toolset(w *workspace.Workspace) ([]llm.Tool, map[string]Tool) {
	all := availableTools(w, m.webAccess(w), m.subagentsAvailable(w))
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

func toolNames(m map[string]Tool) map[string]bool {
	out := make(map[string]bool, len(m))
	for n := range m {
		out[n] = true
	}
	return out
}

// runTool runs one call outside a model turn of opsdeck's own loop (the MCP
// bridge of an external agent).
func (m *Manager) runTool(ctx context.Context, s *Session, env *Env, w *workspace.Workspace, toolMap map[string]Tool, c llm.Block, truncatedTurn bool) llm.Block {
	r := &runner{m: m, s: s, env: env, w: w, tools: toolMap}
	return r.execute(ctx, r.prepare(ctx, c, truncatedTurn))
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
	return m.compact(ctx, s, p, model, false)
}

// compact summarizes older history; force does it whatever the size.
func (m *Manager) compact(ctx context.Context, s *Session, p llm.Provider, model string, force bool) error {
	s.mu.Lock()
	msgs := append([]llm.Message(nil), s.messages...)
	s.mu.Unlock()
	if !force && (estimateTokens(msgs) < compactThreshold || len(msgs) < 6) {
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
