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
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/agent"
	"opsdeck/internal/config"
	"opsdeck/internal/connect"
	"opsdeck/internal/extagent"
	"opsdeck/internal/kube"
	"opsdeck/internal/llm"
	"opsdeck/internal/mcpx"
	"opsdeck/internal/vault"
	"opsdeck/internal/vpn"
	"opsdeck/internal/workspace"
)

func (s *Server) routes(mux *http.ServeMux) {
	u := s.unlocked
	// auth
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/auth/check", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })

	// vault
	mux.HandleFunc("GET /api/vault/status", s.vaultStatus)
	mux.HandleFunc("POST /api/vault/init", s.vaultInit)
	mux.HandleFunc("POST /api/vault/unlock", s.vaultUnlock)
	mux.HandleFunc("POST /api/vault/lock", s.vaultLock)
	mux.HandleFunc("POST /api/vault/password", u(s.vaultPassword))
	mux.HandleFunc("GET /api/vault/types", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, vault.Types) })
	mux.HandleFunc("GET /api/vault/items", u(s.itemsList))
	mux.HandleFunc("POST /api/vault/items", u(s.itemCreate))
	mux.HandleFunc("PUT /api/vault/items/{id}", u(s.itemUpdate))
	mux.HandleFunc("DELETE /api/vault/items/{id}", u(s.itemDelete))
	mux.HandleFunc("POST /api/vault/items/{id}/reveal", u(s.itemReveal))
	mux.HandleFunc("GET /api/vault/items/{id}/totp", u(s.itemTOTP))
	mux.HandleFunc("POST /api/vault/ssh/generate", u(s.sshGenerate))
	mux.HandleFunc("POST /api/vault/ssh/import", u(s.sshImport))
	mux.HandleFunc("POST /api/vault/password-generator", s.passwordGen)

	// settings
	mux.HandleFunc("GET /api/settings", u(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.cfg.Settings()) }))
	mux.HandleFunc("PUT /api/settings", u(s.settingsPut))
	mux.HandleFunc("GET /api/agents/detect", u(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		writeJSON(w, 200, extagent.Detect(ctx))
	}))
	mux.HandleFunc("GET /api/models", u(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, llm.ListModels(s.cfg.Settings().Providers))
	}))

	// workspaces
	mux.HandleFunc("GET /api/kinds", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, workspace.Kinds) })
	mux.HandleFunc("GET /api/workspaces", u(s.wsList))
	mux.HandleFunc("POST /api/workspaces", u(s.wsCreate))
	mux.HandleFunc("POST /api/workspaces/import", u(s.wsImport))
	mux.HandleFunc("GET /api/workspaces/{ws}", u(s.wsGet))
	mux.HandleFunc("PUT /api/workspaces/{ws}", u(s.wsPut))
	mux.HandleFunc("DELETE /api/workspaces/{ws}", u(s.wsDelete))
	mux.HandleFunc("GET /api/workspaces/{ws}/resources", u(s.resList))
	mux.HandleFunc("POST /api/workspaces/{ws}/resources", u(s.resCreate))
	mux.HandleFunc("PUT /api/workspaces/{ws}/resources/{kind}/{name}", u(s.resUpdate))
	mux.HandleFunc("DELETE /api/workspaces/{ws}/resources/{kind}/{name}", u(s.resDelete))
	mux.HandleFunc("GET /api/workspaces/{ws}/validate", u(s.wsValidate))
	mux.HandleFunc("GET /api/workspaces/{ws}/topology", u(s.wsTopology))
	mux.HandleFunc("GET /api/workspaces/{ws}/resolve/{kind}/{name}", u(s.wsResolve))
	mux.HandleFunc("GET /api/workspaces/{ws}/docs", u(s.docsList))
	mux.HandleFunc("GET /api/workspaces/{ws}/docs/file", u(s.docGet))
	mux.HandleFunc("PUT /api/workspaces/{ws}/docs/file", u(s.docPut))
	mux.HandleFunc("DELETE /api/workspaces/{ws}/docs/file", u(s.docDelete))
	mux.HandleFunc("GET /api/workspaces/{ws}/artifacts", u(s.artifactsList))
	mux.HandleFunc("GET /api/workspaces/{ws}/artifacts/file", u(s.artifactGet))
	mux.HandleFunc("DELETE /api/workspaces/{ws}/artifacts/file", u(s.artifactDelete))
	mux.HandleFunc("GET /api/workspaces/{ws}/memory", u(s.memoryGet))
	mux.HandleFunc("PUT /api/workspaces/{ws}/memory", u(s.memoryPut))
	mux.HandleFunc("GET /api/workspaces/{ws}/skills", u(s.skillsList))
	mux.HandleFunc("POST /api/workspaces/{ws}/skills/{name}/copy", u(s.skillCopy))
	mux.HandleFunc("GET /api/skills/library", u(s.skillLibrary))
	mux.HandleFunc("GET /api/skills/library/{name}", u(s.skillLibraryFile))
	mux.HandleFunc("GET /api/workspaces/{ws}/mcp", u(s.mcpStatus))
	mux.HandleFunc("POST /api/workspaces/{ws}/mcp/{name}/trust", u(s.mcpTrust))

	// networks & hosts
	mux.HandleFunc("GET /api/workspaces/{ws}/networks", u(s.netList))
	mux.HandleFunc("POST /api/workspaces/{ws}/networks/{name}/up", u(s.netUp))
	mux.HandleFunc("POST /api/workspaces/{ws}/networks/{name}/down", u(s.netDown))
	mux.HandleFunc("GET /api/workspaces/{ws}/networks/{name}/log", u(s.netLog))
	mux.HandleFunc("GET /api/vpn/drivers", s.vpnDrivers)
	mux.HandleFunc("POST /api/workspaces/{ws}/hosts/{host}/check", u(s.hostCheck))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/stats", u(s.hostStats))
	mux.HandleFunc("POST /api/workspaces/{ws}/hosts/{host}/exec", u(s.hostExec))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/files", u(s.filesList))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/files/content", u(s.fileGet))
	mux.HandleFunc("PUT /api/workspaces/{ws}/hosts/{host}/files/content", u(s.filePut))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/terminal", u(s.terminal))
	mux.HandleFunc("DELETE /api/workspaces/{ws}/known-hosts", u(s.forgetHostKey))

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
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/docker/containers", u(s.dockerContainers))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/docker/images", u(s.dockerImages))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/docker/stats", u(s.dockerStats))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/docker/containers/{id}/inspect", u(s.dockerInspect))
	mux.HandleFunc("POST /api/workspaces/{ws}/hosts/{host}/docker/containers/{id}/{action}", u(s.dockerAction))

	// kubernetes
	mux.HandleFunc("GET /api/workspaces/{ws}/clusters/{c}/namespaces", u(s.kubeNamespaces))
	mux.HandleFunc("GET /api/workspaces/{ws}/clusters/{c}/list/{type}", u(s.kubeList))
	mux.HandleFunc("GET /api/workspaces/{ws}/clusters/{c}/yaml/{type}/{ns}/{name}", u(s.kubeYAML))
	mux.HandleFunc("GET /api/workspaces/{ws}/clusters/{c}/logs/{ns}/{pod}", u(s.kubeLogs))
	mux.HandleFunc("POST /api/workspaces/{ws}/clusters/{c}/deployments/{ns}/{name}/restart", u(s.kubeRestart))
	mux.HandleFunc("POST /api/workspaces/{ws}/clusters/{c}/deployments/{ns}/{name}/scale", u(s.kubeScale))
	mux.HandleFunc("DELETE /api/workspaces/{ws}/clusters/{c}/pods/{ns}/{name}", u(s.kubeDeletePod))

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
	mux.HandleFunc("POST /api/workspaces/{ws}/sessions/{id}/approvals/{aid}", u(s.sessApproval))
	mux.HandleFunc("GET /api/workspaces/{ws}/sessions/{id}/stream", u(s.sessStream))

	// git repositories
	s.gitRoutes(mux, u)
	s.connectorRoutes(mux, u)
	s.topologyRoutes(mux, u)

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

