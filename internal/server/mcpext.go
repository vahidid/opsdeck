package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opsdeck/internal/accesskeys"
)

// The MCP server for agents outside opsdeck (Claude Code, Codex, Cursor,
// Gemini CLI…): /mcp/ext over streamable HTTP, or `opsdeck mcp` over stdio.
// Each request carries an access key (Authorization: Bearer odk_…) made in
// App settings › Agent access; the key picks the workspace and the mode.

func (s *Server) accessKeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/access-keys", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"keys": s.keys.List(), "endpoint": s.mcpEndpoint(), "command": mcpCommand(), "modes": accesskeys.Modes})
	})
	mux.HandleFunc("POST /api/access-keys", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Label, Workspace, Mode string }
		if err := readJSON(r, &in); err != nil {
			fail(w, err)
			return
		}
		if _, err := s.ws.Get(in.Workspace); err != nil {
			fail(w, err)
			return
		}
		k, secret, err := s.keys.Create(in.Label, in.Workspace, in.Mode)
		if err != nil {
			fail(w, err)
			return
		}
		s.record(r, in.Workspace, "access_key.create", k.Label, map[string]any{"mode": k.Mode, "id": k.ID}, nil)
		writeJSON(w, 200, map[string]any{"key": k, "secret": secret, "endpoint": s.mcpEndpoint(), "command": mcpCommand()})
	})
	mux.HandleFunc("PATCH /api/access-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Label, Mode string }
		if err := readJSON(r, &in); err != nil {
			fail(w, err)
			return
		}
		k, err := s.keys.Update(r.PathValue("id"), in.Label, in.Mode)
		if err != nil {
			fail(w, err)
			return
		}
		s.record(r, k.Workspace, "access_key.update", k.Label, map[string]any{"mode": k.Mode, "id": k.ID}, nil)
		writeJSON(w, 200, k)
	})
	mux.HandleFunc("DELETE /api/access-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var k accesskeys.Key
		for _, x := range s.keys.List() {
			if x.ID == id {
				k = x
			}
		}
		if err := s.keys.Revoke(id); err != nil {
			fail(w, err)
			return
		}
		s.record(r, k.Workspace, "access_key.revoke", k.Label, map[string]any{"id": id}, nil)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}

func (s *Server) mcpEndpoint() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.port) + "/mcp/ext"
}

// mcpCommand is the program an agent starts for `mcp` over stdio: this
// opsdeck by its full path, since an agent started from a desktop menu
// (Claude Desktop, an IDE) has no PATH of the shell's, and the desktop app
// is not called opsdeck everywhere (opsdeck-desktop on Linux, inside
// opsdeck.app on macOS). A build run with `go run` gives plain opsdeck.
func mcpCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return "opsdeck"
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if strings.HasPrefix(exe, os.TempDir()) {
		return "opsdeck"
	}
	return exe
}

func accessKey(r *http.Request) string {
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
	}
	return ""
}

// mcpExtHandler serves the workspace of the request's access key. The key is
// checked on every request, so a revoked key stops working at once and a
// changed mode applies to the next call.
func (s *Server) mcpExtHandler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		k, ok := s.keys.Lookup(accessKey(r))
		if !ok {
			return nil
		}
		w, err := s.ws.Get(k.Workspace)
		if err != nil {
			return nil
		}
		sess, err := s.agents.ClientSession(w, k.ID, k.Label, k.Mode)
		if err != nil {
			return nil
		}
		return s.agents.ClientServer(w, sess, s.version)
	}, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Browsers always send Origin; agents don't. A web page must not
		// reach this even with a key.
		if r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		k, ok := s.keys.Lookup(accessKey(r))
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="opsdeck"`)
			http.Error(w, "invalid or revoked opsdeck access key: make one in opsdeck under App settings › Agent access", http.StatusUnauthorized)
			return
		}
		ws, err := s.ws.Get(k.Workspace)
		if err != nil {
			http.Error(w, "the workspace of this access key no longer exists", http.StatusNotFound)
			return
		}
		if _, err := s.agents.ClientSession(ws, k.ID, k.Label, k.Mode); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// Discovery: a running server writes where it listens, so `opsdeck mcp`
// finds it even when the desktop app had to use another port than 7788.

// ServerInfo is the content of <state>/server.json.
type ServerInfo struct {
	URL     string `json:"url"`
	MCP     string `json:"mcp"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

func ServerInfoFile(stateDir string) string { return filepath.Join(stateDir, "server.json") }

// Announce writes server.json; the returned func removes it again.
func (s *Server) Announce() func() {
	path := ServerInfoFile(s.dirs.State)
	b, _ := json.MarshalIndent(ServerInfo{URL: "http://127.0.0.1:" + strconv.Itoa(s.port), MCP: s.mcpEndpoint(), PID: os.Getpid(), Version: s.version}, "", "  ")
	os.WriteFile(path, b, 0o600)
	return func() {
		// only our own file: another opsdeck may have started since
		var cur ServerInfo
		if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &cur) == nil && cur.PID == os.Getpid() {
			os.Remove(path)
		}
	}
}
