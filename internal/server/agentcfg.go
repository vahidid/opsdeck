package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"opsdeck/internal/agent"
	"opsdeck/internal/config"
	"opsdeck/internal/workspace"
)

// webConfig is the agent's web access from App settings.
func (s *Server) webConfig() agent.WebConfig {
	wc := s.cfg.Settings().Web
	return agent.WebConfig{Access: wc.Access, Search: wc.Search, SearchURL: wc.SearchURL, Proxy: wc.Proxy,
		Domains: wc.Domains, NoDefaultDomains: wc.NoDefaultDomains,
		Key: func() (string, error) {
			if wc.KeyRef == "" {
				return "", nil
			}
			return s.providerKey(wc.KeyRef)
		}}
}

func validAccess(level string) bool {
	switch level {
	case "", agent.WebOff, agent.WebSearch, agent.WebFetch:
		return true
	}
	return false
}

// checkWeb validates App settings → Web access.
func checkWeb(wc *config.Web) error {
	if !validAccess(wc.Access) {
		return errors.New("web.access must be off, search or fetch")
	}
	switch wc.Search {
	case "", "duckduckgo":
	case "searxng":
		u, err := url.Parse(strings.TrimSpace(wc.SearchURL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("SearXNG needs the address of its instance, e.g. https://searx.example.org")
		}
		wc.SearchURL = strings.TrimRight(u.String(), "/")
	case "brave", "tavily":
		if wc.KeyRef == "" {
			return fmt.Errorf("%s needs an API key from the vault", wc.Search)
		}
	default:
		return errors.New("web.search must be duckduckgo, searxng, brave or tavily")
	}
	if wc.KeyRef != "" && !strings.HasPrefix(wc.KeyRef, "vault://") {
		return errors.New("the search API key must be a vault:// reference")
	}
	if p := strings.TrimSpace(wc.Proxy); p != "" {
		u, err := url.Parse(p)
		if err != nil || u.Host == "" {
			return fmt.Errorf("invalid web proxy %q (socks5://host:port or http://host:port)", p)
		}
		switch u.Scheme {
		case "socks5", "socks5h", "http", "https":
		default:
			return fmt.Errorf("web proxy scheme must be socks5 or http, not %q", u.Scheme)
		}
		wc.Proxy = p
	}
	var domains []string
	for _, d := range wc.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
		d = strings.TrimSuffix(d, "/")
		if d == "" {
			continue
		}
		if strings.ContainsAny(d, "/ ") {
			return fmt.Errorf("allowed web domain %q must be a domain name like docs.example.com", d)
		}
		domains = append(domains, d)
	}
	wc.Domains = domains
	return nil
}

// checkHooks validates hooks (App settings or a workspace).
func checkHooks(event, name, command, risk string, timeout int) error {
	switch event {
	case agent.HookBeforeTool, agent.HookAfterTool, agent.HookTurnEnd:
	default:
		return fmt.Errorf("hook %q: event must be before_tool, after_tool or turn_end", name)
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("hook %q: the command is empty", name)
	}
	switch risk {
	case "", agent.RiskRead, agent.RiskChange, agent.RiskDestructive:
	default:
		return fmt.Errorf("hook %q: risk must be read, change or destructive", name)
	}
	if timeout < 0 || timeout > 600 {
		return fmt.Errorf("hook %q: timeout must be between 0 and 600 seconds", name)
	}
	return nil
}

// checkAgent validates App settings → Agent.
func (s *Server) checkAgent(a *config.Agent) error {
	for i := range a.Hooks {
		h := &a.Hooks[i]
		h.Name = strings.TrimSpace(h.Name)
		if h.Name == "" {
			h.Name = fmt.Sprintf("hook %d", i+1)
		}
		if err := checkHooks(h.Event, h.Name, h.Command, h.Risk, h.TimeoutSec); err != nil {
			return err
		}
	}
	if m := strings.TrimSpace(a.SubagentModel); m != "" {
		if _, _, ok := s.router().Lookup(m); !ok {
			return fmt.Errorf("subagent model %q is not a configured model", m)
		}
	}
	return nil
}

// checkWorkspaceAgent validates a workspace's agent settings and hooks.
func checkWorkspaceAgent(ws *workspace.Workspace, st *workspace.Settings, hooks []workspace.Hook) error {
	if !validAccess(st.Web) {
		return errors.New("web access must be off, search or fetch (empty = the app default)")
	}
	if v := strings.TrimSpace(st.WebVia); v != "" {
		r := ws.FindRef(v)
		if r == nil || !workspace.IsNetworkKind(r.Kind) {
			return fmt.Errorf("web network %q must be one of the workspace's networks (network/<name>)", v)
		}
		st.WebVia = v
	}
	var models []string
	for _, m := range st.Models {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		if !strings.Contains(m, "/") {
			return fmt.Errorf("allowed model %q must be <provider>/<model> (patterns like ollama/* work)", m)
		}
		models = append(models, m)
	}
	st.Models = models
	for i, h := range hooks {
		name := h.Name
		if name == "" {
			name = fmt.Sprintf("hook %d", i+1)
		}
		if err := checkHooks(h.Event, name, h.Command, h.Risk, h.TimeoutSec); err != nil {
			return err
		}
	}
	return nil
}

// agentHooks are the hooks that apply to a workspace: App settings hooks
// for all workspaces or this one, and the workspace's own hooks the user
// trusted on this machine.
func (s *Server) agentHooks(w *workspace.Workspace) []agent.Hook {
	var out []agent.Hook
	for _, h := range s.cfg.Settings().Agent.Hooks {
		if h.Disabled || (h.Workspace != "" && h.Workspace != w.Name()) {
			continue
		}
		out = append(out, agent.Hook{Name: h.Name, Event: h.Event, Command: h.Command, Tool: h.Tool, Risk: h.Risk,
			Environment: h.Environment, Timeout: time.Duration(h.TimeoutSec) * time.Second})
	}
	for _, h := range w.Hooks {
		if h.Disabled || !s.cfg.MCPTrusted(agent.HookHash(w.Name(), h)) {
			continue
		}
		out = append(out, agent.Hook{Name: h.Name, Event: h.Event, Command: h.Command, Tool: h.Tool, Risk: h.Risk,
			Environment: h.Environment, Timeout: time.Duration(h.TimeoutSec) * time.Second, FromWorkspace: true})
	}
	return out
}

// fullWS is a workspace with the trust state of its hooks.
func (s *Server) fullWS(w *workspace.Workspace) wsFull {
	f := full(w)
	for i, h := range w.Hooks {
		if !s.cfg.MCPTrusted(agent.HookHash(w.Name(), h)) {
			f.UntrustedHooks = append(f.UntrustedHooks, i)
		}
	}
	return f
}

// hooksTrust trusts the workspace's hooks as they are now.
func (s *Server) hooksTrust(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var hashes []string
	for _, h := range ws.Hooks {
		hashes = append(hashes, agent.HookHash(ws.Name(), h))
	}
	err := s.cfg.TrustMCP(hashes...)
	s.record(r, ws.Name(), "hooks.trust", "", map[string]any{"hooks": len(hashes)}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, s.fullWS(ws))
}
