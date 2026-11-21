package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/agent"
	"opsdeck/internal/ansible"
	"opsdeck/internal/config"
	"opsdeck/internal/connect"
	"opsdeck/internal/extagent"
	"opsdeck/internal/llm"
	"opsdeck/internal/vpn"
	"opsdeck/internal/workspace"
)

func (s *Server) routes(mux *http.ServeMux) {
	u := s.unlocked
	s.accessKeyRoutes(mux)
	s.waitingRoutes(mux, u)
	// auth
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/auth/check", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })

	// vault
	s.vaultRoutes(mux, u)

	// settings
	mux.HandleFunc("GET /api/settings", u(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.cfg.Settings()) }))
	mux.HandleFunc("PUT /api/settings", u(s.settingsPut))
	mux.HandleFunc("GET /api/update", s.updateStatus)
	mux.HandleFunc("POST /api/update/install", s.updateInstall)
	mux.HandleFunc("GET /api/agents/detect", u(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		writeJSON(w, 200, extagent.Detect(ctx))
	}))
	mux.HandleFunc("POST /api/providers/models", u(s.providerModels))
	mux.HandleFunc("GET /api/providers/limits", u(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.router().Limits(r.Context()))
	}))
	mux.HandleFunc("GET /api/models", u(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, llm.ListModels(s.cfg.Settings().Providers))
	}))

	// workspaces
	s.workspaceRoutes(mux, u)

	// networks & hosts
	mux.HandleFunc("GET /api/workspaces/{ws}/networks", u(s.netList))
	mux.HandleFunc("POST /api/workspaces/{ws}/networks/{name}/up", u(s.netUp))
	mux.HandleFunc("POST /api/workspaces/{ws}/networks/{name}/down", u(s.netDown))
	mux.HandleFunc("GET /api/workspaces/{ws}/networks/{name}/log", u(s.netLog))
	mux.HandleFunc("POST /api/workspaces/{ws}/networks/{name}/code", u(s.netCode))
	mux.HandleFunc("GET /api/vpn/drivers", s.vpnDrivers)
	mux.HandleFunc("GET /api/vpn/install", s.vpnInstallStatus)
	mux.HandleFunc("POST /api/vpn/install", s.vpnInstallStart)
	// polled by every page: does not count as activity for the auto-lock
	mux.HandleFunc("GET /api/vpn/codes", s.vpnCodes)
	mux.HandleFunc("POST /api/vpn/codes/test", u(s.vpnCodesTest))
	// hosts
	s.hostRoutes(mux, u)

	// forwards & panels
	mux.HandleFunc("POST /api/workspaces/{ws}/forwards", u(s.forwardCreate))
	mux.HandleFunc("GET /api/forwards", u(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.forwards.List()) }))
	mux.HandleFunc("DELETE /api/forwards/{id}", u(func(w http.ResponseWriter, r *http.Request) {
		if !s.forwards.Stop(r.PathValue("id")) {
			writeErr(w, 404, "forward not found")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/workspaces/{ws}/panels/{name}/open", u(s.panelOpen))

	// docker
	s.dockerRoutes(mux, u)

	// kubernetes
	s.kubeRoutes(mux, u)
	s.dbRoutes(mux, u)

	// logs
	mux.HandleFunc("GET /api/workspaces/{ws}/logs/stream", u(s.logStream))

	// agent
	mux.HandleFunc("GET /api/workspaces/{ws}/sessions", u(s.sessList))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions", u(s.sessCreate))
	mux.HandleFunc("GET /api/workspaces/{ws}/sessions/{id}", u(s.sessGet))
	mux.HandleFunc("PUT /api/workspaces/{ws}/sessions/{id}", u(s.sessPut))
	mux.HandleFunc("DELETE /api/workspaces/{ws}/sessions/{id}", u(s.sessDelete))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/messages", u(s.sessMessage))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/interrupt", u(s.sessInterrupt))
	mux.HandleFunc("DELETE /api/workspaces/{ws}/sessions/{id}/pending/{pid}", u(s.sessPendingCancel))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/approvals/{aid}", u(s.sessApproval))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/questions/{qid}", u(s.sessAnswer))
	mux.HandleFunc("GET /api/workspaces/{ws}/sessions/{id}/tasks", u(s.sessTasks))
	mux.HandleFunc("GET /api/workspaces/{ws}/sessions/{id}/tasks/{tid}", u(s.sessTask))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/tasks/{tid}/stop", u(s.sessTaskStop))
	mux.HandleFunc("GET /api/workspaces/{ws}/sessions/{id}/outputs/{oid}", u(s.sessOutput))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/attachments", u(s.sessAttach))
	mux.HandleFunc("GET /api/workspaces/{ws}/sessions/{id}/attachments/{aid}", u(s.sessAttachment))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/fork", u(s.sessFork))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/export", u(s.sessExport))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/shell", u(s.sessShell))
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/compact", u(s.sessCompact))
	mux.HandleFunc("GET /api/workspaces/{ws}/harness", u(s.wsHarness))
	mux.HandleFunc("GET /api/workspaces/{ws}/sessions/{id}/stream", u(s.sessStream))

	// git repositories
	s.gitRoutes(mux, u)
	s.connectorRoutes(mux, u)
	s.dnsRoutes(mux, u)
	s.deployRoutes(mux, u)
	s.acmeRoutes(mux, u)
	s.topologyRoutes(mux, u)
	s.scriptRoutes(mux, u)
	s.ansibleRoutes(mux, u)

	// audit
	mux.HandleFunc("GET /api/audit", u(s.auditList))
	mux.HandleFunc("GET /api/audit/verify", u(func(w http.ResponseWriter, r *http.Request) {
		ok, n, broken := s.audit.Verify()
		writeJSON(w, 200, map[string]any{"ok": ok, "entries": n, "brokenAt": broken})
	}))
}

