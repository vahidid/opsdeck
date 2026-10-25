// Package server exposes the local HTTP API and serves the web UI.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/agent"
	"opsdeck/internal/ansible"
	"opsdeck/internal/audit"
	"opsdeck/internal/config"
	"opsdeck/internal/connect"
	"opsdeck/internal/dbx"
	"opsdeck/internal/dnsx"
	"opsdeck/internal/docker"
	"opsdeck/internal/gitx"
	"opsdeck/internal/kube"
	"opsdeck/internal/llm"
	"opsdeck/internal/mcpx"
	"opsdeck/internal/paths"
	"opsdeck/internal/update"
	"opsdeck/internal/vault"
	"opsdeck/internal/vpn"
	"opsdeck/internal/workspace"
)

//go:embed all:dist
var distFS embed.FS

// Server wires every service together.
type Server struct {
	dirs     paths.Dirs
	cfg      *config.Store
	vault    *vault.Vault
	audit    *audit.Log
	ws       *workspace.Manager
	hub      *connect.Hub
	docker   *docker.Client
	kube     *kube.Manager
	db       *dbx.Manager
	git      *gitx.Manager
	dns      *dnsx.Manager
	ansible  *ansible.Manager
	mcp      *mcpx.Manager
	agents   *agent.Manager
	forwards *connect.Forwards

	token     string
	devOrigin string
	port      int
	desktop   bool
	updater   *update.Updater
}

// Options configure the server.
type Options struct {
	Port      int
	DevOrigin string // extra allowed Origin for UI development (e.g. http://localhost:5173)
	Desktop   bool   // serving the desktop app's window: links and downloads go through the server
	Version   string // of this opsdeck, for updates
	// Quit ends the program so an update can be installed (desktop app);
	// without it an update asks for a restart.
	Quit func()
}