func (s *Server) vaultStatus(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.status()) }

func (s *Server) vaultInit(w http.ResponseWriter, r *http.Request) {
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.vault.Init(in.Password); err != nil {
		fail(w, err)
		return
	}
	for _, ws := range s.ws.List() {
		s.vault.EnsureScope(ws.Name())
	}
	s.audit.Record(auditEntry("vault.init", "", nil))
	writeJSON(w, 200, s.status())
}

func (s *Server) vaultUnlock(w http.ResponseWriter, r *http.Request) {
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.vault.Unlock(in.Password); err != nil {
		if errors.Is(err, vault.ErrBadPassword) {
			s.audit.Record(auditEntryResult("vault.unlock", "denied"))
			time.Sleep(500 * time.Millisecond)
		}
		fail(w, err)
		return
	}
	for _, ws := range s.ws.List() {
		s.vault.EnsureScope(ws.Name())
	}
	s.audit.Record(auditEntry("vault.unlock", "", nil))
	writeJSON(w, 200, s.status())
}

func (s *Server) vaultLock(w http.ResponseWriter, r *http.Request) {
	s.vault.Lock()
	writeJSON(w, 200, s.status())
}

func (s *Server) vaultPassword(w http.ResponseWriter, r *http.Request) {
	var in struct{ OldPassword, NewPassword string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	err := s.vault.ChangePassword(in.OldPassword, in.NewPassword)
	s.record(r, "", "vault.change_password", "", nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// itemDTO never includes secret values.
type fieldDTO struct {
	Name     string `json:"name"`
	Secret   bool   `json:"secret"`
	Value    string `json:"value,omitempty"`
	HasValue bool   `json:"hasValue"`
}

type itemDTO struct {
	ID          string       `json:"id"`
	Scope       string       `json:"scope"`
	Type        string       `json:"type"`
	Name        string       `json:"name"`
	Tags        []string     `json:"tags"`
	Notes       string       `json:"notes"`
	Fields      []fieldDTO   `json:"fields"`
	Policy      vault.Policy `json:"policy"`
	Ref         string       `json:"ref"`
	PublicKey   string       `json:"publicKey,omitempty"`
	Fingerprint string       `json:"fingerprint,omitempty"`
	Created     time.Time    `json:"created"`
	Updated     time.Time    `json:"updated"`
	Revision    int          `json:"revision"`
}

func toDTO(it vault.Item) itemDTO {
	if it.Tags == nil {
		it.Tags = []string{}
	}
	d := itemDTO{ID: it.ID, Scope: it.Scope, Type: it.Type, Name: it.Name, Tags: it.Tags, Notes: it.Notes, Policy: it.Policy,
		Ref: it.Ref(), Created: it.Created, Updated: it.Updated, Revision: it.Revision, Fields: []fieldDTO{}}
	for _, f := range it.Fields {
		fd := fieldDTO{Name: f.Name, Secret: f.Secret, HasValue: f.Value != ""}
		if !f.Secret {
			fd.Value = f.Value
		}
		d.Fields = append(d.Fields, fd)
	}
	if it.Type == "ssh-key" {
		d.PublicKey, d.Fingerprint = vault.PublicKeyInfo(it)
	}
	return d
}

func (s *Server) itemsList(w http.ResponseWriter, r *http.Request) {
	items, err := s.vault.List(r.URL.Query().Get("scope"))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]itemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toDTO(it))
	}
	writeJSON(w, 200, out)
}

