package server

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/gitx"
	"opsdeck/internal/workspace"
)

func (s *Server) gitRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	p := "/api/workspaces/{ws}/repos"
	mux.HandleFunc("GET "+p, u(s.reposList))
	mux.HandleFunc("POST /api/workspaces/{ws}/git/folder", u(s.repoFolder))
	mux.HandleFunc("POST "+p+"/{repo}/check", u(s.repoCheck))
	mux.HandleFunc("POST "+p+"/{repo}/clone", u(s.repoClone))
	mux.HandleFunc("DELETE "+p+"/{repo}/clone", u(s.repoRemove))
	mux.HandleFunc("POST "+p+"/{repo}/fetch", u(s.repoFetch))
	mux.HandleFunc("POST "+p+"/{repo}/pull", u(s.repoPull))
	mux.HandleFunc("GET "+p+"/{repo}/status", u(s.repoStatus))
	mux.HandleFunc("GET "+p+"/{repo}/info", u(s.repoInfo))
	mux.HandleFunc("GET "+p+"/{repo}/branches", u(s.repoBranches))
	mux.HandleFunc("POST "+p+"/{repo}/checkout", u(s.repoCheckout))
	mux.HandleFunc("GET "+p+"/{repo}/files", u(s.repoFiles))
	mux.HandleFunc("GET "+p+"/{repo}/file", u(s.repoFileGet))
	mux.HandleFunc("PUT "+p+"/{repo}/file", u(s.repoFilePut))
	mux.HandleFunc("DELETE "+p+"/{repo}/file", u(s.repoFileDelete))
	mux.HandleFunc("GET "+p+"/{repo}/diff", u(s.repoDiff))
	mux.HandleFunc("GET "+p+"/{repo}/log", u(s.repoLog))
	mux.HandleFunc("GET "+p+"/{repo}/commits/{rev}", u(s.repoShow))
	mux.HandleFunc("POST "+p+"/{repo}/commit", u(s.repoCommit))
	mux.HandleFunc("POST "+p+"/{repo}/push", u(s.repoPush))
	mux.HandleFunc("POST "+p+"/{repo}/discard", u(s.repoDiscard))
	mux.HandleFunc("GET "+p+"/{repo}/mrs", u(s.mrList))
	mux.HandleFunc("POST "+p+"/{repo}/mrs", u(s.mrCreate))
	mux.HandleFunc("GET "+p+"/{repo}/mrs/{id}", u(s.mrGet))
	mux.HandleFunc("POST "+p+"/{repo}/mrs/{id}/comment", u(s.mrComment))
	mux.HandleFunc("POST "+p+"/{repo}/mrs/{id}/merge", u(s.mrMerge))
}

func (s *Server) repo(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, *gitx.Repo, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, nil, false
	}
	repo, err := s.git.Open(ws, r.PathValue("repo"))
	if err != nil {
		fail(w, err)
		return nil, nil, false
	}
	return ws, repo, true
}

// netCtx bounds operations that talk to the server.
func netCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Minute)
}

// RepoSummary is a repository in the list.
type RepoSummary struct {
	Name        string       `json:"name"`
	URL         string       `json:"url"`
	Web         string       `json:"web"`
	Provider    string       `json:"provider"`
	Via         string       `json:"via,omitempty"`
	Environment string       `json:"environment,omitempty"`
	Description string       `json:"description,omitempty"`
	Docs        bool         `json:"docs"`
	Path        string       `json:"path,omitempty"` // working copy outside repos/ (spec.path)
	Auth        string       `json:"auth"`           // token | deployKey | connector | none
	Connection  string       `json:"connection,omitempty"`
	Error       string       `json:"error,omitempty"`
	Status      *gitx.Status `json:"status,omitempty"`
	Last        *gitx.Commit `json:"last,omitempty"`
}

func (s *Server) reposList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	out := []RepoSummary{}
	for _, res := range ws.Resources {
		if res.Kind != "GitRemote" {
			continue
		}
		sum := RepoSummary{Name: res.Name, URL: res.Str("url"), Provider: firstNonEmpty(res.Str("provider"), "auto"), Via: res.Via,
			Environment: res.Environment, Description: res.Description, Docs: res.Bool("docs", false), Auth: "none"}
		if ws.RepoExternal(res.Name) {
			sum.Path = ws.RepoDir(res.Name)
		}
		switch {
		case res.Str("token") != "":
			sum.Auth = "token"
		case res.Str("deployKey") != "":
			sum.Auth = "deployKey"
		}
		repo, err := s.git.Open(ws, res.Name)
		if err != nil {
			sum.Error = err.Error()
			out = append(out, sum)
			continue
		}
		if repo.Remote.Scheme != "file" {
			sum.Web = repo.Remote.Web()
		}
		if repo.Conn != nil && sum.Auth == "none" {
			sum.Auth = "connector"
		}
		if repo.Conn != nil {
			sum.Connection = repo.Conn.ID
			if sum.Provider == "auto" {
				sum.Provider = repo.Conn.Provider
			}
		}
		if repo.Cloned() {
			if st, err := repo.Status(r.Context()); err == nil {
				sum.Status = st
			} else {
				sum.Error = err.Error()
			}
			if log, err := repo.Log(r.Context(), "", "", 1); err == nil && len(log) > 0 {
				sum.Last = &log[0]
			}
		}
		out = append(out, sum)
	}
	writeJSON(w, 200, out)
}

