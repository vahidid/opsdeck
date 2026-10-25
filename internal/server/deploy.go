package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/deployx"
	"opsdeck/internal/workspace"
)

// Deployments page: the workspace's DeployPlatform resources (Portainer,
// Argo CD, Komodo), what they deploy, logs, diffs and actions. Reads are
// free; every action is audited.

func (s *Server) deployRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/deploy/platforms", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, deployx.Platforms) })
	D := "/api/workspaces/{ws}/deploy"
	mux.HandleFunc("GET "+D, u(s.deployList))
	mux.HandleFunc("GET "+D+"/{name}/targets", u(s.deployTargets))
	mux.HandleFunc("GET "+D+"/{name}/target", u(s.deployTarget))
	mux.HandleFunc("GET "+D+"/{name}/logs", u(s.deployLogs))
	mux.HandleFunc("GET "+D+"/{name}/diff", u(s.deployDiff))
	mux.HandleFunc("POST "+D+"/{name}/action", u(s.deployAction))
}

// DeployPlatformInfo is a DeployPlatform resource with what its server says.
type DeployPlatformInfo struct {
	Name        string          `json:"name"`
	Platform    string          `json:"platform"`
	Label       string          `json:"label"`
	URL         string          `json:"url"`
	Environment string          `json:"environment,omitempty"`
	Protected   bool            `json:"protected,omitempty"`
	Via         string          `json:"via,omitempty"`
	Description string          `json:"description,omitempty"`
	Patterns    []string        `json:"targets,omitempty"`
	Server      *deployx.Server `json:"server,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func (s *Server) deployInfo(ctx context.Context, ws *workspace.Workspace, r *workspace.Resource) DeployPlatformInfo {
	info, _ := deployx.PlatformByID(deployx.PlatformOf(r))
	d := DeployPlatformInfo{Name: r.Name, Platform: deployx.PlatformOf(r), Label: info.Label, URL: r.Str("url"), Environment: r.Environment,
		Protected: ws.EnvironmentOf(r).Protected, Via: r.Via, Description: r.Description, Patterns: r.List("targets")}
	o, err := s.deploy.Open(ctx, ws, r)
	if err == nil {
		var sv deployx.Server
		if sv, err = o.Verify(ctx); err == nil {
			d.Server = &sv
		}
	}
	if err != nil {
		d.Error = err.Error()
	}
	return d
}

// deployList checks every platform of the workspace (at once: each may be
// behind its own VPN).
func (s *Server) deployList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(userCtx(r), 60*time.Second)
	defer cancel()
	var res []*workspace.Resource
	for _, x := range ws.Resources {
		if x.Kind == "DeployPlatform" {
			res = append(res, x)
		}
	}
	out := make([]DeployPlatformInfo, len(res))
	var wg sync.WaitGroup
	for i, x := range res {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = s.deployInfo(ctx, ws, x)
		}()
	}
	wg.Wait()
	writeJSON(w, 200, out)
}

// deployOpen opens the platform named in the path.
func (s *Server) deployOpen(w http.ResponseWriter, r *http.Request, ctx context.Context) (*workspace.Workspace, *deployx.Opened, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, nil, false
	}
	res := ws.Find("DeployPlatform", r.PathValue("name"))
	if res == nil {
		writeErr(w, 404, "deployment platform not found")
		return nil, nil, false
	}
	o, err := s.deploy.Open(ctx, ws, res)
	if err != nil {
		writeErr(w, 502, err.Error())
		return nil, nil, false
	}
	return ws, o, true
}

func (s *Server) deployTargets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(userCtx(r), 90*time.Second)
	defer cancel()
	_, o, ok := s.deployOpen(w, r, ctx)
	if !ok {
		return
	}
	ts, err := o.Targets(ctx)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, ts)
}

func (s *Server) deployTarget(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(userCtx(r), 90*time.Second)
	defer cancel()
	_, o, ok := s.deployOpen(w, r, ctx)
	if !ok {
		return
	}
	d, err := o.Detail(ctx, r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) deployLogs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(userCtx(r), 90*time.Second)
	defer cancel()
	_, o, ok := s.deployOpen(w, r, ctx)
	if !ok {
		return
	}
	d, err := o.Detail(ctx, r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if tail <= 0 {
		tail = 300
	}
	text, err := o.Logs(ctx, d, deployx.LogOptions{Service: r.URL.Query().Get("service"), Tail: min(tail, 5000)})
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"text": text})
}

func (s *Server) deployDiff(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(userCtx(r), 90*time.Second)
	defer cancel()
	_, o, ok := s.deployOpen(w, r, ctx)
	if !ok {
		return
	}
	d, err := o.Detail(ctx, r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	diff, err := o.Diff(ctx, d)
	if errors.Is(err, deployx.ErrNoDiff) {
		writeJSON(w, 200, map[string]any{"supported": false})
		return
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"supported": true, "diff": diff})
}

func (s *Server) deployAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
		deployx.Action
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	// The action goes on when the page is left: a half-sent deploy is
	// worse than a finished one.
	ctx, cancel := context.WithTimeout(connect.WithActor(context.WithoutCancel(r.Context()), connect.Actor{Kind: "user"}), 10*time.Minute)
	defer cancel()
	ws, o, ok := s.deployOpen(w, r, ctx)
	if !ok {
		return
	}
	d, err := o.Detail(ctx, in.ID)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if err := o.Check(d, in.Action); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	res, err := o.Run(ctx, d, in.Action)
	detail := map[string]any{"target": d.Name, "id": d.ID, "type": d.Type, "platform": o.Info.ID, "before": deployx.Format(d.Target)}
	for k, v := range map[string]string{"revision": in.Revision, "history": in.History, "service": in.Service} {
		if v != "" {
			detail[k] = v
		}
	}
	if in.Prune {
		detail["prune"] = true
	}
	if res != nil {
		detail["result"] = res.Message
		if !res.OK && err == nil {
			err = errors.New(res.Message)
		}
	}
	s.record(r, ws.Name(), "deploy."+in.Action.ID, "deploy/"+o.Resource.Name+"/"+d.Name, detail, err)
	if err != nil {
		if res != nil {
			writeJSON(w, 200, res)
			return
		}
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, res)
}
