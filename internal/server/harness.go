package server

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"opsdeck/internal/agent"
)

// Background tasks and full outputs of agent sessions.

func (s *Server) sessTasks(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.agents.Tasks(r.PathValue("ws"), sess.Info().ID))
}

func (s *Server) sessTask(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	t, out, err := s.agents.Task(r.PathValue("ws"), sess.Info().ID, r.PathValue("tid"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"task": t, "output": out})
}

func (s *Server) sessTaskStop(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	err := s.agents.StopTask(r.PathValue("ws"), sess.Info().ID, r.PathValue("tid"))
	s.record(r, r.PathValue("ws"), "task.stop", r.PathValue("tid"), map[string]any{"session": sess.Info().ID}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// sessOutput is the full (redacted) output of a long tool result.
func (s *Server) sessOutput(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	out, err := sess.Output(r.PathValue("oid"))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("oid")+`.txt"`)
	}
	w.Write([]byte(out))
}

func (s *Server) sessFork(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct{ At string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	sess, text, err := s.agents.Fork(ws, r.PathValue("id"), in.At)
	s.record(r, ws.Name(), "session.fork", r.PathValue("id"), map[string]any{"at": in.At}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"session": sess.Info(), "text": text})
}

func (s *Server) sessExport(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct{ As, Title string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	path, err := s.agents.Export(r.Context(), ws, r.PathValue("id"), in.As, in.Title)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"path": path})
}

func (s *Server) sessShell(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct{ Command, Target string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	ev, err := s.agents.Shell(r.Context(), ws, r.PathValue("id"), in.Target, in.Command)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, ev)
}

func (s *Server) sessCompact(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	if err := s.agents.Compact(r.Context(), ws, r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// wsHarness is what the agent page needs to know about the workspace's
// harness: its slash commands, subagents and web access.
func (s *Server) wsHarness(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.agents.Harness(ws))
}

// sessAttach stores a file the user attaches to their next message (the
// request body; the name in ?name=).
func (s *Server) sessAttach(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.workspace(w, r); !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, agent.MaxAttachment+1)
	a, err := s.agents.AddAttachment(r.PathValue("ws"), r.PathValue("id"), r.URL.Query().Get("name"), r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("files can be up to %d MB", agent.MaxAttachment>>20))
			return
		}
		fail(w, err)
		return
	}
	writeJSON(w, 200, a)
}

// sessAttachment serves an attached file: images inline (for the chat),
// anything else as a download, text as plain text.
func (s *Server) sessAttachment(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	a, path, err := sess.Attachment(r.PathValue("aid"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, 404, "the file is gone")
		return
	}
	defer f.Close()
	ctype := a.Mime
	if a.Kind == "text" {
		ctype = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	disp := "attachment"
	if a.Kind == "image" && r.URL.Query().Get("download") == "" {
		disp = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": a.Name}))
	io.Copy(w, f)
}