// repoFolder inspects a folder on this computer offered as a working copy
// (adding a repository cloned elsewhere).
func (s *Server) repoFolder(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.workspace(w, r); !ok {
		return
	}
	var in struct{ Path string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	f, err := s.git.InspectFolder(r.Context(), in.Path)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, f)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// done writes the result of an operation and records it.
func (s *Server) done(w http.ResponseWriter, r *http.Request, ws *workspace.Workspace, repo *gitx.Repo, action string, detail map[string]any, out any, err error) {
	s.record(r, ws.Name(), "git."+action, repo.R.ID(), detail, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) repoCheck(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ctx, cancel := netCtx(r)
	defer cancel()
	b, err := repo.Check(ctx)
	res := map[string]any{"ok": err == nil, "defaultBranch": b}
	if err != nil {
		res["error"] = err.Error()
	}
	// Ask the hosting API too: merge requests need it.
	if err == nil && repo.Remote.Scheme != "file" {
		if p, perr := repo.Provider(ctx); perr != nil {
			res["apiError"] = perr.Error()
		} else if info, perr := p.Info(ctx); perr != nil {
			res["provider"], res["apiError"] = p.Kind(), perr.Error()
		} else {
			res["provider"], res["canPush"], res["webUrl"] = p.Kind(), info.CanPush, info.WebURL
		}
	}
	s.record(r, ws.Name(), "git.check", repo.R.ID(), nil, err)
	writeJSON(w, 200, res)
}

func (s *Server) repoClone(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	var in struct{ Branch string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := netCtx(r)
	defer cancel()
	out, err := repo.Clone(ctx, in.Branch)
	s.done(w, r, ws, repo, "clone", map[string]any{"branch": in.Branch}, map[string]string{"output": out}, err)
}

func (s *Server) repoRemove(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	s.done(w, r, ws, repo, "remove_working_copy", nil, map[string]bool{"ok": true}, repo.Remove())
}

func (s *Server) repoFetch(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ctx, cancel := netCtx(r)
	defer cancel()
	out, err := repo.Fetch(ctx)
	s.done(w, r, ws, repo, "fetch", nil, map[string]string{"output": out}, err)
}

func (s *Server) repoPull(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	var in struct{ Rebase bool }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := netCtx(r)
	defer cancel()
	out, err := repo.Pull(ctx, in.Rebase)
	s.done(w, r, ws, repo, "pull", map[string]any{"rebase": in.Rebase}, map[string]string{"output": out}, err)
}

func (s *Server) repoStatus(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	st, err := repo.Status(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) repoInfo(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, err := repo.Provider(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	info, err := p.Info(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"provider": p.Kind(), "defaultBranch": info.DefaultBranch, "webUrl": info.WebURL, "canPush": info.CanPush})
}

func (s *Server) repoBranches(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	list, err := repo.Branches(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) repoCheckout(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	var in struct {
		Branch, Start string
		Create        bool
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	out, err := repo.Checkout(r.Context(), in.Branch, in.Create, in.Start)
	s.done(w, r, ws, repo, "checkout", map[string]any{"branch": in.Branch, "create": in.Create}, map[string]string{"output": out}, err)
}

func (s *Server) repoFiles(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	t, err := repo.Files(r.Context(), r.URL.Query().Get("ref"), 0)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) repoFileGet(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	c, err := repo.Read(r.Context(), q.Get("path"), q.Get("ref"), 2<<20)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) repoFilePut(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	var in struct{ Path, Content string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	err := repo.Write(in.Path, in.Content)
	s.done(w, r, ws, repo, "write_file", map[string]any{"path": in.Path, "bytes": len(in.Content)}, map[string]bool{"ok": true}, err)
}

func (s *Server) repoFileDelete(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	s.done(w, r, ws, repo, "delete_file", map[string]any{"path": p}, map[string]bool{"ok": true}, repo.Delete(p))
}

func (s *Server) repoDiff(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	var paths []string
	if p := r.URL.Query().Get("path"); p != "" {
		paths = []string{p}
	}
	d, trunc, err := repo.Diff(r.Context(), paths, 0)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"diff": d, "truncated": trunc})
}

func (s *Server) repoLog(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	list, err := repo.Log(r.Context(), q.Get("ref"), q.Get("path"), n)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) repoShow(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	out, trunc, err := repo.Show(r.Context(), r.PathValue("rev"), 0)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"diff": out, "truncated": trunc})
}

func (s *Server) repoCommit(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	var in struct {
		Message string
		Paths   []string
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	out, err := repo.Commit(r.Context(), gitx.CommitOptions{Message: in.Message, Paths: in.Paths})
	s.done(w, r, ws, repo, "commit", map[string]any{"message": in.Message, "paths": in.Paths}, map[string]string{"output": out}, err)
}

func (s *Server) repoPush(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	var in struct {
		Branch string
		Force  bool
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := netCtx(r)
	defer cancel()
	out, err := repo.Push(ctx, gitx.PushOptions{Branch: in.Branch, Force: in.Force})
	s.done(w, r, ws, repo, "push", map[string]any{"branch": firstNonEmpty(in.Branch, repo.CurrentBranch(r.Context())), "force": in.Force},
		map[string]string{"output": out}, err)
}

func (s *Server) repoDiscard(w http.ResponseWriter, r *http.Request) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	var in struct{ Paths []string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	out, err := repo.Discard(r.Context(), in.Paths)
	s.done(w, r, ws, repo, "discard", map[string]any{"paths": in.Paths}, map[string]string{"output": out}, err)
}

func (s *Server) provider(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, *gitx.Repo, gitx.Provider, context.Context, context.CancelFunc, bool) {
	ws, repo, ok := s.repo(w, r)
	if !ok {
		return nil, nil, nil, nil, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	p, err := repo.Provider(ctx)
	if err != nil {
		cancel()
		fail(w, err)
		return nil, nil, nil, nil, nil, false
	}
	return ws, repo, p, ctx, cancel, true
}

func mrID(r *http.Request) int {
	id, _ := strconv.Atoi(strings.TrimPrefix(r.PathValue("id"), "!"))
	return id
}

func (s *Server) mrList(w http.ResponseWriter, r *http.Request) {
	_, _, p, ctx, cancel, ok := s.provider(w, r)
	if !ok {
		return
	}
	defer cancel()
	list, err := p.MRs(ctx, r.URL.Query().Get("state"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"provider": p.Kind(), "items": list})
}

func (s *Server) mrGet(w http.ResponseWriter, r *http.Request) {
	_, _, p, ctx, cancel, ok := s.provider(w, r)
	if !ok {
		return
	}
	defer cancel()
	mr, err := p.MR(ctx, mrID(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, mr)
}

func (s *Server) mrCreate(w http.ResponseWriter, r *http.Request) {
	ws, repo, p, ctx, cancel, ok := s.provider(w, r)
	if !ok {
		return
	}
	defer cancel()
	var in struct {
		Title, Description, Source, Target string
		Draft, RemoveSource                bool
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.Source == "" {
		in.Source = repo.CurrentBranch(ctx)
	}
	if in.Target == "" {
		in.Target = repo.DefaultBranch(ctx)
	}
	mr, err := p.CreateMR(ctx, gitx.NewMR{Title: in.Title, Description: in.Description, Source: in.Source, Target: in.Target, Draft: in.Draft, RemoveSource: in.RemoveSource})
	s.done(w, r, ws, repo, "mr_create", map[string]any{"title": in.Title, "source": in.Source, "target": in.Target}, mr, err)
}

func (s *Server) mrComment(w http.ResponseWriter, r *http.Request) {
	ws, repo, p, ctx, cancel, ok := s.provider(w, r)
	if !ok {
		return
	}
	defer cancel()
	var in struct{ Body string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	err := p.Comment(ctx, mrID(r), in.Body)
	s.done(w, r, ws, repo, "mr_comment", map[string]any{"id": mrID(r)}, map[string]bool{"ok": true}, err)
}

func (s *Server) mrMerge(w http.ResponseWriter, r *http.Request) {
	ws, repo, p, ctx, cancel, ok := s.provider(w, r)
	if !ok {
		return
	}
	defer cancel()
	var in struct {
		Method       string
		RemoveSource bool
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	mr, err := p.Merge(ctx, mrID(r), in.Method, in.RemoveSource)
	s.done(w, r, ws, repo, "mr_merge", map[string]any{"id": mrID(r), "method": in.Method}, mr, err)
}

// renameWorkingCopy follows a GitRemote rename (a folder of its own,
// spec.path, stays where it is).
func renameWorkingCopy(ws *workspace.Workspace, oldName, newName string) {
	if oldName == newName || ws.RepoExternal(newName) {
		return
	}
	from, to := ws.RepoDir(oldName), ws.RepoDir(newName)
	if _, err := os.Stat(from); err != nil {
		return
	}
	if _, err := os.Stat(to); err == nil {
		return
	}
	os.Rename(from, to)
}