// ---------------------------------------------------------------------------
// auth & vault

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if len(in.Token) == 0 || subtle.ConstantTimeCompare([]byte(in.Token), []byte(s.token)) != 1 {
		writeErr(w, 401, "invalid token")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 400 * 24 * 3600})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) status() map[string]any {
	return map[string]any{"initialized": s.vault.Initialized(), "unlocked": s.vault.Unlocked(), "autoLockMinutes": int(s.vault.AutoLock().Minutes())}
}

func (s *Server) providerModels(w http.ResponseWriter, r *http.Request) {
	var p config.Provider
	if err := readJSON(r, &p); err != nil {
		fail(w, err)
		return
	}
	if extagent.IsKind(p.Type) {
		writeJSON(w, 200, extagent.ListModels(r.Context(), extagent.Spec{Kind: p.Type, Command: p.Command, Args: p.Args, Env: p.Env}))
		return
	}
	if p.Type != "anthropic" && p.Type != "openai" {
		fail(w, fmt.Errorf("unknown provider type %q", p.Type))
		return
	}
	out := extagent.ModelList{Models: []extagent.Model{}, Source: "api"}
	key := ""
	if p.APIKeyRef != "" {
		k, err := s.providerKey(p.APIKeyRef)
		if err != nil {
			out.Error = "API key: " + err.Error()
			writeJSON(w, 200, out)
			return
		}
		key = k
	}
	models, err := llm.FetchModels(r.Context(), p, key)
	if err != nil {
		out.Error = err.Error()
	}
	for _, m := range models {
		out.Models = append(out.Models, extagent.Model{ID: m.ID, Name: m.Name, Description: m.Description})
	}
	writeJSON(w, 200, out)
}

