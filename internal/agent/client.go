package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opsdeck/internal/connect"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// Client sessions: an agent outside opsdeck (Claude Code, Codex, Cursor,
// Gemini CLI…) connects to the MCP server with an access key and uses the
// workspace's tools. Each key has a session of its own, named after it,
// where its calls show up and wait for approval like the built-in agent's:
// the same risk classification, permission rules, redaction and audit.
// Nobody chats in that session; the outside agent is the one thinking.

// ErrClientSession is returned for a message sent to a client session.
var ErrClientSession = errors.New("this session belongs to an agent connected through MCP; talk to it there")

// clientSessionID is the session of an access key.
func clientSessionID(keyID string) string { return "mcp-" + keyID }

// ClientSession returns (or creates) the session of an access key.
func (m *Manager) ClientSession(w *workspace.Workspace, keyID, label, mode string) (*Session, error) {
	id := clientSessionID(keyID)
	if s, err := m.Get(w.Name(), id); err == nil {
		s.mu.Lock()
		changed := s.info.Mode != mode || s.info.Client != label
		s.info.Mode, s.info.Client = mode, label
		s.info.Title = label + " (MCP)"
		s.mu.Unlock()
		if changed {
			s.save()
		}
		return s, nil
	}
	if !ValidMode(mode) {
		return nil, fmt.Errorf("invalid mode %q", mode)
	}
	now := time.Now().UTC()
	s := &Session{
		info:    Info{ID: id, Workspace: w.Name(), Title: label + " (MCP)", Mode: mode, State: "idle", Created: now, Updated: now, Client: label},
		granted: map[string]bool{}, subs: map[chan Event]struct{}{}, pending: map[string]*pendingApproval{}, questions: map[string]*pendingQuestion{},
	}
	s.path = filepath.Join(m.dir(w.Name()), id+".json")
	if err := s.save(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[w.Name()+"/"+id] = s
	m.mu.Unlock()
	s.emit("client_connected", map[string]any{"client": label}, true)
	return s, nil
}

// beginClientCall marks the session busy for one call of its client and
// returns the context the call runs in: interrupting the session in the UI,
// or the client hanging up, ends it (a waiting approval is then denied).
func (s *Session) beginClientCall(reqCtx context.Context) (context.Context, func()) {
	s.mu.Lock()
	if s.cancel == nil {
		ctx, cancel := context.WithCancel(connect.WithActor(context.Background(), connect.Actor{Kind: "agent", Session: s.info.ID}))
		s.cancel, s.runCtx = cancel, ctx
	}
	base := s.runCtx
	s.clientCalls++
	first := s.clientCalls == 1
	s.mu.Unlock()
	if first {
		s.setState("running")
	}
	ctx, stop := context.WithCancel(base)
	unhook := context.AfterFunc(reqCtx, stop)
	return ctx, func() {
		unhook()
		stop()
		s.mu.Lock()
		s.clientCalls--
		last := s.clientCalls == 0
		if last && s.cancel != nil {
			s.cancel()
			s.cancel, s.runCtx = nil, nil
		}
		s.mu.Unlock()
		if last {
			s.setState("idle")
		}
		s.save()
	}
}

// ErrClientLocked is the answer to a call while opsdeck is locked.
const errClientLocked = "opsdeck is locked: unlock it in the opsdeck app, then try again"

// ClientCall runs one tool call of a connected agent.
func (m *Manager) ClientCall(reqCtx context.Context, w *workspace.Workspace, s *Session, name string, args []byte) (string, bool) {
	if m.deps.Vault != nil && !m.deps.Vault.Unlocked() {
		return errClientLocked, true
	}
	ctx, done := s.beginClientCall(reqCtx)
	defer done()
	env := m.sessionEnv(s, w)
	_, toolMap := m.toolset(w)
	if len(args) == 0 {
		args = []byte("{}")
	}
	res := m.runTool(ctx, s, env, w, toolMap, llm.Block{Type: "tool_use", ID: "c-" + uuid.NewString()[:8], Name: name, Input: args}, false)
	return res.Content, res.IsError
}

// ClientServer is the MCP server a connected agent talks to: the workspace's
// tool set, each call run through ClientCall on the workspace as it is then.
func (m *Manager) ClientServer(w *workspace.Workspace, s *Session, version string) *mcp.Server {
	ws := w.Name()
	info := s.Info()
	var envs []string
	for _, e := range w.Environments {
		if e.Protected {
			envs = append(envs, e.Name+" (protected)")
		} else {
			envs = append(envs, e.Name)
		}
	}
	instructions := fmt.Sprintf(`Tools of the opsdeck workspace %q (%s): its servers, networks and VPNs, containers, clusters, databases, logs and docs.
opsdeck holds the credentials and uses them for you; you never see or need a password or key. Read before you change: look at the state first (status, logs, config), then propose the change.
Mode %q: %s Changes on a protected environment always wait for the user's approval in the opsdeck app, so a call may take until they answer; a denied call returns the reason — do not retry it unchanged.
Environments: %s. Start with the inventory tool to see what exists.`,
		w.Name(), firstNonEmpty(w.Metadata.Title, w.Name()), info.Mode, modeHint(info.Mode), strings.Join(envs, ", "))
	srv := mcp.NewServer(&mcp.Implementation{Name: "opsdeck", Title: "opsdeck · " + firstNonEmpty(w.Metadata.Title, w.Name()), Version: version}, &mcp.ServerOptions{Instructions: instructions})
	defs, _ := m.toolset(w)
	for _, def := range defs {
		def := def
		if clientHidden[def.Name] {
			continue
		}
		tool := &mcp.Tool{Name: def.Name, Description: def.Description, InputSchema: def.Schema}
		srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cur, err := m.deps.Workspaces.Get(ws)
			if err != nil {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}, nil
			}
			out, isErr := m.ClientCall(ctx, cur, s, def.Name, req.Params.Arguments)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}, IsError: isErr}, nil
		})
	}
	return srv
}

// clientHidden are tools only opsdeck's own agent has: a connected agent
// asks its user and keeps its checklist itself, and subagents need opsdeck's
// model.
var clientHidden = map[string]bool{"delegate": true, "ask_user": true, "todo": true}

func modeHint(mode string) string {
	switch mode {
	case ModePlan:
		return "read-only: anything that changes a system is refused."
	case ModeAsk:
		return "every change asks the user first."
	default:
		return "changes the workspace's rules allow run; the others ask the user."
	}
}