// New opens all stores under dirs.
func New(dirs paths.Dirs, opts Options) (*Server, error) {
	if err := dirs.Ensure(); err != nil {
		return nil, err
	}
	cfg, err := config.Load(dirs.ConfigFile())
	if err != nil {
		return nil, err
	}
	v, err := vault.Open(dirs.VaultFile())
	if err != nil {
		return nil, err
	}
	v.SetAutoLock(time.Duration(cfg.Settings().AutoLockMinutes) * time.Minute)
	al, err := audit.Open(dirs.AuditDir())
	if err != nil {
		return nil, err
	}
	s := &Server{dirs: dirs, cfg: cfg, vault: v, audit: al, port: opts.Port, devOrigin: opts.DevOrigin, desktop: opts.Desktop}
	s.ws = workspace.NewManager(dirs.Workspaces(), cfg.ExternalWorkspaces(), cfg.SetExternalWorkspaces)
	// Older versions kept per-workspace state outside the folder; adopt it.
	for _, w := range s.ws.List() {
		if err := w.AdoptLegacyState(dirs.WorkspaceState(w.Name())); err != nil {
			log.Printf("workspace %s: moving legacy state: %v", w.Name(), err)
		}
	}
	s.hub = connect.NewHub(v, s.stateDir)
	s.hub.Elevate = s.elevation
	s.hub.Browser = func() string { return s.cfg.Settings().VPN.Browser }
	s.hub.CodeSources = func() ([]vpn.CodeSource, error) { return codeSources(s.cfg.Settings().VPN.Codes, s.providerKey) }
	s.docker = docker.New(s.hub)
	s.kube = kube.New(s.hub, v)
	s.db = dbx.New(s.hub, v)
	s.git = gitx.New(s.hub)
	s.git.Identity = func() (string, string) { g := s.cfg.Settings().Git; return g.Name, g.Email }
	s.git.Connections = s.cfg.GitConnections
	s.git.Vault = v
	s.dns = dnsx.New(v)
	s.dns.Connections = s.cfg.DNSConnections
	s.ansible = ansible.New(s.hub)
	s.ansible.Touch = v.Touch
	s.ansible.Redact = agent.NewRedactor(func() map[string]string {
		out := v.SecretValues()
		for k, label := range s.ansible.Secrets() {
			out[k] = label
		}
		return out
	}).Redact
	s.mcp = mcpx.New(v)
	s.mcp.Trusted = func(ws string, c workspace.MCPServer) bool { return cfg.MCPTrusted(mcpx.Hash(ws, c)) }
	s.forwards = connect.NewForwards()
	s.agents = agent.NewManager(agent.Deps{
		Workspaces: s.ws, Hub: s.hub, Docker: s.docker, Kube: s.kube, DB: s.db, Git: s.git, DNS: s.dns, Ansible: s.ansible, Vault: v, MCP: s.mcp, Audit: al,
		Router:   s.router,
		StateDir: s.stateDir,
		DefaultModel: func(w *workspace.Workspace) string {
			// the workspace's default, the app's, else the first model —
			// one the workspace allows when there is one
			st := cfg.Settings()
			ids := []string{w.Settings.DefaultModel, st.DefaultModel}
			for _, m := range llm.ListModels(st.Providers) {
				ids = append(ids, m.ID)
			}
			first := ""
			for _, id := range ids {
				if id == "" {
					continue
				}
				if agent.ModelAllowed(w, id) {
					return id
				}
				if first == "" {
					first = id
				}
			}
			return first
		},
		UserScripts: dirs.Scripts(),
		Local:       s.localPolicy,
		Policy:      func() agent.Policy { return agent.Policy{AutoServerChanges: s.cfg.Settings().Agent.AutoServerChanges} },
		Web:         s.webConfig,
		Hooks:       s.agentHooks,
		Subagents: func() agent.SubagentConfig {
			st := s.cfg.Settings()
			var models []string
			for _, m := range llm.ListModels(st.Providers) {
				if p, _, ok := s.router().Lookup(m.ID); ok && (p.Type == "anthropic" || p.Type == "openai") {
					models = append(models, m.ID)
				}
			}
			return agent.SubagentConfig{Model: st.Agent.SubagentModel, Disabled: st.Agent.NoSubagents, Models: models}
		},
	})
	v.OnLock(func() {
		// Locking ends all live access: agents, MCP servers, terminals and
		// SSH connections, networks and port forwards.
		s.agents.InterruptAll()
		s.ansible.ForgetAll()
		s.mcp.CloseAll()
		s.forwards.StopWorkspace("")
		s.hub.CloseAll()
		s.audit.Record(audit.Entry{Actor: "system", Action: "vault.lock"})
	})
	kind := update.KindCLI
	if opts.Desktop {
		kind = update.KindDesktop
	}
	s.updater = update.New(opts.Version, kind, opts.Quit)
	s.agents.SetMCPBase("http://127.0.0.1:" + strconv.Itoa(opts.Port) + "/mcp/")
	if s.token, err = loadToken(filepath.Join(dirs.State, "server.token")); err != nil {
		return nil, err
	}
	return s, nil
}

// elevation is the command prefix for system-mode VPN clients.
func (s *Server) elevation() []string { return vpn.Elevation(s.cfg.Settings().VPN.Elevate) }

// localPolicy is the agent's access to this computer (nil when turned off).
// opsdeck's vault, state (login token, audit, sessions), settings and the
// user's script library (changed only through the script tool, so the
// agent's edits stay drafts) are always out of reach of the file tools.
func (s *Server) localPolicy() *agent.LocalPolicy {
	la := s.cfg.Settings().LocalAccess
	if la.Disabled {
		return nil
	}
	home, _ := os.UserHomeDir()
	vf, cf := s.dirs.VaultFile(), s.dirs.ConfigFile()
	return &agent.LocalPolicy{
		Home:             home,
		AllowedDirs:      la.AllowedDirs,
		AutoReadCommands: la.AutoReadCommands,
		Forbidden: []string{vf, vf + "-wal", vf + "-shm", vf + "-journal", cf, cf + ".tmp", s.dirs.State,
			filepath.Join(s.dirs.Data, "vault.db.tmp"), s.dirs.Scripts()},
	}
}

// stateDir is a workspace's local-only state folder (<workspace>/.opsdeck).
func (s *Server) stateDir(ws string) string {
	if w, err := s.ws.Get(ws); err == nil {
		return w.StateDir()
	}
	return s.dirs.WorkspaceState(ws)
}

