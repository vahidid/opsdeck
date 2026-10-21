// Package mcpx runs the external MCP servers a workspace declares (stdio) and
// exposes their tools to the agent. Environment values may be vault
// references; they are resolved into the server process only.
package mcpx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ReadOnly    bool           `json:"readOnly"`
	Destructive bool           `json:"destructive"`
	Schema      map[string]any `json:"-"`
}

type Status struct {
	Name   string     `json:"name"`
	Status string     `json:"status"` // stopped | running | error
	Error  string     `json:"error,omitempty"`
	Tools  []ToolInfo `json:"tools"`
}

type server struct {
	untrusted bool
	cfg       workspace.MCPServer
	session   *mcp.ClientSession
	tools     []ToolInfo
	err       string
}

type Manager struct {
	vault *vault.Vault
	// Trusted reports whether the user approved a server definition on this machine.
	Trusted func(ws string, cfg workspace.MCPServer) bool

	mu      sync.Mutex
	servers map[string]*server // ws/name
}

func New(v *vault.Vault) *Manager { return &Manager{vault: v, servers: map[string]*server{}} }

// Hash identifies a server definition (workspace + command line + env keys/refs).
func Hash(ws string, cfg workspace.MCPServer) string {
	b, _ := json.Marshal(struct {
		WS  string
		Cfg workspace.MCPServer
	}{ws, cfg})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sameConfig(a, b workspace.MCPServer) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Ensure starts (or restarts after a config change) every server of a workspace.
func (m *Manager) Ensure(ctx context.Context, w *workspace.Workspace) {
	wanted := map[string]bool{}
	for _, cfg := range w.MCPServers {
		key := w.Name() + "/" + cfg.Name
		wanted[key] = true
		m.mu.Lock()
		s := m.servers[key]
		m.mu.Unlock()
		trusted := m.Trusted == nil || m.Trusted(w.Name(), cfg)
		if s != nil && sameConfig(s.cfg, cfg) && (s.session != nil || (s.err != "" && !(s.untrusted && trusted))) {
			continue
		}
		if s != nil && s.session != nil {
			s.session.Close()
		}
		var ns *server
		if !trusted {
			ns = &server{cfg: cfg, untrusted: true, err: "not trusted on this machine: review the command in Workspace settings and click Trust to allow it to run"}
		} else {
			ns = m.start(ctx, w.Name(), cfg)
		}
		m.mu.Lock()
		m.servers[key] = ns
		m.mu.Unlock()
	}
	m.mu.Lock()
	var stale []*server
	for key, s := range m.servers {
		if strings.HasPrefix(key, w.Name()+"/") && !wanted[key] {
			stale = append(stale, s)
			delete(m.servers, key)
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		if s.session != nil {
			s.session.Close()
		}
	}
}

func (m *Manager) start(ctx context.Context, ws string, cfg workspace.MCPServer) *server {
	s := &server{cfg: cfg}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		if strings.HasPrefix(v, "vault://") {
			it, field, err := m.vault.ResolveFor(ws, v)
			if err != nil {
				s.err = fmt.Sprintf("env %s: %v", k, err)
				return s
			}
			if field == "" {
				field = firstSecretField(it)
			}
			v = it.Get(field)
		}
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "opsdeck", Version: "0.1.0"}, nil)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, err := client.Connect(cctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		s.err = err.Error()
		return s
	}
	list, err := session.ListTools(cctx, nil)
	if err != nil {
		session.Close()
		s.err = err.Error()
		return s
	}
	for _, t := range list.Tools {
		info := ToolInfo{Name: t.Name, Description: t.Description}
		if t.Annotations != nil {
			info.ReadOnly = t.Annotations.ReadOnlyHint
			info.Destructive = t.Annotations.DestructiveHint != nil && *t.Annotations.DestructiveHint
		}
		b, _ := json.Marshal(t.InputSchema)
		json.Unmarshal(b, &info.Schema)
		if info.Schema == nil {
			info.Schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		s.tools = append(s.tools, info)
	}
	s.session = session
	return s
}

func firstSecretField(it vault.Item) string {
	for _, f := range it.Fields {
		if f.Secret && f.Value != "" {
			return f.Name
		}
	}
	return ""
}

// Statuses reports the servers of a workspace.
func (m *Manager) Statuses(w *workspace.Workspace) []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Status{}
	for _, cfg := range w.MCPServers {
		st := Status{Name: cfg.Name, Status: "stopped", Tools: []ToolInfo{}}
		if s := m.servers[w.Name()+"/"+cfg.Name]; s != nil {
			if s.err != "" {
				st.Status, st.Error = "error", s.err
			} else if s.session != nil {
				st.Status = "running"
				st.Tools = s.tools
			}
		}
		out = append(out, st)
	}
	return out
}

var unsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ToolName is the agent-facing name of an MCP tool.
func ToolName(server, tool string) string {
	n := "mcp__" + unsafe.ReplaceAllString(server, "_") + "__" + unsafe.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// AgentTool is an MCP tool as seen by the agent.
type AgentTool struct {
	Server string
	ToolInfo
	FullName string
}

func (m *Manager) Tools(w *workspace.Workspace) []AgentTool {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AgentTool
	for _, cfg := range w.MCPServers {
		s := m.servers[w.Name()+"/"+cfg.Name]
		if s == nil || s.session == nil {
			continue
		}
		for _, t := range s.tools {
			out = append(out, AgentTool{Server: cfg.Name, ToolInfo: t, FullName: ToolName(cfg.Name, t.Name)})
		}
	}
	return out
}

// Call invokes a tool and returns its text output.
func (m *Manager) Call(ctx context.Context, w *workspace.Workspace, serverName, tool string, args json.RawMessage) (string, bool, error) {
	m.mu.Lock()
	s := m.servers[w.Name()+"/"+serverName]
	m.mu.Unlock()
	if s == nil || s.session == nil {
		return "", false, fmt.Errorf("MCP server %s is not running", serverName)
	}
	var a any
	if len(args) > 0 {
		json.Unmarshal(args, &a)
	}
	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: a})
	if err != nil {
		return "", false, err
	}
	var b strings.Builder
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			b.WriteString(v.Text)
			b.WriteString("\n")
		default:
			j, _ := json.Marshal(v)
			b.Write(j)
			b.WriteString("\n")
		}
	}
	return b.String(), res.IsError, nil
}

// CloseAll stops every server.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.servers {
		if s.session != nil {
			s.session.Close()
		}
		delete(m.servers, k)
	}
}
