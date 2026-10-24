package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opsdeck/internal/llm"
)

// The bridge exposes a running session's tools as an MCP server, so an
// external agent (Claude Code, Codex, Gemini CLI, any ACP agent) can work
// through opsdeck. Every call goes through runTool: the same risk
// classification, permission rules, approvals, redaction and audit as the
// built-in agent. A bridge token is valid only while its session's turn runs.

type bridge struct {
	ws, session string
	server      *mcp.Server
}

type bridges struct {
	mu sync.Mutex
	m  map[string]*bridge
}

func newToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// openBridge registers a token for the session's current turn.
func (m *Manager) openBridge(s *Session) string {
	tok := newToken()
	m.bridges.mu.Lock()
	if m.bridges.m == nil {
		m.bridges.m = map[string]*bridge{}
	}
	m.bridges.m[tok] = &bridge{ws: s.info.Workspace, session: s.info.ID}
	m.bridges.mu.Unlock()
	return tok
}

func (m *Manager) closeBridge(tok string) {
	m.bridges.mu.Lock()
	delete(m.bridges.m, tok)
	m.bridges.mu.Unlock()
}

// MCPHandler serves the bridge over streamable HTTP. The token comes from
// "Authorization: Bearer <token>" or the last path segment (/mcp/<token>).
func (m *Manager) MCPHandler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		b := m.lookupBridge(bridgeToken(r))
		if b == nil {
			return nil
		}
		return b.server
	}, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Browsers always send Origin; agents don't. Refuse browser access.
		if r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if m.lookupBridge(bridgeToken(r)) == nil {
			http.Error(w, "invalid or expired opsdeck session token", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func bridgeToken(r *http.Request) string {
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
	}
	parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
	return parts[len(parts)-1]
}

func (m *Manager) lookupBridge(tok string) *bridge {
	if len(tok) != 48 {
		return nil
	}
	m.bridges.mu.Lock()
	defer m.bridges.mu.Unlock()
	b := m.bridges.m[tok]
	if b == nil {
		return nil
	}
	if b.server == nil {
		b.server = m.bridgeServer(b)
	}
	return b
}

// bridgeServer builds the MCP server with the workspace's tool set.
func (m *Manager) bridgeServer(b *bridge) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "opsdeck", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "Infrastructure tools of the opsdeck workspace " + b.ws + ". Credentials are used by opsdeck and never shown; changes may wait for the user's approval.",
	})
	w, err := m.deps.Workspaces.Get(b.ws)
	if err != nil {
		return srv
	}
	defs, _ := m.toolset(w)
	for _, def := range defs {
		def := def
		tool := &mcp.Tool{Name: def.Name, Description: def.Description, InputSchema: def.Schema}
		srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, isErr := m.bridgeCall(b, def.Name, req.Params.Arguments)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}, IsError: isErr}, nil
		})
	}
	return srv
}

var errNoTurn = errors.New("this opsdeck session is not running a turn")

// bridgeCall runs one tool call inside the session's current turn.
func (m *Manager) bridgeCall(b *bridge, name string, args []byte) (string, bool) {
	s, err := m.Get(b.ws, b.session)
	if err != nil {
		return err.Error(), true
	}
	s.mu.Lock()
	ctx := s.runCtx
	s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return errNoTurn.Error(), true
	}
	w, err := m.deps.Workspaces.Get(b.ws)
	if err != nil {
		return err.Error(), true
	}
	env := m.env(w)
	s.mu.Lock()
	env.SessionID, env.Model = s.info.ID, s.info.Model
	s.mu.Unlock()
	env.Outputs = s.Output
	_, toolMap := m.toolset(w)
	if len(args) == 0 {
		args = []byte("{}")
	}
	res := m.runTool(ctx, s, env, w, toolMap, llm.Block{Type: "tool_use", ID: "x-" + uuid.NewString()[:8], Name: name, Input: args}, false)
	s.save()
	return res.Content, res.IsError
}
