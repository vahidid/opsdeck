package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"opsdeck/internal/extagent"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// SubagentConfig is App settings → Agent → Subagents.
type SubagentConfig struct {
	Model    string   // "" = the session's model
	Disabled bool     // no delegate tool
	Models   []string // every API model, for a fallback
}

const maxSubagentSteps = 40

// Built-in subagents; a workspace agent with the same name replaces one.
var builtinAgents = []workspace.AgentDef{
	{Name: "investigator", ReadOnly: true,
		Description: "Read-only investigation (diagnostics, logs, configs, metrics) of a well-defined question; several run in parallel",
		Prompt: `Investigate read-only: run diagnostics, read logs and configuration, query metrics and docs. You cannot change anything — changes are refused, so don't try. Work efficiently: send independent reads in one turn (they run in parallel), keep outputs bounded (head, tail, grep, LIMIT), and stop as soon as you can answer.
Report facts with their evidence (the command and the key lines of output) and say how sure you are; separate what you saw from what you infer.`},
	{Name: "deployer", ReadOnly: false,
		Description: "Carries out a planned, well-defined change step by step; every change asks the user as usual",
		Prompt: `Carry out the change described in the task, exactly and carefully. Check the current state first, change one step at a time, and verify after each step (service status, health checks, logs). Every change waits for the user's approval as usual; if one is denied, stop and report. If anything unexpected happens, stop and report instead of improvising a different change.
Report what you changed (exact commands and files), how you verified it, and anything left undone.`},
}

// subagentDefs are the subagents of a workspace.
func subagentDefs(w *workspace.Workspace) []workspace.AgentDef {
	own := w.Agents()
	out := append([]workspace.AgentDef(nil), own...)
	for _, b := range builtinAgents {
		dup := false
		for _, o := range own {
			dup = dup || o.Name == b.Name
		}
		if !dup {
			out = append(out, b)
		}
	}
	return out
}

func (m *Manager) subagentConfig() SubagentConfig {
	if m.deps.Subagents == nil {
		return SubagentConfig{Disabled: true}
	}
	return m.deps.Subagents()
}

// subagentModel picks a subagent's model: its own, the configured subagent
// model, the session's, else the first API model the workspace allows.
// Subagents run in opsdeck's own loop, so local agent CLIs do not qualify.
func (m *Manager) subagentModel(w *workspace.Workspace, own, session string) string {
	if m.deps.Router == nil {
		return ""
	}
	router := m.deps.Router()
	cfg := m.subagentConfig()
	ok := func(id string) bool {
		if id == "" || !ModelAllowed(w, id) {
			return false
		}
		p, _, found := router.Lookup(id)
		return found && !extagent.IsKind(p.Type)
	}
	for _, id := range append([]string{own, cfg.Model, session}, cfg.Models...) {
		if ok(id) {
			return id
		}
	}
	return ""
}

func (m *Manager) subagentsAvailable(w *workspace.Workspace) bool {
	return !m.subagentConfig().Disabled && m.subagentModel(w, "", "") != ""
}

// readOnlyHidden are tools a read-only subagent does not get: they can only
// change things, which it is not allowed to do.
var readOnlyHidden = map[string]bool{"write_file": true, "edit_file": true, "write_doc": true, "repo_write": true, "git_sync": true,
	"run_script": true, "vault_add_item": true, "vault_import_ssh_key": true, "known_hosts_import": true, "local_write_file": true,
	"local_edit_file": true, "ansible_vault": true, "ask_user": true, "merge_request": true, "memory": true}

// subagentTools is the tool set of a subagent: the session's, without
// delegate, without changing tools for read-only agents, limited to the
// agent's own list when it has one.
func subagentTools(all map[string]Tool, def workspace.AgentDef) ([]llm.Tool, map[string]Tool) {
	keep := map[string]bool{}
	for _, t := range def.Tools {
		keep[t] = true
	}
	byName := map[string]Tool{}
	var defs []llm.Tool
	for name, t := range all {
		if name == "delegate" || (def.ReadOnly && readOnlyHidden[name]) {
			continue
		}
		if len(keep) > 0 && !keep[name] && name != "read_output" {
			continue
		}
		byName[name] = t
	}
	// keep the order stable for prompt caching
	for _, t := range Tools() {
		if _, ok := byName[t.Def.Name]; ok {
			defs = append(defs, t.Def)
		}
	}
	for name, t := range byName {
		found := false
		for _, d := range defs {
			found = found || d.Name == name
		}
		if !found {
			defs = append(defs, t.Def)
		}
	}
	return defs, byName
}

type delegateKey struct{}

type delegateFunc func(ctx context.Context, def workspace.AgentDef, task string) (string, error)

func toolDelegate() Tool {
	return Tool{
		Def: llm.Tool{Name: "delegate", Description: "Hand a self-contained piece of work to a subagent (listed in the system prompt) and get its report: investigator for read-only investigations — several delegate calls in one turn run in parallel, e.g. one per host group or per symptom —, deployer for a planned change it carries out step by step with the usual approvals, or one of the workspace's own agents. The subagent does not see this conversation: give it everything it needs (hosts, symptoms, what to check, what to report).",
			Schema: obj(map[string]any{
				"agent":   str("Subagent name, e.g. investigator"),
				"task":    str("The complete task: goal, targets, what to check or change, what to report"),
				"context": str("Optional facts it should know (what you found so far, constraints)"),
			}, "agent", "task")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Agent, Task, Context string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Task) == "" {
				return nil, errors.New("task is empty")
			}
			var def *workspace.AgentDef
			var names []string
			for _, d := range env.Agents {
				names = append(names, d.Name)
				if d.Name == strings.TrimSpace(in.Agent) {
					d := d
					def = &d
				}
			}
			if def == nil {
				return nil, fmt.Errorf("no subagent %q; available: %s", in.Agent, strings.Join(names, ", "))
			}
			task := strings.TrimSpace(in.Task)
			if c := strings.TrimSpace(in.Context); c != "" {
				task += "\n\nContext from the main agent:\n" + c
			}
			return &Call{Risk: RiskRead, Target: "agent/" + def.Name, Scope: ScopeLocal, Serial: !def.ReadOnly, Match: def.Name,
				Preview: def.Name + ": " + task, Run: func(ctx context.Context) (string, error) {
					run, _ := ctx.Value(delegateKey{}).(delegateFunc)
					if run == nil {
						return "", errors.New("subagents are not available here")
					}
					return run(ctx, *def, task)
				}}, nil
		},
	}
}