func (s *Server) settingsPut(w http.ResponseWriter, r *http.Request) {
	var in config.Settings
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	seen := map[string]bool{}
	for i, p := range in.Providers {
		p.ID = strings.TrimSpace(p.ID)
		if p.ID == "" || strings.Contains(p.ID, "/") {
			fail(w, fmt.Errorf("provider %d: id is required and must not contain '/'", i+1))
			return
		}
		if seen[p.ID] {
			fail(w, fmt.Errorf("duplicate provider id %q", p.ID))
			return
		}
		seen[p.ID] = true
		if p.Type != "anthropic" && p.Type != "openai" && !extagent.IsKind(p.Type) {
			fail(w, fmt.Errorf("provider %s: type must be anthropic, openai, claude-code, codex, gemini-cli, antigravity or acp", p.ID))
			return
		}
		if p.Type == extagent.ACP && p.Command == "" {
			fail(w, fmt.Errorf("provider %s: an ACP agent needs a command", p.ID))
			return
		}
		if extagent.IsKind(p.Type) && len(p.Models) == 0 {
			p.Models = []string{"default"}
		}
		for m := range p.ModelNames {
			if !slices.Contains(p.Models, m) {
				delete(p.ModelNames, m)
			}
		}
		if p.APIKeyRef != "" && !strings.HasPrefix(p.APIKeyRef, "vault://") {
			fail(w, fmt.Errorf("provider %s: the API key must be a vault:// reference", p.ID))
			return
		}
		in.Providers[i] = p
	}
	if in.AutoLockMinutes < 0 {
		in.AutoLockMinutes = 0
	}
	home, _ := os.UserHomeDir()
	dirs := []string{}
	for _, d := range in.LocalAccess.AllowedDirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if d == "~" || strings.HasPrefix(d, "~/") || strings.HasPrefix(d, `~\`) {
			d = filepath.Join(home, d[1:])
		}
		if !filepath.IsAbs(d) {
			fail(w, fmt.Errorf("allowed folder %q must be an absolute path or start with ~/", d))
			return
		}
		dirs = append(dirs, filepath.Clean(d))
	}
	in.LocalAccess.AllowedDirs = dirs
	switch in.VPN.Elevate {
	case "", "auto", "sudo", "pkexec", "none":
	default:
		fail(w, fmt.Errorf("vpn.elevate must be auto, sudo, pkexec or none"))
		return
	}
	if err := checkVPNCodes(&in.VPN.Codes); err != nil {
		fail(w, err)
		return
	}
	if err := checkWeb(&in.Web); err != nil {
		fail(w, err)
		return
	}
	if err := s.checkAgent(&in.Agent); err != nil {
		fail(w, err)
		return
	}
	if err := s.cfg.SetSettings(in); err != nil {
		fail(w, err)
		return
	}
	s.vault.SetAutoLock(time.Duration(in.AutoLockMinutes) * time.Minute)
	s.record(r, "", "settings.update", "", nil, nil)
	writeJSON(w, 200, s.cfg.Settings())
}

// ---------------------------------------------------------------------------
// networks & hosts

func (s *Server) netList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.hub.Networks(ws))
}

func (s *Server) netUp(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	// Wait a while; slow connects (and logins) continue in the background
	// and show up as "connecting"/"auth" in the status.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	err := s.hub.NetworkUp(ctx, ws, r.PathValue("name"))
	if errors.Is(err, connect.ErrConnecting) {
		err = nil
	}
	s.record(r, ws.Name(), "network.up", "network/"+r.PathValue("name"), nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, s.hub.Networks(ws))
}

func (s *Server) netLog(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	writeJSON(w, 200, s.hub.NetworkLog(ws.Name(), r.PathValue("name"), n))
}

func (s *Server) vpnDrivers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"drivers":    vpn.Drivers(s.elevation()),
		"privileged": vpn.Privileged(),
		"elevate":    strings.Join(s.elevation(), " "),
		"browser":    vpn.FindBrowser(s.cfg.Settings().VPN.Browser),
		"os":         runtime.GOOS,
	})
}

func (s *Server) netDown(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	s.hub.NetworkDown(ws.Name(), r.PathValue("name"))
	s.record(r, ws.Name(), "network.down", "network/"+r.PathValue("name"), nil, nil)
	writeJSON(w, 200, s.hub.Networks(ws))
}

// ---------------------------------------------------------------------------
// forwards & panels

func (s *Server) forwardCreate(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Target     string
		RemoteHost string
		RemotePort int
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	res := ws.FindTarget(in.Target)
	if res == nil {
		writeErr(w, 404, "target not found")
		return
	}
	var dial connect.DialFunc
	remote := ""
	switch res.Kind {
	case "Host":
		// Connect from the host itself, so services bound to its loopback work.
		client, err := s.hub.SSH(r.Context(), ws, res)
		if err != nil {
			fail(w, err)
			return
		}
		dial = client.DialContext
		host := in.RemoteHost
		if host == "" {
			host = "127.0.0.1"
		}
		remote = net.JoinHostPort(host, strconv.Itoa(in.RemotePort))
	default:
		d, err := s.hub.Dialer(r.Context(), ws, res)
		if err != nil {
			fail(w, err)
			return
		}
		dial = d
		host := in.RemoteHost
		if host == "" {
			host = res.Str("address")
		}
		remote = net.JoinHostPort(host, strconv.Itoa(in.RemotePort))
	}
	if in.RemotePort <= 0 {
		fail(w, errors.New("remotePort is required"))
		return
	}
	fw, err := s.forwards.Start(ws.Name(), res.ID(), remote, "", dial)
	s.record(r, ws.Name(), "forward.create", res.ID(), map[string]any{"remote": remote}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, fw)
}

func (s *Server) panelOpen(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var p *workspace.Resource
	for _, kind := range []string{"Panel", "Prometheus", "Loki", "Elasticsearch"} {
		if p = ws.Find(kind, r.PathValue("name")); p != nil {
			break
		}
	}
	if p == nil {
		writeErr(w, 404, "panel not found")
		return
	}
	raw := p.Str("url")
	if p.Via == "" {
		writeJSON(w, 200, map[string]string{"url": raw})
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		fail(w, fmt.Errorf("invalid panel URL %q", raw))
		return
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	dial, err := s.hub.Dialer(r.Context(), ws, p)
	if err != nil {
		fail(w, err)
		return
	}
	fw, err := s.forwards.Start(ws.Name(), p.ID(), net.JoinHostPort(u.Hostname(), port), u.Scheme, dial)
	if err != nil {
		fail(w, err)
		return
	}
	local := *u
	local.Host = "127.0.0.1:" + strconv.Itoa(fw.LocalPort)
	s.record(r, ws.Name(), "panel.open", p.ID(), nil, nil)
	writeJSON(w, 200, map[string]string{"url": local.String()})
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// agent sessions

func (s *Server) sessList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.agents.List(ws.Name()))
}

func (s *Server) sessCreate(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Model, Mode string
		Headless    bool
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.Model != "" && !agent.ModelAllowed(ws, in.Model) {
		fail(w, fmt.Errorf("this workspace only allows the models %s", strings.Join(ws.Settings.Models, ", ")))
		return
	}
	sess, err := s.agents.Create(ws, in.Model, in.Mode)
	if err != nil {
		fail(w, err)
		return
	}
	if in.Headless {
		sess.SetHeadless(true)
	}
	writeJSON(w, 201, sess.Info())
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) (*agent.Session, bool) {
	if _, ok := s.workspace(w, r); !ok {
		return nil, false
	}
	sess, err := s.agents.Get(r.PathValue("ws"), r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return nil, false
	}
	return sess, true
}

func (s *Server) sessGet(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	events := sess.Events()
	if events == nil {
		events = []agent.Event{}
	}
	// seq: how recent the snapshot is (the last event id when it was taken)
	info, seq := sess.InfoSeq()
	writeJSON(w, 200, map[string]any{"session": info, "seq": seq, "events": events, "pending": sess.Pending()})
}

func (s *Server) sessPut(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var in struct {
		Title, Model, Mode string
		Headless           *bool
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.Headless != nil {
		sess.SetHeadless(*in.Headless)
	}
	if ws, err := s.ws.Get(r.PathValue("ws")); err == nil && in.Model != "" && !agent.ModelAllowed(ws, in.Model) {
		fail(w, fmt.Errorf("this workspace only allows the models %s", strings.Join(ws.Settings.Models, ", ")))
		return
	}
	if err := sess.Update(in.Title, in.Model, in.Mode); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, sess.Info())
}

func (s *Server) sessDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.workspace(w, r); !ok {
		return
	}
	if err := s.agents.Delete(r.PathValue("ws"), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) sessMessage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.workspace(w, r); !ok {
		return
	}
	var in struct {
		Text        string
		Attachments []string
		Mode        string // while a turn runs: queue | steer ("" = refuse)
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	p, queued, err := s.agents.SendMode(r.PathValue("ws"), r.PathValue("id"), in.Text, in.Attachments, in.Mode)
	if err != nil {
		fail(w, err)
		return
	}
	if queued {
		writeJSON(w, 202, map[string]any{"ok": true, "pending": p})
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (s *Server) sessPendingCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.workspace(w, r); !ok {
		return
	}
	if err := s.agents.CancelPending(r.PathValue("ws"), r.PathValue("id"), r.PathValue("pid")); err != nil {
		if errors.Is(err, agent.ErrNoSuchPending) {
			writeErr(w, 404, err.Error())
			return
		}
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) sessInterrupt(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	sess.Interrupt()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) sessApproval(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var in struct {
		Decision, Note, Secret string
		Canary                 bool
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := sess.Resolve(r.PathValue("aid"), in.Decision, in.Note, in.Secret, in.Canary); err != nil {
		if errors.Is(err, agent.ErrSecretRequired) || errors.Is(err, ansible.ErrWrongPassword) {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error(), Code: "bad_secret"})
			return
		}
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) sessAnswer(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var in agent.Answer
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := sess.Answer(r.PathValue("qid"), in); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) sessStream(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	ch, unsub := sess.Subscribe()
	defer unsub()
	sseHeaders(w)
	info, _ := json.Marshal(map[string]any{"type": "status", "state": sess.Info().State, "usage": sess.Info().Usage})
	fmt.Fprintf(w, "data: %s\n\n", info)
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
}

// ---------------------------------------------------------------------------
// audit

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	writeJSON(w, 200, s.audit.Query(q.Get("workspace"), before, limit))
}

// updateStatus reports the running and the latest version; it checks GitHub
// at most hourly (refresh=1: now), and not on its own when that is off.
func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1"
	check := refresh || !s.cfg.Settings().Updates.NoCheck
	writeJSON(w, 200, s.updater.Status(r.Context(), check, refresh))
}

func (s *Server) updateInstall(w http.ResponseWriter, r *http.Request) {
	err := s.updater.Install()
	s.record(r, "", "app.update", "", nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, s.updater.Status(r.Context(), false, false))
}