type itemInput struct {
	Scope  string   `json:"scope"`
	Type   string   `json:"type"`
	Name   string   `json:"name"`
	Tags   []string `json:"tags"`
	Notes  string   `json:"notes"`
	Fields []struct {
		Name   string  `json:"name"`
		Value  *string `json:"value"`
		Secret bool    `json:"secret"`
	} `json:"fields"`
	Policy *vault.Policy `json:"policy"`
}

func (in itemInput) toItem() (vault.Item, map[string]bool) {
	it := vault.Item{Scope: in.Scope, Type: in.Type, Name: in.Name, Tags: in.Tags, Notes: in.Notes}
	if in.Policy != nil {
		it.Policy = *in.Policy
	}
	keep := map[string]bool{}
	for _, f := range in.Fields {
		v := ""
		if f.Value == nil {
			keep[f.Name] = true
		} else {
			v = *f.Value
		}
		it.Fields = append(it.Fields, vault.Field{Name: f.Name, Value: v, Secret: f.Secret})
	}
	return it, keep
}

func (s *Server) ensureScope(scope string) error {
	if scope == "" {
		return errors.New("scope is required")
	}
	if scope != vault.GlobalScope {
		if _, err := s.ws.Get(scope); err != nil {
			return err
		}
	}
	return s.vault.EnsureScope(scope)
}

