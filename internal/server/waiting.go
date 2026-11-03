package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/agent"
	"opsdeck/internal/sysproc"
)

// What agents wait for the user to decide, across workspaces and sessions:
// the UI shows each one wherever the user is (with Approve and Deny), and
// opsdeck notifies the desktop when no opsdeck window has the focus, so an
// approval is not missed — above all one an outside agent (Claude Code over
// MCP) waits for while the user is in its terminal.

// presence is whether an opsdeck window has the focus. The UI reports it
// when it changes and every 30 s while focused; a report older than 75 s
// counts as no focus (a window closed without saying so).
type presence struct {
	mu      sync.Mutex
	focused bool
	at      time.Time
}

func (p *presence) set(focused bool) {
	p.mu.Lock()
	p.focused, p.at = focused, time.Now()
	p.mu.Unlock()
}

func (p *presence) watched() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.focused && time.Since(p.at) < 75*time.Second
}

// Replaced in tests.
var desktopNotify = sysproc.Notify

func (s *Server) waitingRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/waiting", u(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"items": s.agents.Waiting()})
	}))
	mux.HandleFunc("GET /api/waiting/stream", u(s.waitingStream))
	mux.HandleFunc("POST /api/presence", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Focused bool }
		readJSON(r, &in)
		s.presence.set(in.Focused)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}

// waitingStream sends the whole list on connect and whenever it changes.
func (s *Server) waitingStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	ch, stop := s.agents.WatchWaiting()
	defer stop()
	sseHeaders(w)
	send := func() {
		b, _ := json.Marshal(map[string]any{"items": s.agents.Waiting()})
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	send()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			send()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// notifyWaiting shows a desktop notification for an approval or a question,
// unless the user looks at opsdeck (then the in-app card is enough) or
// turned notifications off.
func (s *Server) notifyWaiting(wt agent.Waiting) {
	if s.cfg.Settings().Agent.NoNotifications || s.presence.watched() {
		return
	}
	title, body := waitingText(wt)
	desktopNotify(title, body)
}

func waitingText(wt agent.Waiting) (title, body string) {
	who := wt.Client
	if who == "" {
		who = "The agent"
	}
	where := wt.Workspace
	if wt.SessionTitle != "" && wt.Client == "" {
		where += " · " + wt.SessionTitle
	}
	if wt.Kind == "question" {
		return who + " asks you", clip(wt.Question, 160) + "\n" + where
	}
	what := wt.Tool
	if wt.Target != "" {
		what += " on " + wt.Target
	}
	if wt.Environment != "" && wt.Environment != "internet" {
		what += " (" + wt.Environment + ")"
	}
	risk := ""
	if wt.Risk == "destructive" {
		risk = " ⚠ destructive"
	}
	title = who + " waits for your approval" + risk
	body = what
	if wt.Reason != "" {
		body += "\n" + clip(wt.Reason, 140)
	}
	return title, body + "\n" + where
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
