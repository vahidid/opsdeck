package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"opsdeck/internal/config"
	"opsdeck/internal/extagent"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// mcpBase is the URL of the MCP bridge (e.g. http://127.0.0.1:7788/mcp/).
var mcpBase struct {
	sync.Mutex
	url string
}

// SetMCPBase tells the manager where the server serves the MCP bridge.
func (m *Manager) SetMCPBase(url string) {
	mcpBase.Lock()
	mcpBase.url = url
	mcpBase.Unlock()
}

const externalNote = `
You are running inside opsdeck. Your ONLY tools are those of the MCP server named "opsdeck" (they may appear as mcp__opsdeck__<name>). You have no local shell or file access: reach servers, clusters and files exclusively through these tools. A tool call may wait while the user approves it; a denied call returns an error explaining why.`

// runExternal runs one turn with a locally installed agent CLI.
func (m *Manager) runExternal(ctx context.Context, s *Session, w *workspace.Workspace, text string, p config.Provider, model string) {
	env := m.env(w)
	s.mu.Lock()
	env.SessionID, env.Model = s.info.ID, s.info.Model
	mode := s.info.Mode
	resume := ""
	if pid, id, ok := strings.Cut(s.info.External, ":"); ok && pid == p.ID {
		resume = id
	}
	s.mu.Unlock()

	m.deps.MCP.Ensure(ctx, w)
	tok := m.openBridge(s)
	defer m.closeBridge(tok)

	workDir := filepath.Join(w.StateDir(), "agent-cwd")
	os.MkdirAll(workDir, 0o700)
	bin, _ := os.Executable()
	mcpBase.Lock()
	base := mcpBase.url
	mcpBase.Unlock()

	defs, _ := m.toolset(w)
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	req := extagent.Request{
		ToolNames:  names,
		Spec:       extagent.Spec{Kind: p.Type, Command: p.Command, Args: p.Args, Env: p.Env, Model: model},
		Prompt:     text,
		System:     m.redactor.Redact(buildSystemPrompt(env, mode)) + externalNote,
		FirstTurn:  resume == "",
		Resume:     resume,
		MCPURL:     base,
		MCPToken:   tok,
		OpsdeckBin: bin,
		WorkDir:    workDir,
	}
	texts := map[string]string{}
	res, err := extagent.Run(ctx, req, func(ev extagent.Event) {
		switch ev.Kind {
		case "text_delta":
			s.emit("assistant_delta", map[string]any{"messageId": ev.MsgID, "text": ev.Text}, false)
		case "text":
			texts[ev.MsgID] = ev.Text
			s.emit("assistant", map[string]any{"messageId": ev.MsgID, "text": ev.Text}, true)
			s.mu.Lock()
			s.messages = append(s.messages, llm.Message{Role: "assistant", Content: []llm.Block{{Type: "text", Text: ev.Text}}})
			s.mu.Unlock()
		case "notice":
			s.emit("error", map[string]any{"message": ev.Text}, true)
		}
	})
	s.mu.Lock()
	if res.Resume != "" {
		s.info.External = p.ID + ":" + res.Resume
	}
	s.info.Usage.InputTokens += res.InputTokens
	s.info.Usage.OutputTokens += res.OutputTokens
	s.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		s.emit("error", map[string]any{"message": m.redactor.Redact(err.Error())}, true)
	}
	s.save()
}