// delegate runs a subagent for one task inside the call callID and returns
// its final report. Its tool calls go through the same permission path,
// shown in the chat inside the delegate call.
func (r *runner) delegate(ctx context.Context, callID string, def workspace.AgentDef, task string) (string, error) {
	if r.parent != "" {
		return "", errors.New("a subagent cannot start other subagents")
	}
	m, s := r.m, r.s
	info := s.Info()
	model := m.subagentModel(r.w, def.Model, info.Model)
	if model == "" {
		return "", errors.New("no API model for subagents: choose one in App settings → Agent (local agent CLIs cannot run subagents)")
	}
	provider, bare, err := m.deps.Router().Resolve(model)
	if err != nil {
		return "", err
	}
	mode := ""
	if def.ReadOnly {
		mode = ModePlan
	}
	env := *r.env
	defs, tools := subagentTools(r.tools, def)
	env.Tools = toolNames(tools)
	sub := &runner{m: m, s: s, env: &env, w: r.w, tools: tools, parent: callID, agent: def.Name, mode: mode}
	sessMode := info.Mode
	if mode != "" {
		sessMode = mode
	}
	system := buildSystemPrompt(&env, sessMode) + subagentRole(def)
	msgs := []llm.Message{{Role: "user", Content: []llm.Block{{Type: "text", Text: m.redactor.Redact(task)}}}}
	s.emit("subagent", map[string]any{"callId": callID, "agent": def.Name, "model": model, "readOnly": def.ReadOnly, "phase": "start"}, true)

	var final string
	toolCalls, steps := 0, 0
	var usage llm.Usage
	end := func(state string) {
		s.emit("subagent", map[string]any{"callId": callID, "agent": def.Name, "phase": "end", "state": state, "steps": steps, "toolCalls": toolCalls,
			"inputTokens": usage.InputTokens, "outputTokens": usage.OutputTokens}, true)
	}
	for ; steps < maxSubagentSteps; steps++ {
		if ctx.Err() != nil {
			end("interrupted")
			return "", ctx.Err()
		}
		msgID := uuid.NewString()[:8]
		resp, err := provider.Chat(ctx, llm.Request{Model: bare, System: m.redactor.Redact(system), Messages: msgs, Tools: defs}, func(delta string) {
			sub.emit("assistant_delta", map[string]any{"messageId": msgID, "text": delta}, false)
		})
		if err != nil {
			end("error")
			return "", fmt.Errorf("subagent %s: %w", def.Name, err)
		}
		usage.InputTokens += resp.Usage.InputTokens
		usage.OutputTokens += resp.Usage.OutputTokens
		s.mu.Lock()
		s.info.Usage.InputTokens += resp.Usage.InputTokens
		s.info.Usage.OutputTokens += resp.Usage.OutputTokens
		s.info.Usage.CacheRead += resp.Usage.CacheRead
		s.mu.Unlock()
		if len(resp.Content) > 0 {
			msgs = append(msgs, llm.Message{Role: "assistant", Content: resp.Content})
		}
		var texts []string
		var calls []llm.Block
		for _, b := range resp.Content {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					texts = append(texts, b.Text)
				}
			case "tool_use":
				calls = append(calls, b)
			}
		}
		if len(texts) > 0 {
			final = strings.Join(texts, "\n\n")
			sub.emit("assistant", map[string]any{"messageId": msgID, "text": final}, true)
		}
		if resp.StopReason == "refusal" {
			end("error")
			return "", errors.New("the subagent's model declined: " + resp.Refusal)
		}
		if len(calls) == 0 {
			steps++
			break
		}
		toolCalls += len(calls)
		results := sub.runCalls(ctx, calls, resp.StopReason == "max_tokens")
		msgs = append(msgs, llm.Message{Role: "user", Content: results})
		s.save()
	}
	if steps >= maxSubagentSteps && final == "" {
		end("error")
		return "", fmt.Errorf("subagent %s stopped after %d steps without a report", def.Name, steps)
	}
	end("done")
	if strings.TrimSpace(final) == "" {
		final = "(the subagent finished without a report)"
	}
	return fmt.Sprintf("Report of subagent %s (%d tool calls):\n\n%s", def.Name, toolCalls, final), nil
}

func subagentRole(def workspace.AgentDef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n# Your role: subagent %q\nYou work for the main opsdeck agent, not directly with the user: the main agent gave you the task below and only sees your final message. ", def.Name)
	if def.ReadOnly {
		b.WriteString("You are read-only: changes are refused. ")
	}
	b.WriteString("Finish with a concise report for the main agent: what you found or did, with evidence (commands and key output), and what is left open.\n\n")
	b.WriteString(def.Prompt)
	b.WriteString("\n")
	return b.String()
}
