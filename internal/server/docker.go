package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"opsdeck/internal/connect"
	"opsdeck/internal/workspace"
)

func (s *Server) dockerRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	D := "/api/workspaces/{ws}/hosts/{host}/docker"
	get := func(p string, fn dockerFn) { mux.HandleFunc("GET "+D+p, u(s.dockerRead(fn))) }
	do := func(method, p, action string, fn dockerFn) {
		mux.HandleFunc(method+" "+D+p, u(s.dockerChange(action, fn)))
	}

	// engine
	get("/info", func(c *dctx) (any, error) { return s.docker.Info(c.ctx, c.ws, c.h) })
	get("/df", func(c *dctx) (any, error) { return s.docker.DiskUsage(c.ctx, c.ws, c.h) })
	do("POST", "/prune", "docker.prune", func(c *dctx) (any, error) {
		var in struct{ Kind string }
		if err := c.body(&in); err != nil {
			return nil, err
		}
		c.target(in.Kind)
		return output(s.docker.Prune(c.ctx, c.ws, c.h, in.Kind))
	})
	get("/inspect/{kind}/{id}", func(c *dctx) (any, error) {
		return s.docker.InspectKind(c.ctx, c.ws, c.h, c.r.PathValue("kind"), c.r.PathValue("id"))
	})

	// containers
	get("/containers", func(c *dctx) (any, error) { return s.docker.Containers(c.ctx, c.ws, c.h, c.q("all") == "1") })
	get("/containers/{id}/inspect", func(c *dctx) (any, error) { return s.docker.Inspect(c.ctx, c.ws, c.h, c.r.PathValue("id")) })
	get("/containers/{id}/top", func(c *dctx) (any, error) { return s.docker.Top(c.ctx, c.ws, c.h, c.r.PathValue("id")) })
	get("/stats", func(c *dctx) (any, error) {
		var ids []string
		if v := c.q("ids"); v != "" {
			ids = strings.Split(v, ",")
		}
		return s.docker.Stats(c.ctx, c.ws, c.h, ids...)
	})
	mux.HandleFunc("GET "+D+"/containers/{id}/exec", u(s.dockerExec))
	do("POST", "/containers/{id}/{action}", "", func(c *dctx) (any, error) {
		id, action := c.r.PathValue("id"), c.r.PathValue("action")
		c.action, c.tgt = "docker."+action, c.h.ID()+"/"+id
		return ok(s.docker.Action(c.ctx, c.ws, c.h, id, action))
	})

	// images
	get("/images", func(c *dctx) (any, error) { return s.docker.Images(c.ctx, c.ws, c.h) })
	do("POST", "/images/pull", "docker.image.pull", func(c *dctx) (any, error) {
		var in struct{ Ref string }
		if err := c.body(&in); err != nil {
			return nil, err
		}
		c.target(in.Ref)
		return output(s.docker.ImagePull(c.ctx, c.ws, c.h, in.Ref))
	})
	do("DELETE", "/images", "docker.image.rm", func(c *dctx) (any, error) {
		c.target(c.q("ref"))
		return ok(s.docker.ImageRemove(c.ctx, c.ws, c.h, c.q("ref"), c.q("force") == "1"))
	})

	// networks
	get("/networks", func(c *dctx) (any, error) { return s.docker.Networks(c.ctx, c.ws, c.h) })
	do("DELETE", "/networks/{id}", "docker.network.rm", func(c *dctx) (any, error) {
		c.target(c.r.PathValue("id"))
		return ok(s.docker.NetworkRemove(c.ctx, c.ws, c.h, c.r.PathValue("id")))
	})

	// volumes
	get("/volumes", func(c *dctx) (any, error) { return s.docker.Volumes(c.ctx, c.ws, c.h) })
	do("POST", "/volumes", "docker.volume.create", func(c *dctx) (any, error) {
		var in struct{ Name string }
		if err := c.body(&in); err != nil {
			return nil, err
		}
		c.target(in.Name)
		return ok(s.docker.VolumeCreate(c.ctx, c.ws, c.h, in.Name))
	})
	do("DELETE", "/volumes/{name}", "docker.volume.rm", func(c *dctx) (any, error) {
		c.target(c.r.PathValue("name"))
		return ok(s.docker.VolumeRemove(c.ctx, c.ws, c.h, c.r.PathValue("name")))
	})
	get("/volumes/{name}/size", func(c *dctx) (any, error) {
		n, err := s.docker.VolumeSize(c.ctx, c.ws, c.h, c.r.PathValue("name"))
		return map[string]int64{"bytes": n}, err
	})
	get("/volumes/{name}/files", func(c *dctx) (any, error) {
		mode, entries, err := s.docker.VolumeList(c.ctx, c.ws, c.h, c.r.PathValue("name"), c.q("path"))
		return map[string]any{"access": mode, "entries": entries}, err
	})
	mux.HandleFunc("GET "+D+"/volumes/{name}/file", u(s.volumeFileGet))
	mux.HandleFunc("PUT "+D+"/volumes/{name}/file", u(s.volumeFilePut))
	mux.HandleFunc("POST "+D+"/volumes/{name}/upload", u(s.volumeUpload))
	do("POST", "/volumes/{name}/mkdir", "docker.volume.mkdir", func(c *dctx) (any, error) {
		var in struct{ Path, Name string }
		if err := c.body(&in); err != nil {
			return nil, err
		}
		c.target(c.r.PathValue("name") + ":/" + path.Join(in.Path, in.Name))
		return ok(s.docker.VolumeMkdir(c.ctx, c.ws, c.h, c.r.PathValue("name"), in.Path, in.Name))
	})
	do("DELETE", "/volumes/{name}/file", "docker.volume.delete", func(c *dctx) (any, error) {
		c.target(c.r.PathValue("name") + ":/" + strings.TrimPrefix(c.q("path"), "/"))
		return ok(s.docker.VolumeDelete(c.ctx, c.ws, c.h, c.r.PathValue("name"), c.q("path")))
	})

	// compose
	get("/compose", func(c *dctx) (any, error) { return s.docker.ComposeProjects(c.ctx, c.ws, c.h) })
	do("POST", "/compose", "docker.compose.create", func(c *dctx) (any, error) {
		var in struct {
			Name, Dir, Content string
			Start              bool
		}
		if err := c.body(&in); err != nil {
			return nil, err
		}
		c.target(in.Name)
		return output(s.docker.ComposeCreate(c.ctx, c.ws, c.h, in.Name, in.Dir, in.Content, in.Start))
	})
	get("/compose/{project}", func(c *dctx) (any, error) { return s.docker.ComposePS(c.ctx, c.ws, c.h, c.r.PathValue("project")) })
	get("/compose/{project}/files", func(c *dctx) (any, error) {
		return s.docker.ComposeFiles(c.ctx, c.ws, c.h, c.r.PathValue("project"))
	})
	get("/compose/{project}/file", func(c *dctx) (any, error) {
		content, err := s.docker.ComposeReadFile(c.ctx, c.ws, c.h, c.r.PathValue("project"), c.q("path"))
		return map[string]string{"path": c.q("path"), "content": content}, err
	})
	do("PUT", "/compose/{project}/file", "docker.compose.file", func(c *dctx) (any, error) {
		var in struct {
			Path, Content string
			Deploy        bool
		}
		if err := c.body(&in); err != nil {
			return nil, err
		}
		c.target(c.r.PathValue("project") + " " + in.Path)
		return output(s.docker.ComposeWriteFile(c.ctx, c.ws, c.h, c.r.PathValue("project"), in.Path, in.Content, in.Deploy))
	})
	do("POST", "/compose/{project}/{action}", "", func(c *dctx) (any, error) {
		var in struct{ Services []string }
		if err := c.body(&in); err != nil {
			return nil, err
		}
		project, action := c.r.PathValue("project"), c.r.PathValue("action")
		c.action, c.tgt = "docker.compose."+action, c.h.ID()+"/compose/"+project
		return output(s.docker.ComposeAction(c.ctx, c.ws, c.h, project, action, in.Services))
	})

	// swarm
	get("/swarm", func(c *dctx) (any, error) { return s.docker.Swarm(c.ctx, c.ws, c.h) })
	get("/services", func(c *dctx) (any, error) { return s.docker.Services(c.ctx, c.ws, c.h) })
	get("/services/{name}/tasks", func(c *dctx) (any, error) { return s.docker.Tasks(c.ctx, c.ws, c.h, c.r.PathValue("name"), false) })
	do("POST", "/services/{name}/{action}", "", func(c *dctx) (any, error) {
		var in struct{ Arg string }
		if err := c.body(&in); err != nil {
			return nil, err
		}
		name, action := c.r.PathValue("name"), c.r.PathValue("action")
		c.action, c.tgt = "docker.service."+action, c.h.ID()+"/service/"+name
		c.detail["arg"] = in.Arg
		return output(s.docker.ServiceAction(c.ctx, c.ws, c.h, name, action, in.Arg))
	})
	get("/stacks", func(c *dctx) (any, error) { return s.docker.Stacks(c.ctx, c.ws, c.h) })
	get("/stacks/{name}/tasks", func(c *dctx) (any, error) { return s.docker.Tasks(c.ctx, c.ws, c.h, c.r.PathValue("name"), true) })
	do("POST", "/stacks", "docker.stack.deploy", func(c *dctx) (any, error) {
		var in struct {
			Name, File, Content string
			Prune               bool
		}
		if err := c.body(&in); err != nil {
			return nil, err
		}
		c.target(in.Name)
		if in.File == "" && strings.TrimSpace(in.Content) == "" {
			return nil, errors.New("give a compose file on the host or its content")
		}
		return output(s.docker.StackDeploy(c.ctx, c.ws, c.h, in.Name, in.File, strings.NewReader(in.Content), in.Prune))
	})
	do("DELETE", "/stacks/{name}", "docker.stack.rm", func(c *dctx) (any, error) {
		c.target(c.r.PathValue("name"))
		return output(s.docker.StackRemove(c.ctx, c.ws, c.h, c.r.PathValue("name")))
	})
	get("/nodes", func(c *dctx) (any, error) { return s.docker.Nodes(c.ctx, c.ws, c.h) })
	do("POST", "/nodes/{id}/availability", "docker.node.availability", func(c *dctx) (any, error) {
		var in struct{ Availability string }
		if err := c.body(&in); err != nil {
			return nil, err
		}
		c.target(c.r.PathValue("id") + " " + in.Availability)
		return ok(s.docker.NodeAvailability(c.ctx, c.ws, c.h, c.r.PathValue("id"), in.Availability))
	})
}

