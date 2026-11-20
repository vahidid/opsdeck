package server

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/workspace"
)

// Hosts: reachability, stats, exec and the file manager over a host's path.

func (s *Server) hostRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST /api/workspaces/{ws}/hosts/{host}/check", u(s.hostCheck))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/stats", u(s.hostStats))
	mux.HandleFunc("POST /api/workspaces/{ws}/hosts/{host}/exec", u(s.hostExec))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/files", u(s.filesList))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/files/content", u(s.fileGet))
	mux.HandleFunc("PUT /api/workspaces/{ws}/hosts/{host}/files/content", u(s.filePut))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/files/download", u(s.fileDownload))
	mux.HandleFunc("POST /api/workspaces/{ws}/hosts/{host}/files/upload", u(s.fileUpload))
	mux.HandleFunc("GET /api/workspaces/{ws}/hosts/{host}/terminal", u(s.terminal))
	mux.HandleFunc("DELETE /api/workspaces/{ws}/known-hosts", u(s.forgetHostKey))
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

// fileDownload streams a file of a host to the browser.
func (s *Server) fileDownload(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, 400, "path is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Hour)
	defer cancel()
	n, err := s.hub.Download(ctx, ws, h, p, func(size int64) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base("/" + p)}))
		w.Header().Set("Cache-Control", "no-store")
		if size >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		}
	}, w)
	s.record(r, ws.Name(), "host.download", h.ID(), map[string]any{"path": p, "bytes": n}, err)
	if err != nil && n == 0 {
		for _, k := range []string{"Content-Disposition", "Content-Length"} {
			w.Header().Del(k)
		}
		w.Header().Set("Content-Type", "application/json")
		fail(w, err)
	}
}

// fileUpload streams uploaded files (multipart) into a directory of a host.
func (s *Server) fileUpload(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "" {
		writeErr(w, 400, "path is required")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "expected a multipart upload")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Hour)
	defer cancel()
	saved := []string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		name := path.Base("/" + strings.ReplaceAll(part.FileName(), "\\", "/"))
		if part.FileName() == "" || name == "/" || name == ".." {
			part.Close()
			continue
		}
		target := path.Join(dir, name)
		n, err := s.hub.Upload(ctx, ws, h, target, part)
		part.Close()
		s.record(r, ws.Name(), "host.upload", h.ID(), map[string]any{"path": target, "bytes": n}, err)
		if err != nil {
			fail(w, fmt.Errorf("%s: %w", name, err))
			return
		}
		saved = append(saved, name)
	}
	writeJSON(w, 200, map[string]any{"saved": saved})
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
