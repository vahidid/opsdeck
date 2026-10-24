package server

import (
	"net/http"
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
