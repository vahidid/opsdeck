package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"opsdeck/internal/agent"
	"opsdeck/internal/connect"
	"opsdeck/internal/scripts"
	"opsdeck/internal/workspace"
)

func (s *Server) scriptRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/workspaces/{ws}/scripts", u(s.scriptsList))
	mux.HandleFunc("GET /api/workspaces/{ws}/scripts/{scope}/{name}", u(s.scriptGet))
	mux.HandleFunc("PUT /api/workspaces/{ws}/scripts/{scope}/{name}", u(s.scriptPut))
	mux.HandleFunc("DELETE /api/workspaces/{ws}/scripts/{scope}/{name}", u(s.scriptDelete))
	mux.HandleFunc("POST /api/workspaces/{ws}/scripts/{scope}/{name}/approve", u(s.scriptApprove))
	mux.HandleFunc("POST /api/workspaces/{ws}/scripts/{scope}/{name}/copy", u(s.scriptCopy))
	mux.HandleFunc("POST /api/workspaces/{ws}/scripts/{scope}/{name}/run", u(s.scriptRun))
	mux.HandleFunc("GET /api/workspaces/{ws}/script-runs", u(s.scriptRuns))
}

// scriptStore returns the store named by {scope}: the user's library or the
// workspace's scripts/ folder.
func (s *Server) scriptStore(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, scripts.Store, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, scripts.Store{}, false
	}
	switch r.PathValue("scope") {
	case scripts.ScopeUser:
		return ws, scripts.Store{Dir: s.dirs.Scripts(), Scope: scripts.ScopeUser}, true
	case scripts.ScopeWorkspace:
		return ws, scripts.WorkspaceStore(ws), true
	}
	writeErr(w, 400, "scope must be user or workspace")
	return nil, scripts.Store{}, false
}

func (s *Server) scriptsList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	user := scripts.Store{Dir: s.dirs.Scripts(), Scope: scripts.ScopeUser}.List()
	own := scripts.WorkspaceStore(ws).List()
	writeJSON(w, 200, map[string]any{"user": user, "workspace": own})
}

func (s *Server) scriptGet(w http.ResponseWriter, r *http.Request) {
	_, st, ok := s.scriptStore(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var sc scripts.Script
	var err error
	if v := r.URL.Query().Get("version"); v != "" {
		n, _ := strconv.Atoi(v)
		sc, err = st.Version(name, n)
	} else {
		sc, err = st.Get(name)
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, sc)
}

func (s *Server) scriptPut(w http.ResponseWriter, r *http.Request) {
	ws, st, ok := s.scriptStore(w, r)
	if !ok {
		return
	}
	var in scripts.Script
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	in.Name = r.PathValue("name")
	sc, err := st.Save(in, "user")
	s.record(r, ws.Name(), "script.save", "script/"+st.Scope+"/"+in.Name, map[string]any{"version": sc.Version, "hash": sc.Hash}, err)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, sc)
}

func (s *Server) scriptDelete(w http.ResponseWriter, r *http.Request) {
	ws, st, ok := s.scriptStore(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	err := st.Delete(name)
	s.record(r, ws.Name(), "script.delete", "script/"+st.Scope+"/"+name, nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) scriptApprove(w http.ResponseWriter, r *http.Request) {
	ws, st, ok := s.scriptStore(w, r)
	if !ok {
		return
	}
	var in struct{ Hash string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	name := r.PathValue("name")
	sc, err := st.Approve(name, in.Hash)
	s.record(r, ws.Name(), "script.approve", "script/"+st.Scope+"/"+name, map[string]any{"hash": in.Hash, "version": sc.Version}, err)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, sc)
}

// scriptCopy copies a script to the other scope: one of the user's scripts
// into the workspace (to share it with the team, pinned at this version), or
// a workspace script into the user's library (to reuse it elsewhere).
func (s *Server) scriptCopy(w http.ResponseWriter, r *http.Request) {
	ws, from, ok := s.scriptStore(w, r)
	if !ok {
		return
	}
	to := scripts.WorkspaceStore(ws)
	if from.Scope == scripts.ScopeWorkspace {
		to = scripts.Store{Dir: s.dirs.Scripts(), Scope: scripts.ScopeUser}
	}
	name := r.PathValue("name")
	err := scripts.Copy(from, name, to)
	s.record(r, ws.Name(), "script.copy", "script/"+from.Scope+"/"+name, map[string]any{"to": to.Scope}, err)
	if err != nil {
		fail(w, err)
		return
	}
	sc, err := to.Get(name)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, sc)
}

// scriptRun runs a script from the Scripts page. The user starts it, so no
// approval is asked; it keeps running if the page is closed (a half-applied
// bootstrap is worse than a finished one) and is recorded in the history.
func (s *Server) scriptRun(w http.ResponseWriter, r *http.Request) {
	ws, st, ok := s.scriptStore(w, r)
	if !ok {
		return
	}
	var in struct {
		Target string
		Params map[string]string
		Force  bool
		Hash   string // the content the user looked at
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	sc, err := st.Get(r.PathValue("name"))
	if err != nil {
		fail(w, err)
		return
	}
	if in.Hash != "" && in.Hash != sc.Hash {
		writeErr(w, 409, fmt.Sprintf("the script changed since you opened it (now %s); reload it", sc.Hash))
		return
	}
	hosts, err := targetHosts(ws, in.Target)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var names []string
	for _, h := range hosts {
		names = append(names, h.Name)
	}
	runner := scripts.Runner{WS: ws, Hub: s.hub, Vault: s.vault, Redact: agent.NewRedactor(s.vault.SecretValues).Redact, Actor: "user"}
	ctx := connect.WithActor(context.WithoutCancel(r.Context()), connect.Actor{Kind: "user"})
	results, err := runner.Run(ctx, &sc, hosts, in.Params, in.Force)
	detail := map[string]any{"hosts": names, "version": sc.Version, "hash": sc.Hash, "force": in.Force}
	if err == nil {
		var failed []string
		for _, res := range results {
			if !res.OK() {
				failed = append(failed, res.Host)
			}
		}
		detail["failed"] = failed
	}
	s.record(r, ws.Name(), "script.run", "script/"+sc.Scope+"/"+sc.Name, detail, err)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"script": sc.Ref(), "results": results})
}

func (s *Server) scriptRuns(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	writeJSON(w, 200, scripts.Runs(ws, q.Get("script"), q.Get("host"), limit))
}

// targetHosts expands host names, host/<name>, group/<name> and
// comma-separated lists into hosts.
func targetHosts(ws *workspace.Workspace, target string) ([]*workspace.Resource, error) {
	var out []*workspace.Resource
	seen := map[string]bool{}
	for _, t := range strings.Split(target, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		res := ws.FindTarget(t)
		if res == nil {
			res = ws.Find("Host", strings.TrimPrefix(t, "host/"))
		}
		if res == nil {
			return nil, fmt.Errorf("target %q not found", t)
		}
		members := []*workspace.Resource{res}
		switch res.Kind {
		case "Host":
		case "HostGroup":
			members = ws.GroupMembers(res)
		default:
			return nil, fmt.Errorf("%s is a %s, not a host", t, res.Kind)
		}
		for _, h := range members {
			if !seen[h.Name] {
				seen[h.Name] = true
				out = append(out, h)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("choose at least one host")
	}
	return out, nil
}
