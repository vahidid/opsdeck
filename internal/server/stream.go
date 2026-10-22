package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"opsdeck/internal/audit"
	"opsdeck/internal/connect"
	"opsdeck/internal/docker"
	"opsdeck/internal/workspace"
)

func auditEntry(action, target string, detail map[string]any) audit.Entry {
	return audit.Entry{Actor: "user", Action: action, Target: target, Detail: detail}
}

func auditEntryResult(action, result string) audit.Entry {
	return audit.Entry{Actor: "user", Action: action, Result: result}
}

// ---------------------------------------------------------------------------
// SSH terminal over WebSocket

type termMsg struct {
	T    string `json:"t"`
	D    string `json:"d,omitempty"`
	C    int    `json:"c,omitempty"`
	R    int    `json:"r,omitempty"`
	Code int    `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`
}

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	ws, h, ok := s.hostRes(w, r)
	if !ok {
		return
	}
	s.ptyWS(w, r, ws, h, "host.terminal", h.ID(), func(ctx context.Context, cols, rows int) (*connect.Shell, error) {
		return s.hub.OpenShell(ctx, ws, h, cols, rows)
	})
}

// ptyWS serves an interactive PTY session (a host shell, docker exec…) over
// a WebSocket: output as {t:"d"}, input and resizes from the browser.
func (s *Server) ptyWS(w http.ResponseWriter, r *http.Request, ws *workspace.Workspace, h *workspace.Resource, action, target string,
	open func(ctx context.Context, cols, rows int) (*connect.Shell, error)) {
	up := websocket.Upgrader{CheckOrigin: func(req *http.Request) bool {
		o := req.Header.Get("Origin")
		return o == "" || s.originOK(o, req.Host)
	}}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var wmu sync.Mutex
	send := func(m termMsg) error {
		wmu.Lock()
		defer wmu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return conn.WriteJSON(m)
	}
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if path, _ := ws.Chain(h); len(path) > 0 {
		var names []string
		for _, p := range path {
			names = append(names, p.ID())
		}
		send(termMsg{T: "s", Msg: "connecting via " + strings.Join(names, " → ") + " …"})
	} else {
		send(termMsg{T: "s", Msg: "connecting to " + h.Str("address") + " …"})
	}
	octx, ocancel := context.WithTimeout(ctx, 60*time.Second)
	shell, err := open(octx, cols, rows)
	ocancel()
	s.record(r, ws.Name(), action, target, nil, err)
	if err != nil {
		send(termMsg{T: "e", Msg: err.Error()})
		return
	}
	defer shell.Close()

	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		var pending []byte
		for {
			n, err := shell.Stdout.Read(buf)
			if n > 0 {
				data := append(pending, buf[:n]...)
				// Hold back an incomplete trailing UTF-8 sequence.
				cut := len(data)
				for i := len(data) - 1; i >= 0 && i >= len(data)-3; i-- {
					if utf8.RuneStart(data[i]) {
						if !utf8.FullRune(data[i:]) {
							cut = i
						}
						break
					}
				}
				pending = append([]byte(nil), data[cut:]...)
				if send(termMsg{T: "d", D: strings.ToValidUTF8(string(data[:cut]), "�")}) != nil {
					return
				}
			}
			if err != nil {
				code := 0
				if werr := shell.Wait(); werr != nil {
					code = 1
				}
				send(termMsg{T: "x", Code: code})
				return
			}
		}
	}()
	go func() {
		defer cancel()
		for {
			var m termMsg
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			switch m.T {
			case "d":
				shell.Stdin.Write([]byte(m.D))
			case "r":
				if m.C > 0 && m.R > 0 {
					shell.Resize(m.C, m.R)
				}
			}
			s.vault.Touch()
		}
	}()
	<-ctx.Done()
}

// ---------------------------------------------------------------------------
// Log streaming (SSE)

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (s *Server) logStream(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	h := ws.Find("Host", q.Get("host"))
	if h == nil {
		writeErr(w, 404, "host not found")
		return
	}
	lines, _ := strconv.Atoi(q.Get("lines"))
	if lines <= 0 || lines > 20000 {
		lines = 200
	}
	follow := q.Get("follow") == "1" || q.Get("follow") == "true"
	var cmd string
	var dockerSrc *docker.LogSource
	switch q.Get("source") {
	case "journal", "":
		cmd = fmt.Sprintf("journalctl --no-pager -o short-iso -n %d", lines)
		if u := q.Get("unit"); u != "" {
			cmd += " -u " + shq(u)
		}
		if follow {
			cmd += " -f"
		}
	case "file":
		p := q.Get("path")
		if p == "" {
			writeErr(w, 400, "path is required")
			return
		}
		cmd = fmt.Sprintf("tail -n %d", lines)
		if follow {
			cmd += " -F"
		}
		cmd += " -- " + shq(p)
	case "docker", "compose", "service", "events":
		src := docker.LogSource{Kind: q.Get("source"), Target: q.Get("container"), Service: q.Get("service"), Tail: lines, Since: q.Get("since"), Follow: follow}
		switch src.Kind {
		case "docker":
			src.Kind = "container"
		case "compose":
			src.Target = q.Get("project")
		case "service":
			src.Target = q.Get("service")
			src.Service = ""
		}
		if q.Get("lines") == "all" {
			src.Tail = -1
		}
		c, err := s.docker.LogsCommand(r.Context(), ws, h, src)
		if err != nil {
			fail(w, err)
			return
		}
		dockerSrc = &src
		cmd = c
	default:
		writeErr(w, 400, "source must be journal, file, docker, compose, service or events")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	sseHeaders(w)
	flusher.Flush()
	var mu sync.Mutex
	write := func(event string, v any) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(v)
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	ctx := r.Context()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				mu.Lock()
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
				mu.Unlock()
			}
		}
	}()
	var err error
	switch {
	case dockerSrc != nil && dockerSrc.Kind == "events":
		err = s.hub.ExecStream(ctx, ws, h, cmd, func(line string) {
			if e, ok := docker.ParseEvent(line); ok {
				write("", map[string]any{"event": e})
			}
		})
	case dockerSrc != nil:
		kind := dockerSrc.Kind
		err = s.hub.ExecStreams(ctx, ws, h, cmd, func(stderr bool, line string) {
			write("", docker.ParseLogLine(kind, line, stderr))
		})
	default:
		err = s.hub.ExecStream(ctx, ws, h, cmd, func(line string) {
			write("", map[string]string{"line": line})
		})
	}
	close(done)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		// a live docker stream that broke (SSH or VPN dropped) can resume
		// from its last line; the viewer reconnects with since=<last ts>
		write("error", map[string]any{"message": err.Error(), "retry": dockerSrc != nil && follow})
		return
	}
	write("end", map[string]bool{"ok": true})
}