func (s *Server) router() *llm.Router {
	return llm.NewRouter(s.cfg.Settings().Providers, s.providerKey)
}

// providerKey resolves a model provider's API key reference.
func (s *Server) providerKey(ref string) (string, error) {
	it, field, err := s.vault.ResolveFor("", ref) // provider keys live in the global scope
	if err != nil {
		return "", err
	}
	if field == "" {
		for _, f := range []string{"token", "password"} {
			if v := it.Get(f); v != "" {
				return v, nil
			}
		}
		return "", errors.New("the referenced vault item has no token")
	}
	return it.Get(field), nil
}

func loadToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	return tok, os.WriteFile(path, []byte(tok), 0o600)
}

// URL is the login link printed at startup.
func (s *Server) URL() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.port) + "/#token=" + s.token
}

// Close shuts everything down.
func (s *Server) Close() {
	s.agents.InterruptAll()
	s.forwards.StopWorkspace("")
	s.mcp.CloseAll()
	s.hub.CloseAll()
	s.vault.Close()
	s.audit.Close()
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	if s.desktop {
		s.desktopRoutes(mux)
	}
	dist, _ := fs.Sub(distFS, "dist")
	static := http.FileServer(http.FS(dist))
	// MCP bridge for external agents (bearer token per running session).
	mux.Handle("/mcp/", s.agents.MCPHandler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if r.URL.Path != "/" {
			if f, err := dist.Open(strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				f.Close()
				if strings.HasPrefix(r.URL.Path, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				static.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback
		b, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "UI not built: run `npm run build` in web/", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	})
	return s.guard(mux)
}

// guard enforces: loopback Host header (anti DNS-rebinding), same-origin
// requests, and the session cookie for /api.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "[::1]" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !s.originOK(origin, r.Host) {
			writeErr(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		if r.URL.Path == "/api/auth/login" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.authed(r) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originOK(origin, host string) bool {
	if origin == "http://"+host || (s.devOrigin != "" && origin == s.devOrigin) {
		return true
	}
	return false
}

const cookieName = "opsdeck_session"

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1 {
		return true
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), []byte(s.token)) == 1
	}
	return false
}

// ---------------------------------------------------------------------------
// helpers

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

// fail maps errors to HTTP statuses.
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vault.ErrLocked):
		writeJSON(w, http.StatusLocked, apiError{Error: err.Error(), Code: "vault_locked"})
	case errors.Is(err, vault.ErrNotInitialized):
		writeJSON(w, http.StatusLocked, apiError{Error: err.Error(), Code: "vault_uninitialized"})
	case errors.Is(err, vault.ErrBadPassword):
		writeJSON(w, http.StatusUnauthorized, apiError{Error: err.Error(), Code: "bad_password"})
	case errors.Is(err, vault.ErrNotFound), errors.Is(err, os.ErrNotExist), strings.Contains(err.Error(), "not found"):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, vault.ErrConflict), strings.Contains(err.Error(), "already exists"):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, agent.ErrBusy):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

// unlocked wraps handlers that need an unlocked vault.
func (s *Server) unlocked(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.vault.Initialized() {
			fail(w, vault.ErrNotInitialized)
			return
		}
		if !s.vault.Unlocked() {
			fail(w, vault.ErrLocked)
			return
		}
		s.vault.Touch()
		h(w, r)
	}
}

func (s *Server) workspace(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, bool) {
	ws, err := s.ws.Get(r.PathValue("ws"))
	if err != nil {
		fail(w, err)
		return nil, false
	}
	return ws, true
}

func (s *Server) record(r *http.Request, ws, action, target string, detail map[string]any, err error) {
	e := audit.Entry{Workspace: ws, Actor: "user", Action: action, Target: target, Detail: detail}
	if err != nil {
		e.Result = "error"
		if e.Detail == nil {
			e.Detail = map[string]any{}
		}
		e.Detail["error"] = err.Error()
	}
	s.audit.Record(e)
}

// Serve listens on the port.
func (s *Server) Serve(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("opsdeck listening on %s", addr)
	return srv.ListenAndServe()
}
