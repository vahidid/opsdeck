package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// ACP (Agent Client Protocol, agentclientprotocol.com) is JSON-RPC 2.0 over
// stdio. opsdeck is the client: it starts the agent, opens (or loads) a
// session with opsdeck's MCP bridge as the only MCP server, sends the prompt
// and turns session/update notifications into timeline events. The client
// advertises no file system or terminal capability, and permission requests
// are granted only for opsdeck's own MCP tools.

type rpcMsg struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

type acpConn struct {
	p       *process
	mu      sync.Mutex // serialises writes
	nextID  atomic.Int64
	pending sync.Map // id -> chan rpcMsg
	onNote  func(method string, params json.RawMessage)
	onReq   func(method string, params json.RawMessage) (any, error)
}

func (c *acpConn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.p.stdin.Write(append(b, '\n'))
	return err
}

func (c *acpConn) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan rpcMsg, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m, ok := <-ch:
		if !ok {
			return errors.New("agent exited")
		}
		if m.Error != nil {
			msg := m.Error.Message
			if len(m.Error.Data) > 0 && string(m.Error.Data) != "null" {
				msg += ": " + string(m.Error.Data)
			}
			return fmt.Errorf("%s: %s", method, msg)
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	}
}

// readLoop dispatches responses, notifications and agent->client requests.
func (c *acpConn) readLoop() {
	for c.p.stdout.Scan() {
		var m rpcMsg
		if json.Unmarshal(c.p.stdout.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && m.ID != nil: // request from the agent
			id := *m.ID
			go func() {
				res, err := c.onReq(m.Method, m.Params)
				if err != nil {
					c.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": err.Error()}})
					return
				}
				c.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": res})
			}()
		case m.Method != "":
			c.onNote(m.Method, m.Params)
		case m.ID != nil:
			var id int64
			if json.Unmarshal(*m.ID, &id) == nil {
				if ch, ok := c.pending.Load(id); ok {
					ch.(chan rpcMsg) <- m
				}
			}
		}
	}
	c.pending.Range(func(k, v any) bool {
		close(v.(chan rpcMsg))
		c.pending.Delete(k)
		return true
	})
}

// isOpsdeckTool recognises opsdeck MCP tools in permission requests across
// agents ("mcp__opsdeck__exec", "opsdeck_exec", "exec (opsdeck MCP Server)").
func isOpsdeckTool(title string, raw json.RawMessage) bool {
	t := strings.ToLower(title)
	return strings.Contains(t, "opsdeck")
}