// dctx is what a docker handler works with.
type dctx struct {
	ctx    context.Context
	r      *http.Request
	ws     *workspace.Workspace
	h      *workspace.Resource
	action string
	tgt    string
	detail map[string]any
}

func (c *dctx) q(k string) string               { return c.r.URL.Query().Get(k) }
func (c *dctx) body(v any) error                { return readJSON(c.r, v) }
func (c *dctx) target(name string)              { c.tgt = c.h.ID() + "/" + name }
func output(out string, err error) (any, error) { return map[string]string{"output": out}, err }
func ok(err error) (any, error)                 { return map[string]bool{"ok": err == nil}, err }

type dockerFn func(c *dctx) (any, error)

// dockerRead serves a read: errors map to statuses, results to JSON.
func (s *Server) dockerRead(fn dockerFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, h, okk := s.hostRes(w, r)
		if !okk {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		v, err := fn(&dctx{ctx: ctx, r: r, ws: ws, h: h})
		if err != nil {
			dockerFail(w, err)
			return
		}
		if raw, isRaw := v.([]byte); isRaw {
			w.Header().Set("Content-Type", "application/json")
			w.Write(raw)
			return
		}
		writeJSON(w, 200, v)
	}
}

// dockerChange serves a change: it keeps running when the page goes away
// (a half-done compose up is worse than a finished one) and is audited.
func (s *Server) dockerChange(action string, fn dockerFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, h, okk := s.hostRes(w, r)
		if !okk {
			return
		}
		ctx, cancel := context.WithTimeout(connect.WithActor(context.WithoutCancel(r.Context()), connect.Actor{Kind: "user"}), 20*time.Minute)
		defer cancel()
		c := &dctx{ctx: ctx, r: r, ws: ws, h: h, action: action, tgt: h.ID(), detail: map[string]any{}}
		v, err := fn(c)
		s.record(r, ws.Name(), firstNonEmptyStr(c.action, action), c.tgt, c.detail, err)
		if err != nil {
			if m, isOut := v.(map[string]string); isOut && m["output"] != "" && !strings.Contains(err.Error(), m["output"]) {
				err = fmt.Errorf("%v\n%s", err, m["output"])
			}
			dockerFail(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

func firstNonEmptyStr(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// dockerFail reports docker errors as 400 (they are the host's answer, not
// a broken request), except for not-found and vault errors.
func dockerFail(w http.ResponseWriter, err error) {
	msg := err.Error()
	if strings.Contains(msg, "not found") || strings.Contains(msg, "No such") || errors.Is(err, context.DeadlineExceeded) {
		fail(w, err)
		return
	}
	if strings.Contains(msg, "vault") {
		fail(w, err)
		return
	}
	writeErr(w, 400, msg)
}

// dockerExec opens an interactive shell in a container over a WebSocket.
func (s *Server) dockerExec(w http.ResponseWriter, r *http.Request) {
	ws, h, okk := s.hostRes(w, r)
	if !okk {
		return
	}
	id := r.PathValue("id")
	q := r.URL.Query()
	s.ptyWS(w, r, ws, h, "docker.exec", h.ID()+"/"+id, func(ctx context.Context, cols, rows int) (ptySession, error) {
		cmd, err := s.docker.ExecCommand(ctx, ws, h, id, q.Get("user"), q.Get("shell"))
		if err != nil {
			return nil, err
		}
		sh, err := s.hub.OpenCommand(ctx, ws, h, cmd, cols, rows)
		if err != nil {
			return nil, err
		}
		return sshPTY{sh}, nil
	})
}

// volumeFileGet shows (up to 1 MB) or downloads a file of a volume.
func (s *Server) volumeFileGet(w http.ResponseWriter, r *http.Request) {
	ws, h, okk := s.hostRes(w, r)
	if !okk {
		return
	}
	vol, p := r.PathValue("name"), r.URL.Query().Get("path")
	if r.URL.Query().Get("download") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Hour)
		defer cancel()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base("/" + p)}))
		w.Header().Set("Cache-Control", "no-store")
		cw := &countWriter{w: w}
		err := s.docker.VolumeRead(ctx, ws, h, vol, p, 0, cw)
		s.record(r, ws.Name(), "docker.volume.download", h.ID()+"/"+vol+":/"+strings.TrimPrefix(p, "/"), map[string]any{"bytes": cw.n}, err)
		if err != nil && cw.n == 0 {
			w.Header().Del("Content-Disposition")
			w.Header().Set("Content-Type", "application/json")
			dockerFail(w, err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	const max = 1 << 20
	var buf limitedBytes
	err := s.docker.VolumeRead(ctx, ws, h, vol, p, max+1, &buf)
	if err != nil {
		dockerFail(w, err)
		return
	}
	data := buf.b
	truncated := len(data) > max
	if truncated {
		data = data[:max]
	}
	binary := !isText(data)
	content := ""
	if !binary {
		content = string(data)
	}
	writeJSON(w, 200, map[string]any{"path": p, "content": content, "binary": binary, "truncated": truncated, "size": len(data)})
}

// volumeFilePut saves a text file (the editor) in a volume.
func (s *Server) volumeFilePut(w http.ResponseWriter, r *http.Request) {
	ws, h, okk := s.hostRes(w, r)
	if !okk {
		return
	}
	var in struct{ Path, Content string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	vol := r.PathValue("name")
	dir, name := path.Split("/" + strings.TrimPrefix(in.Path, "/"))
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	err := s.docker.VolumeWrite(ctx, ws, h, vol, dir, name, strings.NewReader(in.Content))
	s.record(r, ws.Name(), "docker.volume.write", h.ID()+"/"+vol+":"+path.Join(dir, name), map[string]any{"bytes": len(in.Content)}, err)
	if err != nil {
		dockerFail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// volumeUpload streams uploaded files (multipart) into a directory of a volume.
func (s *Server) volumeUpload(w http.ResponseWriter, r *http.Request) {
	ws, h, okk := s.hostRes(w, r)
	if !okk {
		return
	}
	vol, dir := r.PathValue("name"), r.URL.Query().Get("path")
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "expected a multipart upload")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Hour)
	defer cancel()
	var saved []string
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
		if part.FileName() == "" || name == "/" {
			part.Close()
			continue
		}
		cw := &countReader{r: part}
		err = s.docker.VolumeWrite(ctx, ws, h, vol, dir, name, cw)
		part.Close()
		s.record(r, ws.Name(), "docker.volume.upload", h.ID()+"/"+vol+":"+path.Join("/", dir, name), map[string]any{"bytes": cw.n}, err)
		if err != nil {
			dockerFail(w, fmt.Errorf("%s: %w", name, err))
			return
		}
		saved = append(saved, name)
	}
	writeJSON(w, 200, map[string]any{"saved": saved})
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type limitedBytes struct{ b []byte }

func (l *limitedBytes) Write(p []byte) (int, error) {
	l.b = append(l.b, p...)
	return len(p), nil
}

// isText guesses whether data is text: UTF-8 (a character cut at the end
// is fine) without NUL bytes.
func isText(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	for i := 0; i < 4 && len(b) > 0; i++ {
		if utf8.Valid(b) {
			return true
		}
		b = b[:len(b)-1]
	}
	return utf8.Valid(b)
}
