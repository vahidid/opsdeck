package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"opsdeck/internal/agent"
	"opsdeck/internal/bundle"
	"opsdeck/internal/mcpx"
	"opsdeck/internal/workspace"
)

// Workspaces: the workspace YAML, its resources, docs, artifacts, memory, skills and MCP servers.

func (s *Server) workspaceRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/kinds", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, workspace.Kinds) })
	mux.HandleFunc("GET /api/workspaces", u(s.wsList))
	mux.HandleFunc("POST /api/workspaces", u(s.wsCreate))
	mux.HandleFunc("POST /api/workspaces/import", u(s.wsImport))
	mux.HandleFunc("POST /api/workspaces/import-file", u(s.wsImportFile))
	mux.HandleFunc("GET /api/workspaces/{ws}", u(s.wsGet))
	mux.HandleFunc("PUT /api/workspaces/{ws}", u(s.wsPut))
	mux.HandleFunc("DELETE /api/workspaces/{ws}", u(s.wsDelete))
	mux.HandleFunc("POST /api/workspaces/{ws}/export", u(s.wsExport))
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
	mux.HandleFunc("POST /api/workspaces/{ws}/hooks/trust", u(s.hooksTrust))
	mux.HandleFunc("GET /api/agent/hook-runs", u(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.appHookRuns()) }))
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
	Hooks        []workspace.Hook        `json:"hooks"`
	// UntrustedHooks are the indexes of hooks that do not run until the
	// user trusts them on this machine (they came from the folder).
	UntrustedHooks []int `json:"untrustedHooks"`
	// HookRuns are the hooks' last results, by index (null: not run since
	// opsdeck started).
	HookRuns []*agent.HookRun `json:"hookRuns"`
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
	f.Hooks = append([]workspace.Hook{}, w.Hooks...)
	f.UntrustedHooks = []int{}
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

// wsExport returns the workspace and its vault scope as one file encrypted
// with the given password (see internal/bundle).
func (s *Server) wsExport(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	f, items, err := bundle.Export(ws, s.vault, in.Password)
	s.record(r, ws.Name(), "workspace.export", "", map[string]any{"vaultItems": items}, err)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.opsdeck"`, ws.Name(), time.Now().Format("2006-01-02")))
	writeJSON(w, 200, f)
}

// wsImportFile creates a workspace from an exported file; its secrets are
// stored in this vault under this vault's keys.
func (s *Server) wsImportFile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Bundle   bundle.File
		Password string
	}
	// larger than readJSON allows: the export carries all workspace files
	if err := json.NewDecoder(io.LimitReader(r.Body, 128<<20)).Decode(&in); err != nil {
		fail(w, errors.New("invalid export file: "+err.Error()))
		return
	}
	res, err := bundle.Import(s.ws, s.vault, &in.Bundle, in.Password)
	if err != nil {
		fail(w, err)
		return
	}
	s.record(r, res.Workspace.Name(), "workspace.import", "file", map[string]any{"vaultItems": res.Items, "skipped": res.Skipped}, nil)
	writeJSON(w, 201, map[string]any{"workspace": summary(res.Workspace), "items": res.Items, "skipped": res.Skipped})
}

func (s *Server) wsGet(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.fullWS(ws))
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
	if err := checkWorkspaceAgent(ws, &ws.Settings, in.Hooks); err != nil {
		fail(w, err)
		return
	}
	before := map[string]bool{}
	for _, m := range ws.MCPServers {
		before[mcpx.Hash(ws.Name(), m)] = true
	}
	for _, h := range ws.Hooks {
		before[agent.HookHash(ws.Name(), h)] = true
	}
	ws.MCPServers = in.MCPServers
	if in.Hooks != nil {
		ws.Hooks = in.Hooks
	}
	if err := ws.Save(); err != nil {
		fail(w, err)
		return
	}
	// Servers and hooks the user added or edited here are trusted;
	// definitions that came from disk (import, git pull) need an explicit
	// trust action.
	var hashes []string
	for _, m := range ws.MCPServers {
		if h := mcpx.Hash(ws.Name(), m); !before[h] {
			hashes = append(hashes, h)
		}
	}
	for _, hk := range ws.Hooks {
		if h := agent.HookHash(ws.Name(), hk); !before[h] {
			hashes = append(hashes, h)
		}
	}
	if err := s.cfg.TrustMCP(hashes...); err != nil {
		fail(w, err)
		return
	}
	s.record(r, ws.Name(), "workspace.update", "", nil, nil)
	writeJSON(w, 200, s.fullWS(ws))
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
	s.keys.RevokeWorkspace(name)
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