func runACP(ctx context.Context, req Request, emit func(Event)) (Result, error) {
	args := append([]string(nil), req.Spec.Args...)
	env := childEnv(req.Spec.Env)
	gemini := req.Spec.Kind == GeminiCLI
	if gemini {
		args = append([]string{"--acp", "--skip-trust", "--allowed-mcp-server-names", "opsdeck"}, args...)
		if m := req.Spec.Model; m != "" && m != "default" {
			args = append(args, "--model", m)
		}
		path, err := writeGeminiSettings(req)
		if err != nil {
			return Result{}, err
		}
		defer os.Remove(path)
		env = append(env, "GEMINI_CLI_SYSTEM_SETTINGS_PATH="+path)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p, err := start(ctx, req.Spec.Command, args, env, req.WorkDir)
	if err != nil {
		return Result{}, err
	}
	defer p.stdin.Close()

	var (
		res        Result
		loading    atomic.Bool
		msgCounter atomic.Int64
		curMsg     atomic.Value
		textMu     sync.Mutex
		text       strings.Builder
	)
	curMsg.Store("acp-1")
	flush := func() {
		textMu.Lock()
		defer textMu.Unlock()
		if strings.TrimSpace(text.String()) != "" {
			emit(Event{Kind: "text", MsgID: curMsg.Load().(string), Text: text.String()})
		}
		text.Reset()
		curMsg.Store(fmt.Sprintf("acp-%d", msgCounter.Add(1)+1))
	}
	conn := &acpConn{p: p}
	conn.onNote = func(method string, params json.RawMessage) {
		if method != "session/update" || loading.Load() {
			return
		}
		var n struct {
			Update struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Title string `json:"title"`
			} `json:"update"`
		}
		if json.Unmarshal(params, &n) != nil {
			return
		}
		switch n.Update.Kind {
		case "agent_message_chunk":
			if n.Update.Content.Type == "text" {
				textMu.Lock()
				text.WriteString(n.Update.Content.Text)
				textMu.Unlock()
				emit(Event{Kind: "text_delta", MsgID: curMsg.Load().(string), Text: n.Update.Content.Text})
			}
		case "tool_call":
			// opsdeck shows its own tool cards (from the bridge); close the
			// current text segment so text before and after the call stay apart.
			flush()
		}
	}
	conn.onReq = func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "session/request_permission":
			var r struct {
				ToolCall struct {
					Title    string          `json:"title"`
					Kind     string          `json:"kind"`
					RawInput json.RawMessage `json:"rawInput"`
				} `json:"toolCall"`
				Options []struct {
					OptionID string `json:"optionId"`
					Kind     string `json:"kind"`
				} `json:"options"`
			}
			json.Unmarshal(params, &r)
			want := "reject_once"
			if isOpsdeckTool(r.ToolCall.Title, r.ToolCall.RawInput) {
				// opsdeck applies its own approval inside the tool call.
				want = "allow_once"
			} else {
				emit(Event{Kind: "notice", Text: "Blocked the agent's own tool \"" + r.ToolCall.Title + "\": only opsdeck tools are allowed"})
			}
			for _, o := range r.Options {
				if o.Kind == want {
					return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}}, nil
				}
			}
			for _, o := range r.Options {
				if strings.HasPrefix(o.Kind, "reject") {
					return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}}, nil
				}
			}
			return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
		}
		return nil, fmt.Errorf("%s is not supported by opsdeck", method)
	}
	go conn.readLoop()
	done := make(chan error, 1)
	go func() { done <- p.wait(ctx) }()
	fail := func(err error) (Result, error) {
		cancel()
		if tail := p.stderrTail(); tail != "" && !strings.Contains(err.Error(), tail) {
			err = fmt.Errorf("%w\n%s", err, tail)
		}
		return res, err
	}

	var initRes struct {
		AgentCapabilities struct {
			LoadSession     bool `json:"loadSession"`
			MCPCapabilities struct {
				HTTP bool `json:"http"`
			} `json:"mcpCapabilities"`
		} `json:"agentCapabilities"`
		AuthMethods []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"authMethods"`
	}
	if err := conn.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]string{"name": "opsdeck", "version": "0.1.0"},
	}, &initRes); err != nil {
		return fail(err)
	}

	var server map[string]any
	if initRes.AgentCapabilities.MCPCapabilities.HTTP {
		server = map[string]any{"type": "http", "name": "opsdeck", "url": req.MCPURL,
			"headers": []map[string]string{{"name": "Authorization", "value": "Bearer " + req.MCPToken}}}
	} else {
		server = map[string]any{"name": "opsdeck", "command": req.OpsdeckBin, "args": []string{"mcp-stdio", "--url", req.MCPURL},
			"env": []map[string]string{{"name": "OPSDECK_MCP_TOKEN", "value": req.MCPToken}}}
	}
	servers := []any{server}
	if gemini {
		servers = []any{} // configured (and trusted) in the private settings file
	}

	sessionID := ""
	if req.Resume != "" && initRes.AgentCapabilities.LoadSession {
		loading.Store(true) // the agent replays history: don't show it again
		err := conn.call(ctx, "session/load", map[string]any{"sessionId": req.Resume, "cwd": req.WorkDir, "mcpServers": servers}, nil)
		loading.Store(false)
		if err == nil {
			sessionID = req.Resume
		}
	}
	prompt := req.Prompt
	if sessionID == "" {
		var ns struct {
			SessionID string `json:"sessionId"`
		}
		err := conn.call(ctx, "session/new", map[string]any{"cwd": req.WorkDir, "mcpServers": servers}, &ns)
		if err != nil {
			if len(initRes.AuthMethods) > 0 {
				var names []string
				for _, a := range initRes.AuthMethods {
					names = append(names, a.Name)
				}
				return fail(fmt.Errorf("%w — the agent may need you to sign in first: open it once in a terminal (%s)", err, strings.Join(names, ", ")))
			}
			return fail(err)
		}
		sessionID = ns.SessionID
		// ACP has no system prompt: put opsdeck's instructions first.
		prompt = "<opsdeck-context>\n" + req.System + "\n</opsdeck-context>\n\n" + req.Prompt
	}
	res.Resume = sessionID

	go func() {
		<-ctx.Done()
		conn.send(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": sessionID}})
	}()
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	err = conn.call(ctx, "session/prompt", map[string]any{"sessionId": sessionID,
		"prompt": []map[string]string{{"type": "text", "text": prompt}}}, &pr)
	flush()
	if err != nil {
		return fail(err)
	}
	if pr.StopReason == "refusal" {
		return res, errors.New("the agent refused this request")
	}
	if pr.StopReason == "max_tokens" || pr.StopReason == "max_turn_requests" {
		emit(Event{Kind: "notice", Text: "The agent stopped early (" + pr.StopReason + ")"})
	}
	cancel()
	<-done
	return res, nil
}
