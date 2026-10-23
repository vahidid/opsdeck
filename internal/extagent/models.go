package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Model is one model an agent CLI offers.
type Model struct {
	ID          string `json:"id"` // what opsdeck passes back to the agent ("default" = its own default)
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ModelList is what an agent reported, or the built-in list when it could
// not be asked (Error says why).
type ModelList struct {
	Models []Model `json:"models"`
	Source string  `json:"source"` // cli | builtin
	Error  string  `json:"error,omitempty"`
}

// ListModels asks the agent CLI which models it offers with the user's own
// login: Claude Code through its stream-json control protocol, Codex through
// `codex app-server` (model/list), Gemini CLI and other ACP agents from the
// model list of a new ACP session. No prompt is sent, so it costs no tokens.
func ListModels(ctx context.Context, spec Spec) ModelList {
	if spec.Command == "" {
		spec.Command = DefaultCommand(spec.Kind)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	models, err := listModels(ctx, spec)
	if err == nil && len(models) > 0 {
		return ModelList{Models: models, Source: "cli"}
	}
	if err == nil {
		err = errors.New("the agent did not report any models")
	}
	return ModelList{Models: builtinModels(spec), Source: "builtin", Error: err.Error()}
}

func listModels(ctx context.Context, spec Spec) ([]Model, error) {
	if spec.Command == "" {
		return nil, errors.New("no command configured for this agent")
	}
	if _, err := exec.LookPath(spec.Command); err != nil {
		return nil, fmt.Errorf("%s not found: install it or set its path", spec.Command)
	}
	dir, err := os.MkdirTemp("", "opsdeck-models-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	switch spec.Kind {
	case ClaudeCode:
		return listClaudeModels(ctx, spec, dir)
	case Codex:
		return listCodexModels(ctx, spec, dir)
	case GeminiCLI, ACP:
		return listACPModels(ctx, spec, dir)
	}
	return nil, fmt.Errorf("unknown agent kind %q", spec.Kind)
}

// builtinModels is the fallback list for an agent kind (or known ACP command).
func builtinModels(spec Spec) []Model {
	for _, c := range candidates {
		if c.Kind == spec.Kind && (spec.Kind != ACP || c.Command == spec.Command) {
			out := make([]Model, len(c.Models))
			for i, m := range c.Models {
				out[i] = Model{ID: m, Name: m}
			}
			if len(out) > 0 && out[0].ID == "default" {
				out[0].Name = "Default"
				out[0].Description = "The agent's own default model"
			}
			return out
		}
	}
	return []Model{{ID: "default", Name: "Default", Description: "The agent's own default model"}}
}

// listClaudeModels starts Claude Code in stream-json input mode and sends the
// SDK's initialize control request, whose response lists the models the
// user's account can use (aliases such as "default", "opus" and full names).
func listClaudeModels(ctx context.Context, spec Spec, dir string) ([]Model, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--settings", `{"disableAllHooks":true}`,
		"--no-session-persistence"}
	args = append(args, spec.Args...)
	p, err := start(ctx, spec.Command, args, childEnv(spec.Env), dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		p.stdin.Close()
		cancel()
		p.cmd.Wait()
	}()
	if _, err := p.stdin.Write([]byte(`{"type":"control_request","request_id":"opsdeck-models","request":{"subtype":"initialize"}}` + "\n")); err != nil {
		return nil, err
	}
	for p.stdout.Scan() {
		var ev struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Error     string `json:"error"`
				Response  struct {
					Models []struct {
						Value       string `json:"value"`
						DisplayName string `json:"displayName"`
						Description string `json:"description"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(p.stdout.Bytes(), &ev) != nil || ev.Type != "control_response" || ev.Response.RequestID != "opsdeck-models" {
			continue
		}
		if ev.Response.Subtype != "success" {
			return nil, fmt.Errorf("Claude Code: %s", ev.Response.Error)
		}
		var out []Model
		for _, m := range ev.Response.Response.Models {
			if m.Value != "" {
				out = append(out, Model{ID: m.Value, Name: firstNonEmpty(m.DisplayName, m.Value), Description: m.Description})
			}
		}
		return out, nil
	}
	return nil, exitError(ctx, p, "Claude Code")
}

// listCodexModels asks `codex app-server` (JSON-RPC over stdio) for model/list.
func listCodexModels(ctx context.Context, spec Spec, dir string) ([]Model, error) {
	conn, stop, err := startConn(ctx, spec.Command, []string{"app-server"}, spec.Env, dir)
	if err != nil {
		return nil, err
	}
	defer stop()
	if err := conn.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "opsdeck", "version": "0.1.0"}}, nil); err != nil {
		return nil, conn.explain(ctx, err)
	}
	conn.send(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
	out := []Model{{ID: "default", Name: "Default", Description: "Codex's own default model"}}
	cursor := ""
	for page := 0; page < 10; page++ {
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Data []struct {
				ID          string `json:"id"`
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
				Hidden      bool   `json:"hidden"`
				IsDefault   bool   `json:"isDefault"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if err := conn.call(ctx, "model/list", params, &res); err != nil {
			return nil, conn.explain(ctx, err)
		}
		for _, m := range res.Data {
			id := firstNonEmpty(m.Model, m.ID)
			if id == "" || m.Hidden {
				continue
			}
			name := firstNonEmpty(m.DisplayName, id)
			if m.IsDefault {
				out[0].Description = "Codex's own default model (now " + name + ")"
			}
			out = append(out, Model{ID: id, Name: name, Description: m.Description})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// listACPModels opens an ACP session without MCP servers and reads the
// session's model choices (the "model" config option, or the older models
// field), then closes the session again when the agent supports it.
func listACPModels(ctx context.Context, spec Spec, dir string) ([]Model, error) {
	args := append([]string(nil), spec.Args...)
	if spec.Kind == GeminiCLI {
		args = append([]string{"--acp", "--skip-trust"}, args...)
	}
	conn, stop, err := startConn(ctx, spec.Command, args, spec.Env, dir)
	if err != nil {
		return nil, err
	}
	defer stop()
	init, err := conn.initialize(ctx)
	if err != nil {
		return nil, conn.explain(ctx, err)
	}
	var sess acpSession
	if err := conn.call(ctx, "session/new", map[string]any{"cwd": dir, "mcpServers": []any{}}, &sess); err != nil {
		return nil, conn.explain(ctx, init.authHint(err))
	}
	if init.AgentCapabilities.SessionCapabilities.Close != nil {
		cctx, ccancel := context.WithTimeout(ctx, 3*time.Second)
		conn.call(cctx, "session/close", map[string]string{"sessionId": sess.SessionID}, nil)
		ccancel()
	}
	models, current := sess.models()
	if len(models) == 0 {
		return nil, errors.New("this agent does not let clients choose a model; set it in the agent's own configuration")
	}
	def := Model{ID: "default", Name: "Default", Description: "The agent's own default model"}
	for _, m := range models {
		if m.ID == current {
			def.Description += " (now " + m.Name + ")"
		}
	}
	return append([]Model{def}, models...), nil
}

// startConn starts a JSON-RPC agent process; stop ends it.
func startConn(ctx context.Context, name string, args []string, env map[string]string, dir string) (*acpConn, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	p, err := start(ctx, name, args, childEnv(env), dir)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	conn := &acpConn{p: p,
		onNote: func(string, json.RawMessage) {},
		onReq: func(method string, _ json.RawMessage) (any, error) {
			if method == "session/request_permission" {
				return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
			}
			return nil, fmt.Errorf("%s is not supported by opsdeck", method)
		}}
	go conn.readLoop()
	return conn, func() {
		p.stdin.Close()
		cancel()
		p.cmd.Wait()
	}, nil
}

// explain adds the agent's stderr to an error.
func (c *acpConn) explain(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("the agent did not answer in time (%v)", err)
	}
	if tail := c.p.stderrTail(); tail != "" && !strings.Contains(err.Error(), tail) {
		return fmt.Errorf("%w\n%s", err, tail)
	}
	return err
}

func exitError(ctx context.Context, p *process, name string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s did not answer in time", name)
	}
	if tail := p.stderrTail(); tail != "" {
		return fmt.Errorf("%s exited: %s", name, tail)
	}
	return fmt.Errorf("%s exited without listing its models", name)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