func (s *Server) itemCreate(w http.ResponseWriter, r *http.Request) {
	var in itemInput
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.ensureScope(in.Scope); err != nil {
		fail(w, err)
		return
	}
	it, _ := in.toItem()
	if in.Policy == nil {
		it.Policy.AgentUse = true
	}
	created, err := s.vault.Create(it)
	s.record(r, scopeWS(in.Scope), "vault.create", "vault://"+in.Scope+"/"+in.Name, map[string]any{"type": in.Type}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, toDTO(created))
}

func scopeWS(scope string) string {
	if scope == vault.GlobalScope {
		return ""
	}
	return scope
}

func (s *Server) itemUpdate(w http.ResponseWriter, r *http.Request) {
	var in itemInput
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	it, keep := in.toItem()
	if in.Policy == nil {
		if cur, err := s.vault.Get(r.PathValue("id")); err == nil {
			it.Policy = cur.Policy
		}
	}
	updated, err := s.vault.Update(r.PathValue("id"), it, keep)
	if err != nil {
		fail(w, err)
		return
	}
	s.record(r, scopeWS(updated.Scope), "vault.update", updated.Ref(), nil, nil)
	writeJSON(w, 200, toDTO(updated))
}

func (s *Server) itemDelete(w http.ResponseWriter, r *http.Request) {
	it, err := s.vault.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.vault.Delete(it.ID); err != nil {
		fail(w, err)
		return
	}
	s.record(r, scopeWS(it.Scope), "vault.delete", it.Ref(), nil, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) itemReveal(w http.ResponseWriter, r *http.Request) {
	var in struct{ Field string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	it, err := s.vault.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	s.record(r, scopeWS(it.Scope), "vault.reveal", it.Ref()+"#"+in.Field, nil, nil)
	writeJSON(w, 200, map[string]string{"value": it.Get(in.Field)})
}

func (s *Server) itemTOTP(w http.ResponseWriter, r *http.Request) {
	it, err := s.vault.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	code, period, remaining, err := vault.TOTP(it.Get("totp"), time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"code": code, "period": period, "remaining": remaining})
}

func (s *Server) sshGenerate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope, Name, Algorithm, Comment string
		Tags                            []string
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.ensureScope(in.Scope); err != nil {
		fail(w, err)
		return
	}
	comment := in.Comment
	if comment == "" {
		comment = in.Name + "@opsdeck"
	}
	fields, err := vault.GenerateSSHKey(in.Algorithm, comment)
	if err != nil {
		fail(w, err)
		return
	}
	it, err := s.vault.Create(vault.Item{Scope: in.Scope, Type: "ssh-key", Name: in.Name, Tags: in.Tags, Fields: fields, Policy: vault.Policy{AgentUse: true}})
	s.record(r, scopeWS(in.Scope), "vault.ssh_generate", "vault://"+in.Scope+"/"+in.Name, map[string]any{"algorithm": in.Algorithm}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, toDTO(it))
}

func (s *Server) sshImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope, Name, PrivateKey, Passphrase string
		Tags                                []string
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.ensureScope(in.Scope); err != nil {
		fail(w, err)
		return
	}
	fields, err := vault.ImportSSHKey(in.PrivateKey, in.Passphrase)
	if err != nil {
		fail(w, err)
		return
	}
	it, err := s.vault.Create(vault.Item{Scope: in.Scope, Type: "ssh-key", Name: in.Name, Tags: in.Tags, Fields: fields, Policy: vault.Policy{AgentUse: true}})
	s.record(r, scopeWS(in.Scope), "vault.ssh_import", "vault://"+in.Scope+"/"+in.Name, nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, toDTO(it))
}

func (s *Server) passwordGen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Length  int
		Symbols bool
	}
	readJSON(r, &in)
	if in.Length == 0 {
		in.Length = 24
	}
	p, err := vault.GeneratePassword(in.Length, in.Symbols)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"password": p})
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
			fail(w, fmt.Errorf("provider %s: type must be anthropic, openai, claude-code, codex, gemini-cli or acp", p.ID))
			return
		}
		if p.Type == extagent.ACP && p.Command == "" {
			fail(w, fmt.Errorf("provider %s: an ACP agent needs a command", p.ID))
			return
		}
		if extagent.IsKind(p.Type) && len(p.Models) == 0 {
			p.Models = []string{"default"}
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
	if err := s.cfg.SetSettings(in); err != nil {
		fail(w, err)
		return
	}
	s.vault.SetAutoLock(time.Duration(in.AutoLockMinutes) * time.Minute)
	s.record(r, "", "settings.update", "", nil, nil)
	writeJSON(w, 200, s.cfg.Settings())
}

