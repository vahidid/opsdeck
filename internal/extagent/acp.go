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

	initRes, err := conn.initialize(ctx)
	if err != nil {
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
	var sess acpSession
	if req.Resume != "" && initRes.AgentCapabilities.LoadSession {
		loading.Store(true) // the agent replays history: don't show it again
		err := conn.call(ctx, "session/load", map[string]any{"sessionId": req.Resume, "cwd": req.WorkDir, "mcpServers": servers}, &sess)
		loading.Store(false)
		if err == nil {
			sessionID = req.Resume
		}
	}
	prompt := req.Prompt
	if sessionID == "" {
		sess = acpSession{}
		if err := conn.call(ctx, "session/new", map[string]any{"cwd": req.WorkDir, "mcpServers": servers}, &sess); err != nil {
			return fail(initRes.authHint(err))
		}
		sessionID = sess.SessionID
		// ACP has no system prompt: put opsdeck's instructions first.
		prompt = "<opsdeck-context>\n" + req.System + "\n</opsdeck-context>\n\n" + req.Prompt
	}
	// Gemini CLI gets the model on its command line; other agents switch the
	// session's model through ACP.
	if m := req.Spec.Model; !gemini && m != "" && m != "default" {
		if err := conn.setModel(ctx, sess, sessionID, m); err != nil {
			if ctx.Err() != nil {
				return fail(err)
			}
			emit(Event{Kind: "notice", Text: "Could not switch the agent to model \"" + m + "\" (" + err.Error() + "); it uses its default model"})
		}
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

type acpInit struct {
	AgentCapabilities struct {
		LoadSession     bool `json:"loadSession"`
		MCPCapabilities struct {
			HTTP bool `json:"http"`
		} `json:"mcpCapabilities"`
		SessionCapabilities struct {
			Close json.RawMessage `json:"close"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
	AuthMethods []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"authMethods"`
}

func (c *acpConn) initialize(ctx context.Context) (acpInit, error) {
	var r acpInit
	err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]string{"name": "opsdeck", "version": "0.1.0"},
	}, &r)
	return r, err
}

// authHint explains a failed session/new of an agent that has sign-in methods.
func (r acpInit) authHint(err error) error {
	if len(r.AuthMethods) == 0 {
		return err
	}
	var names []string
	for _, a := range r.AuthMethods {
		names = append(names, a.Name)
	}
	return fmt.Errorf("%w — the agent may need you to sign in first: open it once in a terminal (%s)", err, strings.Join(names, ", "))
}

// acpSession is the result of session/new and session/load. Agents offer
// models as a "model" select config option (session/set_config_option) or,
// in older versions of the protocol, as a models field (session/set_model).
type acpSession struct {
	SessionID     string            `json:"sessionId"`
	ConfigOptions []acpConfigOption `json:"configOptions"`
	Models        *struct {
		CurrentModelID  string `json:"currentModelId"`
		AvailableModels []struct {
			ModelID     string `json:"modelId"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"availableModels"`
	} `json:"models"`
}

type acpConfigOption struct {
	ID           string          `json:"id"`
	Category     string          `json:"category"`
	Type         string          `json:"type"`
	CurrentValue json.RawMessage `json:"currentValue"`
	// a value, or a group {group, name, options}
	Options []struct {
		Value       string `json:"value"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Options     []struct {
			Value       string `json:"value"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"options"`
}

func (s acpSession) modelOption() *acpConfigOption {
	for i, o := range s.ConfigOptions {
		if o.Type == "select" && (o.Category == "model" || (o.Category == "" && o.ID == "model")) {
			return &s.ConfigOptions[i]
		}
	}
	return nil
}

// models lists the session's model choices and the current one.
func (s acpSession) models() ([]Model, string) {
	var out []Model
	if o := s.modelOption(); o != nil {
		var cur string
		json.Unmarshal(o.CurrentValue, &cur)
		for _, v := range o.Options {
			if v.Value != "" {
				out = append(out, Model{ID: v.Value, Name: firstNonEmpty(v.Name, v.Value), Description: v.Description})
			}
			for _, g := range v.Options {
				desc := g.Description
				if v.Name != "" && desc == "" {
					desc = v.Name
				}
				out = append(out, Model{ID: g.Value, Name: firstNonEmpty(g.Name, g.Value), Description: desc})
			}
		}
		if len(out) > 0 {
			return out, cur
		}
	}
	if s.Models != nil {
		for _, m := range s.Models.AvailableModels {
			out = append(out, Model{ID: m.ModelID, Name: firstNonEmpty(m.Name, m.ModelID), Description: m.Description})
		}
		return out, s.Models.CurrentModelID
	}
	return nil, ""
}

// setModel switches a session to a model.
func (c *acpConn) setModel(ctx context.Context, s acpSession, sessionID, model string) error {
	if o := s.modelOption(); o != nil {
		var cur string
		if json.Unmarshal(o.CurrentValue, &cur) == nil && cur == model {
			return nil
		}
		return c.call(ctx, "session/set_config_option", map[string]any{"sessionId": sessionID, "configId": o.ID, "value": model}, nil)
	}
	if s.Models != nil {
		if s.Models.CurrentModelID == model {
			return nil
		}
		return c.call(ctx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": model}, nil)
	}
	return errors.New("this agent does not let clients choose a model")
}
