package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/server"
)

// opsdeck run: one agent turn without the UI, for CI and cron. It uses the
// running opsdeck (its vault must be unlocked, or OPSDECK_VAULT_PASSWORD
// set), or starts one inside this process. The session is headless:
// nothing waits for an approval — what would ask is denied, so allow what
// the job needs with workspace permission rules or the mode.

func runUsage(fs *flag.FlagSet) {
	fmt.Fprintf(os.Stderr, `Usage: opsdeck run -p "prompt" --ws <workspace> [flags]

Runs the agent headless: nobody approves, so a call that would ask is denied
(allow what the job needs with workspace permission rules, or a mode).

Flags:
`)
	fs.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Exit codes: 0 done, 1 the run ended with an error, 2 bad usage, 3 an action was denied.
`)
}

type runOpts struct {
	prompt, ws, mode, model, session, output string
	port                                     int
	timeout                                  time.Duration
	waitTasks                                bool
}

func runCommand(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var o runOpts
	fs.StringVar(&o.prompt, "p", "", "the prompt (\"-\" reads it from stdin)")
	fs.StringVar(&o.ws, "ws", "", "workspace name")
	fs.StringVar(&o.mode, "mode", "ask", "plan, ask, auto or full")
	fs.StringVar(&o.model, "model", "", "model id (<provider>/<model>; default: the workspace's)")
	fs.StringVar(&o.session, "session", "", "continue this session instead of starting one")
	fs.StringVar(&o.output, "output", "text", "text, json (one object at the end) or stream-json (every event)")
	fs.IntVar(&o.port, "port", 7788, "port of a running opsdeck")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Minute, "stop the run after this long")
	fs.BoolVar(&o.waitTasks, "wait-tasks", true, "keep going while the session's background tasks run")
	fs.Usage = func() { runUsage(fs) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if o.prompt == "-" {
		b, _ := io.ReadAll(os.Stdin)
		o.prompt = string(b)
	}
	o.prompt = strings.TrimSpace(o.prompt)
	if o.prompt == "" || o.ws == "" {
		runUsage(fs)
		return 2
	}
	switch o.output {
	case "text", "json", "stream-json":
	default:
		fmt.Fprintln(os.Stderr, "opsdeck run: --output must be text, json or stream-json")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	c, closeFn, err := connectServer(ctx, o.port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opsdeck run:", err)
		return 1
	}
	defer closeFn()
	code, err := c.run(ctx, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opsdeck run:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

type apiClient struct {
	base, token string
	hc          *http.Client
}

func (c *apiClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		var e struct{ Error string }
		json.Unmarshal(b, &e)
		return fmt.Errorf("%s %s: %s", method, path, firstNonEmpty(e.Error, res.Status))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// connectServer reaches the running opsdeck, or starts one in this process
// on a free port. Either way the vault ends up unlocked or it fails.
func connectServer(ctx context.Context, port int) (*apiClient, func(), error) {
	dirs := paths.Default()
	tok, _ := os.ReadFile(filepath.Join(dirs.State, "server.token"))
	c := &apiClient{base: "http://127.0.0.1:" + strconv.Itoa(port), token: strings.TrimSpace(string(tok)), hc: &http.Client{}}
	closeFn := func() {}
	if c.token == "" || c.do(ctx, "GET", "/api/auth/check", nil, nil) != nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, err
		}
		p := ln.Addr().(*net.TCPAddr).Port
		srv, err := server.New(dirs, server.Options{Port: p, Version: version})
		if err != nil {
			ln.Close()
			return nil, nil, err
		}
		hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go hs.Serve(ln)
		tok, _ := os.ReadFile(filepath.Join(dirs.State, "server.token"))
		c = &apiClient{base: "http://127.0.0.1:" + strconv.Itoa(p), token: strings.TrimSpace(string(tok)), hc: &http.Client{}}
		closeFn = func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			hs.Shutdown(sctx)
			srv.Close()
		}
	}
	var st struct{ Initialized, Unlocked bool }
	if err := c.do(ctx, "GET", "/api/vault/status", nil, &st); err != nil {
		closeFn()
		return nil, nil, err
	}
	if !st.Initialized {
		closeFn()
		return nil, nil, errors.New("opsdeck is not set up yet: open it once and create the vault")
	}
	if !st.Unlocked {
		pw := os.Getenv("OPSDECK_VAULT_PASSWORD")
		if pw == "" {
			closeFn()
			return nil, nil, errors.New("the vault is locked: unlock opsdeck, or set OPSDECK_VAULT_PASSWORD")
		}
		if err := c.do(ctx, "POST", "/api/vault/unlock", map[string]string{"password": pw}, nil); err != nil {
			closeFn()
			return nil, nil, err
		}
	}
	return c, closeFn, nil
}

type runSummary struct {
	Session    string   `json:"session"`
	Workspace  string   `json:"workspace"`
	Result     string   `json:"result"`
	ToolCalls  int      `json:"toolCalls"`
	Denied     int      `json:"denied"`
	Errors     []string `json:"errors"`
	Usage      any      `json:"usage"`
	DurationMs int64    `json:"durationMs"`
}

func (c *apiClient) run(ctx context.Context, o runOpts) (int, error) {
	start := time.Now()
	ws := "/api/workspaces/" + o.ws
	var sess struct {
		ID       string
		Headless bool
		Usage    any
	}
	restore := func() {}
	if o.session != "" {
		if err := c.do(ctx, "GET", ws+"/sessions/"+o.session, nil, &struct{ Session any }{&sess}); err != nil {
			return 1, err
		}
		was := sess.Headless
		on := true
		body := map[string]any{"headless": &on, "mode": o.mode, "model": o.model}
		if err := c.do(ctx, "PUT", ws+"/sessions/"+o.session, body, &sess); err != nil {
			return 1, err
		}
		restore = func() {
			c.do(context.Background(), "PUT", ws+"/sessions/"+o.session, map[string]any{"headless": &was}, nil)
		}
	} else if err := c.do(ctx, "POST", ws+"/sessions", map[string]any{"mode": o.mode, "model": o.model, "headless": true}, &sess); err != nil {
		return 1, err
	}
	defer restore()
	base := ws + "/sessions/" + sess.ID

	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+base+"/stream", nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.hc.Do(req)
	if err != nil {
		return 1, err
	}
	defer res.Body.Close()
	events := make(chan map[string]any, 1024)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev map[string]any
				if json.Unmarshal([]byte(d), &ev) == nil {
					events <- ev
				}
			}
		}
	}()
	// the stream starts with the session's state: subscribed, so no event
	// of the turn can be missed
	select {
	case <-events:
	case <-time.After(15 * time.Second):
		return 1, errors.New("no answer from the event stream")
	case <-ctx.Done():
		return 1, ctx.Err()
	}
	if err := c.do(ctx, "POST", base+"/messages", map[string]string{"text": o.prompt}, nil); err != nil {
		return 1, err
	}

	sum := runSummary{Session: sess.ID, Workspace: o.ws, Errors: []string{}}
	out := json.NewEncoder(os.Stdout)
	streaming := false
	running := false // until the turn reports running
	str := func(v any) string { s, _ := v.(string); return s }
	for {
		var ev map[string]any
		select {
		case ev = <-events:
			if ev == nil {
				return 1, errors.New("the event stream ended")
			}
		case <-ctx.Done():
			c.do(context.Background(), "POST", base+"/interrupt", nil, nil)
			return 1, errors.New("stopped: " + context.Cause(ctx).Error())
		}
		typ := str(ev["type"])
		if o.output == "stream-json" && typ != "status" {
			out.Encode(ev)
		}
		_, nested := ev["parent"]
		switch typ {
		case "assistant_delta":
			if o.output == "text" && !nested {
				fmt.Print(str(ev["text"]))
				streaming = true
			}
		case "assistant":
			if !nested {
				sum.Result = str(ev["text"])
				if o.output == "text" {
					if !streaming {
						fmt.Print(sum.Result)
					}
					fmt.Println()
					streaming = false
				}
			}
		case "tool_call":
			sum.ToolCalls++
			if o.output == "text" {
				in, _ := json.Marshal(ev["input"])
				fmt.Fprintf(os.Stderr, "→ %s %s %s\n", str(ev["tool"]), str(ev["target"]), oneLine(string(in), 160))
			}
		case "tool_result":
			if o.output == "text" {
				out := str(ev["output"])
				if isErr, _ := ev["isError"].(bool); isErr {
					if strings.HasPrefix(out, "Denied: ") {
						sum.Denied++
					}
					fmt.Fprintf(os.Stderr, "  ✗ %s\n", oneLine(out, 300))
				}
			} else if isErr, _ := ev["isError"].(bool); isErr && strings.HasPrefix(str(ev["output"]), "Denied: ") {
				sum.Denied++
			}
		case "notification":
			if o.output == "text" {
				fmt.Fprintf(os.Stderr, "• %s\n", oneLine(str(ev["text"]), 300))
			}
		case "error":
			sum.Errors = append(sum.Errors, str(ev["message"]))
			if o.output == "text" {
				fmt.Fprintln(os.Stderr, "error:", str(ev["message"]))
			}
		case "status":
			sum.Usage = ev["usage"]
			switch str(ev["state"]) {
			case "idle":
				if !running {
					continue
				}
				if o.waitTasks && c.tasksRunning(ctx, base) {
					running = false
					continue
				}
				sum.DurationMs = time.Since(start).Milliseconds()
				if o.output == "json" {
					out.Encode(sum)
				}
				switch {
				case len(sum.Errors) > 0:
					return 1, nil
				case sum.Denied > 0:
					return 3, nil
				}
				return 0, nil
			default:
				running = true
			}
		}
	}
}

func (c *apiClient) tasksRunning(ctx context.Context, base string) bool {
	var tasks []struct{ State string }
	if c.do(ctx, "GET", base+"/tasks", nil, &tasks) != nil {
		return false
	}
	for _, t := range tasks {
		if t.State == "running" {
			return true
		}
	}
	return false
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
