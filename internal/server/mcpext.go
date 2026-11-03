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
	"opsdeck/internal/agentlink"
	"opsdeck/plugins"
)

// The MCP server for agents outside opsdeck (Claude Code, Codex, Cursor,
// Gemini CLI…): /mcp/ext over streamable HTTP, or `opsdeck mcp` over stdio.
// Each request carries an access key (Authorization: Bearer odk_…) made in
// App settings › Agent access; the key picks the workspace and the mode.

func (s *Server) accessKeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/access-keys", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"keys": s.keyViews(), "endpoint": s.mcpEndpoint(), "command": mcpCommand(), "modes": accesskeys.Modes})
	})
	// The agents opsdeck can connect by itself, and whether each is here.
	mux.HandleFunc("GET /api/agent-clients", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"clients": agentlink.Clients()})
	})
	// Make a key; with a client, write it into that agent's config too.
	mux.HandleFunc("POST /api/access-keys", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Label, Workspace, Mode, Client string
			Skill                          bool
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, err)
			return
		}
		if _, err := s.ws.Get(in.Workspace); err != nil {
			fail(w, err)
			return
		}
		if in.Client != "" && !agentlink.Known(in.Client) {
			writeErr(w, 400, "unknown agent "+in.Client)
			return
		}
		k, secret, err := s.keys.Create(in.Label, in.Workspace, in.Mode)
		if err != nil {
			fail(w, err)
			return
		}
		s.record(r, in.Workspace, "access_key.create", k.Label, map[string]any{"mode": k.Mode, "id": k.ID, "client": in.Client}, nil)
		out := map[string]any{"key": k, "secret": secret, "endpoint": s.mcpEndpoint(), "command": mcpCommand()}
		if in.Client != "" {
			link, err := s.linkKey(r, k, secret, in.Client, in.Skill)
			if err != nil {
				// the key stays: the user can still set it up by hand
				out["linkError"] = err.Error()
			} else {
				out["link"] = link
				k, _ = s.keys.Get(k.ID)
				out["key"] = k
			}
		}
		writeJSON(w, 200, out)
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
	// A new secret for a key. When opsdeck wrote the key into an agent's
	// config (or the request names one), the config gets the new key too;
	// the secret is returned once either way.
	mux.HandleFunc("POST /api/access-keys/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Client string
			Skill  bool
		}
		readJSON(r, &in)
		old, ok := s.keys.Get(r.PathValue("id"))
		if !ok {
			writeErr(w, 404, "no such access key")
			return
		}
		client := firstNonEmpty(in.Client, old.Client)
		if client != "" && !agentlink.Known(client) {
			writeErr(w, 400, "unknown agent "+client)
			return
		}
		k, secret, err := s.keys.Rotate(old.ID)
		if err != nil {
			fail(w, err)
			return
		}
		s.record(r, k.Workspace, "access_key.rotate", k.Label, map[string]any{"id": k.ID, "client": client}, nil)
		out := map[string]any{"key": k, "secret": secret, "endpoint": s.mcpEndpoint(), "command": mcpCommand()}
		if client != "" {
			if link, err := s.linkKey(r, k, secret, client, in.Skill); err != nil {
				out["linkError"] = err.Error()
			} else {
				out["link"] = link
				k, _ = s.keys.Get(k.ID)
				out["key"] = k
			}
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("DELETE /api/access-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		k, _ := s.keys.Get(id)
		if err := s.keys.Revoke(id); err != nil {
			fail(w, err)
			return
		}
		detail := map[string]any{"id": id}
		// also take it out of the agent's config, unless asked to keep it
		if k.Client != "" && r.URL.Query().Get("keepConfig") == "" {
			if err := agentlink.Uninstall(r.Context(), k.Client, k.Server); err != nil {
				detail["unlinkError"] = err.Error()
			} else {
				detail["unlinked"] = k.Client
			}
		}
		s.record(r, k.Workspace, "access_key.revoke", k.Label, detail, nil)
		writeJSON(w, 200, map[string]any{"ok": true, "unlinkError": detail["unlinkError"]})
	})
}

// keyView is a key as the UI lists it: is it still in its agent's config?
type keyView struct {
	accesskeys.Key
	Linked bool `json:"linked"`
}

func (s *Server) keyViews() []keyView {
	var out []keyView
	for _, k := range s.keys.List() {
		out = append(out, keyView{Key: k, Linked: k.Client != "" && agentlink.Installed(k.Client, k.Server)})
	}
	return out
}

// linkKey writes a key into an agent's config (and the skill, when asked).
// Another key of this workspace in the same agent is revoked: its entry
// is the one just replaced.
func (s *Server) linkKey(r *http.Request, k accesskeys.Key, secret, client string, skill bool) (map[string]any, error) {
	name := agentlink.ServerName(k.Workspace)
	path, err := agentlink.Install(r.Context(), client, agentlink.Target{Name: name, Endpoint: s.mcpEndpoint(), Secret: secret, Command: mcpCommand()})
	if err != nil {
		return nil, err
	}
	for _, o := range s.keys.List() {
		if o.ID != k.ID && o.Client == client && o.Server == name {
			s.keys.Revoke(o.ID)
			s.record(r, o.Workspace, "access_key.revoke", o.Label, map[string]any{"id": o.ID, "replacedBy": k.ID}, nil)
		}
	}
	s.keys.SetClient(k.ID, client, name)
	out := map[string]any{"client": client, "config": path, "server": name}
	for _, c := range agentlink.Clients() {
		if c.ID == client {
			out["note"] = c.Note
			if skill && c.Skill != "" {
				if where, err := plugins.InstallSkill(c.Skill, "", true); err != nil {
					out["skillError"] = err.Error()
				} else {
					out["skill"] = where
				}
			}
		}
	}
	s.record(r, k.Workspace, "access_key.link", k.Label, map[string]any{"id": k.ID, "client": client, "config": path}, nil)
	return out, nil
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