// ---------------------------------------------------------------------------
// workspaces

type wsSummary struct {
	Name          string   `json:"name"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Color         string   `json:"color"`
	Tags          []string `json:"tags"`
	Path          string   `json:"path"`
	ResourceCount int      `json:"resourceCount"`
	HostCount     int      `json:"hostCount"`
}

type wsFull struct {
	wsSummary
	Environments []workspace.Environment `json:"environments"`
	Settings     workspace.Settings      `json:"settings"`
	Permissions  workspace.Permissions   `json:"permissions"`
	MCPServers   []workspace.MCPServer   `json:"mcpServers"`
}

func summary(w *workspace.Workspace) wsSummary {
	hosts := 0
	for _, r := range w.Resources {
		if r.Kind == "Host" {
			hosts++
		}
	}
	return wsSummary{Name: w.Name(), Title: w.Metadata.Title, Description: w.Metadata.Description, Color: w.Metadata.Color,
		Tags: w.Metadata.Tags, Path: w.Path, ResourceCount: len(w.Resources), HostCount: hosts}
}

func full(w *workspace.Workspace) wsFull {
	f := wsFull{wsSummary: summary(w), Environments: w.Environments, Settings: w.Settings, Permissions: w.Permissions, MCPServers: w.MCPServers}
	if f.Environments == nil {
		f.Environments = []workspace.Environment{}
	}
	if f.Permissions.Rules == nil {
		f.Permissions.Rules = []workspace.Rule{}
	}
	if f.MCPServers == nil {
		f.MCPServers = []workspace.MCPServer{}
	}
	for i := range f.MCPServers {
		if f.MCPServers[i].Args == nil {
			f.MCPServers[i].Args = []string{}
		}
		if f.MCPServers[i].Env == nil {
			f.MCPServers[i].Env = map[string]string{}
		}
	}
	return f
}

func (s *Server) wsList(w http.ResponseWriter, r *http.Request) {
	out := []wsSummary{}
	for _, ws := range s.ws.List() {
		out = append(out, summary(ws))
	}
	writeJSON(w, 200, out)
}

func (s *Server) wsCreate(w http.ResponseWriter, r *http.Request) {
	var in workspace.Meta
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	ws, err := s.ws.Create(in)
	s.record(r, in.Name, "workspace.create", "", nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	s.vault.EnsureScope(ws.Name())
	writeJSON(w, 201, summary(ws))
}

func (s *Server) wsImport(w http.ResponseWriter, r *http.Request) {
	var in struct{ Path string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	ws, err := s.ws.Import(in.Path)
	if err != nil {
		fail(w, err)
		return
	}
	s.vault.EnsureScope(ws.Name())
	s.record(r, ws.Name(), "workspace.import", in.Path, nil, nil)
	writeJSON(w, 201, summary(ws))
}

func (s *Server) wsGet(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, full(ws))
}

func (s *Server) wsPut(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in wsFull
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	ws.Metadata.Title, ws.Metadata.Description, ws.Metadata.Color = in.Title, in.Description, in.Color
	if in.Tags != nil {
		ws.Metadata.Tags = in.Tags
	}
	if in.Environments != nil {
		ws.Environments = in.Environments
	}
	ws.Settings = in.Settings
	for _, rule := range in.Permissions.Rules {
		if rule.Effect != "allow" && rule.Effect != "ask" && rule.Effect != "deny" {
			fail(w, fmt.Errorf("rule effect must be allow, ask or deny"))
			return
		}
	}
	ws.Permissions = in.Permissions
	before := map[string]bool{}
	for _, m := range ws.MCPServers {
		before[mcpx.Hash(ws.Name(), m)] = true
	}
	ws.MCPServers = in.MCPServers
	if err := ws.Save(); err != nil {
		fail(w, err)
		return
	}
	// Servers the user added or edited here are trusted; definitions that
	// came from disk (import, git pull) need an explicit trust action.
	var hashes []string
	for _, m := range ws.MCPServers {
		if h := mcpx.Hash(ws.Name(), m); !before[h] {
			hashes = append(hashes, h)
		}
	}
	if err := s.cfg.TrustMCP(hashes...); err != nil {
		fail(w, err)
		return
	}
	s.record(r, ws.Name(), "workspace.update", "", nil, nil)
	writeJSON(w, 200, full(ws))
}

func (s *Server) wsDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("ws")
	s.hub.CloseWorkspace(name)
	s.forwards.StopWorkspace(name)
	if err := s.ws.Delete(name); err != nil {
		fail(w, err)
		return
	}
	if r.URL.Query().Get("purgeVault") == "1" {
		s.vault.DeleteScope(name)
	}
	s.record(r, name, "workspace.delete", "", map[string]any{"purgeVault": r.URL.Query().Get("purgeVault") == "1"}, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) resList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	out := ws.Resources
	if out == nil {
		out = []*workspace.Resource{}
	}
	writeJSON(w, 200, out)
}

func (s *Server) resCreate(w http.ResponseWriter, r *http.Request) {
	s.resPut(w, r, "")
}

func (s *Server) resUpdate(w http.ResponseWriter, r *http.Request) {
	s.resPut(w, r, r.PathValue("kind")+"/"+r.PathValue("name"))
}

func (s *Server) resPut(w http.ResponseWriter, r *http.Request, oldID string) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var res workspace.Resource
	if err := readJSON(r, &res); err != nil {
		fail(w, err)
		return
	}
	res.CleanSpec()
	if err := res.CheckNoLiteralSecrets(); err != nil {
		fail(w, err)
		return
	}
	if err := ws.PutResource(oldID, &res); err != nil {
		fail(w, err)
		return
	}
	if oldID != "" {
		// connection settings may have changed
		if kind, name, _ := strings.Cut(oldID, "/"); kind == "Host" {
			s.hub.Disconnect(ws.Name(), name)
		} else if workspace.IsNetworkKind(kind) {
			s.hub.NetworkDown(ws.Name(), name)
		} else if kind == "GitRemote" && res.Kind == "GitRemote" {
			renameWorkingCopy(ws, name, res.Name)
		}
	}
	action := "resource.create"
	if oldID != "" {
		action = "resource.update"
	}
	s.record(r, ws.Name(), action, res.ID(), nil, nil)
	writeJSON(w, 200, res)
}

func (s *Server) resDelete(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	id := r.PathValue("kind") + "/" + r.PathValue("name")
	if err := ws.DeleteResource(id); err != nil {
		fail(w, err)
		return
	}
	if workspace.IsNetworkKind(r.PathValue("kind")) {
		s.hub.NetworkDown(ws.Name(), r.PathValue("name"))
	}
	s.record(r, ws.Name(), "resource.delete", id, nil, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) wsValidate(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, ws.Validate(func(ref string) error {
		_, _, err := s.vault.ResolveFor(ws.Name(), ref)
		return err
	}))
}

func (s *Server) wsTopology(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	nodes, edges := ws.Topology()
	writeJSON(w, 200, map[string]any{"nodes": nodes, "edges": edges})
}

type hop struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Label string `json:"label"`
}

func hops(chain []*workspace.Resource) []hop {
	out := []hop{}
	for _, h := range chain {
		label := h.Str("address")
		if label == "" {
			label = h.Kind
		}
		out = append(out, hop{Kind: h.Kind, Name: h.Name, Label: label})
	}
	return out
}

func (s *Server) wsResolve(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	res := ws.Find(r.PathValue("kind"), r.PathValue("name"))
	if res == nil {
		writeErr(w, 404, "resource not found")
		return
	}
	chain, err := ws.Chain(res)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"path": hops(chain)})
}

func (s *Server) docsList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, ws.Docs())
}

func (s *Server) docGet(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	c, err := ws.ReadDoc(p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"path": p, "content": c})
}

func (s *Server) docPut(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct{ Path, Content string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := ws.WriteDoc(in.Path, in.Content); err != nil {
		fail(w, err)
		return
	}
	s.record(r, ws.Name(), "doc.write", in.Path, nil, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) docDelete(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	if err := ws.DeleteDoc(p); err != nil {
		fail(w, err)
		return
	}
	s.record(r, ws.Name(), "doc.delete", p, nil, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) artifactsList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, ws.Artifacts())
}

func (s *Server) artifactGet(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	c, trunc, err := ws.ReadArtifact(p, 2<<20)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"path": p, "content": c, "truncated": trunc})
}

func (s *Server) artifactDelete(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	if err := ws.DeleteArtifact(p); err != nil {
		fail(w, err)
		return
	}
	s.record(r, ws.Name(), "artifact.delete", p, nil, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) memoryGet(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	b, _ := os.ReadFile(ws.MemoryPath())
	writeJSON(w, 200, map[string]string{"content": string(b)})
}

func (s *Server) memoryPut(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct{ Content string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := os.WriteFile(ws.MemoryPath(), []byte(in.Content), 0o600); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) skillsList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, agent.AllSkills(ws))
}

func (s *Server) mcpStatus(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	s.mcp.Ensure(ctx, ws)
	writeJSON(w, 200, s.mcp.Statuses(ws))
}

func (s *Server) mcpTrust(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	for _, m := range ws.MCPServers {
		if m.Name == r.PathValue("name") {
			if err := s.cfg.TrustMCP(mcpx.Hash(ws.Name(), m)); err != nil {
				fail(w, err)
				return
			}
			s.record(r, ws.Name(), "mcp.trust", m.Name, map[string]any{"command": m.Command, "args": m.Args}, nil)
			ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
			defer cancel()
			s.mcp.Ensure(ctx, ws)
			writeJSON(w, 200, s.mcp.Statuses(ws))
			return
		}
	}
	writeErr(w, 404, "MCP server not found")
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

func (s *Server) hostRes(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, *workspace.Resource, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, nil, false
	}
	h := ws.Find("Host", r.PathValue("host"))
	if h == nil {
		writeErr(w, 404, "host not found")
		return nil, nil, false
	}
	return ws, h, true
}

func (s *Server) hostCheck(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	res := s.hub.Check(ctx, ws, h)
	chain, _ := ws.Chain(h)
	writeJSON(w, 200, map[string]any{"reachable": res.Reachable, "latencyMs": res.LatencyMs, "error": res.Error, "ssh": res.SSH, "path": hops(chain)})
}

func (s *Server) hostStats(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	st, err := s.hub.Stats(ctx, ws, h)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) hostExec(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	var in struct {
		Command    string
		TimeoutSec int
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.TimeoutSec <= 0 || in.TimeoutSec > 3600 {
		in.TimeoutSec = 120
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(in.TimeoutSec)*time.Second)
	defer cancel()
	res, err := s.hub.Exec(ctx, ws, h, in.Command, nil, 4<<20)
	s.record(r, ws.Name(), "host.exec", h.ID(), map[string]any{"command": in.Command, "exitCode": res.ExitCode}, err)
	if err != nil && res.DurationMs == 0 {
		fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) filesList(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	path, entries, err := s.hub.ListDir(r.Context(), ws, h, r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	if entries == nil {
		entries = []connect.FileEntry{}
	}
	writeJSON(w, 200, map[string]any{"path": path, "entries": entries})
}

func (s *Server) fileGet(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	c, trunc, err := s.hub.ReadFile(r.Context(), ws, h, p, 2<<20)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"path": p, "content": c, "truncated": trunc})
}

func (s *Server) filePut(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	var in struct{ Path, Content string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	err := s.hub.WriteFile(r.Context(), ws, h, in.Path, in.Content)
	s.record(r, ws.Name(), "host.write_file", h.ID(), map[string]any{"path": in.Path, "bytes": len(in.Content)}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) forgetHostKey(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	host := r.URL.Query().Get("host")
	// Accept a host resource name too.
	if res := ws.Find("Host", host); res != nil {
		host = net.JoinHostPort(res.Str("address"), strconv.Itoa(res.Int("port", 22)))
		s.hub.Disconnect(ws.Name(), res.Name)
	}
	if err := s.hub.ForgetHostKey(ws.Name(), host); err != nil {
		fail(w, err)
		return
	}
	s.record(r, ws.Name(), "host.forget_key", host, nil, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
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
	p := ws.Find("Panel", r.PathValue("name"))
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
// docker

func (s *Server) dockerContainers(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	list, err := s.docker.Containers(r.Context(), ws, h, r.URL.Query().Get("all") == "1")
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) dockerImages(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	list, err := s.docker.Images(r.Context(), ws, h)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) dockerStats(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	list, err := s.docker.Stats(r.Context(), ws, h)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) dockerInspect(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	raw, err := s.docker.Inspect(r.Context(), ws, h, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) dockerAction(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	id, action := r.PathValue("id"), r.PathValue("action")
	err := s.docker.Action(r.Context(), ws, h, id, action)
	s.record(r, ws.Name(), "docker."+action, h.ID()+"/"+id, nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// kubernetes

func (s *Server) cluster(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, *workspace.Resource, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, nil, false
	}
	c := ws.Find("KubernetesCluster", r.PathValue("c"))
	if c == nil {
		writeErr(w, 404, "cluster not found")
		return nil, nil, false
	}
	return ws, c, true
}

func nsParam(ns string) string {
	if ns == "_" || ns == "*" {
		return ""
	}
	return ns
}

func (s *Server) kubeNamespaces(w http.ResponseWriter, r *http.Request) {
	ws, c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	cs, _, err := s.kube.Client(r.Context(), ws, c)
	if err != nil {
		fail(w, err)
		return
	}
	rows, err := kube.List(r.Context(), cs, "namespaces", "")
	if err != nil {
		fail(w, err)
		return
	}
	out := []string{}
	for _, row := range rows {
		out = append(out, fmt.Sprint(row["name"]))
	}
	writeJSON(w, 200, out)
}

func (s *Server) kubeList(w http.ResponseWriter, r *http.Request) {
	ws, c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	cs, _, err := s.kube.Client(r.Context(), ws, c)
	if err != nil {
		fail(w, err)
		return
	}
	rows, err := kube.List(r.Context(), cs, r.PathValue("type"), nsParam(r.URL.Query().Get("namespace")))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) kubeYAML(w http.ResponseWriter, r *http.Request) {
	ws, c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	cs, _, err := s.kube.Client(r.Context(), ws, c)
	if err != nil {
		fail(w, err)
		return
	}
	y, err := kube.YAML(r.Context(), cs, r.PathValue("type"), nsParam(r.PathValue("ns")), r.PathValue("name"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"yaml": y})
}

func (s *Server) kubeLogs(w http.ResponseWriter, r *http.Request) {
	ws, c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	cs, _, err := s.kube.Client(r.Context(), ws, c)
	if err != nil {
		fail(w, err)
		return
	}
	tail, _ := strconv.ParseInt(r.URL.Query().Get("tail"), 10, 64)
	if tail <= 0 {
		tail = 500
	}
	lines := []string{}
	err = kube.Logs(r.Context(), cs, r.PathValue("ns"), r.PathValue("pod"), r.URL.Query().Get("container"), tail, false, func(l string) { lines = append(lines, l) })
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"lines": lines})
}

func (s *Server) kubeRestart(w http.ResponseWriter, r *http.Request) {
	ws, c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	cs, _, err := s.kube.Client(r.Context(), ws, c)
	if err == nil {
		err = kube.RestartDeployment(r.Context(), cs, r.PathValue("ns"), r.PathValue("name"))
	}
	s.record(r, ws.Name(), "k8s.restart", c.ID()+"/"+r.PathValue("ns")+"/"+r.PathValue("name"), nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) kubeScale(w http.ResponseWriter, r *http.Request) {
	ws, c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	var in struct{ Replicas int32 }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	cs, _, err := s.kube.Client(r.Context(), ws, c)
	if err == nil {
		err = kube.ScaleDeployment(r.Context(), cs, r.PathValue("ns"), r.PathValue("name"), in.Replicas)
	}
	s.record(r, ws.Name(), "k8s.scale", c.ID()+"/"+r.PathValue("ns")+"/"+r.PathValue("name"), map[string]any{"replicas": in.Replicas}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) kubeDeletePod(w http.ResponseWriter, r *http.Request) {
	ws, c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	cs, _, err := s.kube.Client(r.Context(), ws, c)
	if err == nil {
		err = kube.DeletePod(r.Context(), cs, r.PathValue("ns"), r.PathValue("name"))
	}
	s.record(r, ws.Name(), "k8s.delete_pod", c.ID()+"/"+r.PathValue("ns")+"/"+r.PathValue("name"), nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

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
	var in struct{ Model, Mode string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	sess, err := s.agents.Create(ws, in.Model, in.Mode)
	if err != nil {
		fail(w, err)
		return
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
	writeJSON(w, 200, map[string]any{"session": sess.Info(), "events": events})
}

func (s *Server) sessPut(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var in struct{ Title, Model, Mode string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
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
	var in struct{ Text string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.agents.Send(r.PathValue("ws"), r.PathValue("id"), in.Text); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
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
	var in struct{ Decision, Note string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := sess.Resolve(r.PathValue("aid"), in.Decision, in.Note); err != nil {
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
