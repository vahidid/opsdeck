package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"opsdeck/internal/sysproc"
)

// runAntigravity drives Google's Antigravity CLI (`agy`) in print mode with
// the user's own Google sign-in. Its only MCP server is opsdeck's bridge,
// written to the scratch folder's workspace config (.agents/mcp_config.json)
// with the opsdeck tools pre-allowed; the folder is empty and the terminal
// sandbox is on, so its own file and shell tools have nothing to work on.
// The JSON envelope (`--output-format json`) carries the conversation id,
// the answer and the usage.
func runAntigravity(ctx context.Context, req Request, emit func(Event)) (Result, error) {
	cleanup, err := writeAntigravityWorkspace(req)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	args := []string{"-p", "--output-format", "json", "--disable-slash-commands", "--sandbox",
		// opsdeck's calls may wait minutes for a human approval
		"--print-timeout", "60m"}
	if m := req.Spec.Model; m != "" && m != "default" {
		args = append(args, "--model", m)
	}
	if req.Resume != "" {
		args = append(args, "--conversation", req.Resume)
	}
	args = append(args, req.Spec.Args...)

	// agy has no system prompt option: the instructions go with the first
	// message of a conversation.
	prompt := withoutFiles(req.Prompt, req.Files, "Antigravity", emit)
	if req.FirstTurn && req.System != "" {
		prompt = "<instructions>\n" + req.System + "\n</instructions>\n\n" + prompt
	}
	p, err := start(ctx, req.Spec.Command, args, childEnv(req.Spec.Env), req.WorkDir)
	if err != nil {
		return Result{}, err
	}
	p.stdin.Write([]byte(prompt))
	p.stdin.Close()

	var out strings.Builder
	for p.stdout.Scan() {
		out.Write(p.stdout.Bytes())
		out.WriteByte('\n')
	}
	werr := p.wait(ctx)
	res, text, failure := parseAntigravity(out.String())
	if text != "" {
		emit(Event{Kind: "text", MsgID: fmt.Sprintf("agy-%d", time.Now().UnixNano()), Text: text})
	}
	if failure != "" {
		low := strings.ToLower(failure)
		if strings.Contains(low, "sign in") || strings.Contains(low, "login") || strings.Contains(low, "auth") {
			failure += " — sign in by running `agy` once"
		}
		return res, fmt.Errorf("Antigravity: %s", failure)
	}
	if werr != nil {
		return res, werr
	}
	if text == "" {
		return res, errors.New("Antigravity returned no answer")
	}
	return res, nil
}

// parseAntigravity reads agy's JSON envelope ({conversation_id, status,
// response, usage}); NDJSON (one event per line) and plain text are read as
// well, since the format has changed between versions.
func parseAntigravity(out string) (res Result, text, failure string) {
	type envelope struct {
		Type           string          `json:"type"`
		ConversationID string          `json:"conversation_id"`
		SessionID      string          `json:"session_id"`
		Status         string          `json:"status"`
		Response       string          `json:"response"`
		Result         string          `json:"result"`
		Content        json.RawMessage `json:"content"`
		Role           string          `json:"role"`
		Delta          bool            `json:"delta"`
		Error          json.RawMessage `json:"error"`
		Usage          struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			Prompt       int `json:"prompt_tokens"`
			Completion   int `json:"completion_tokens"`
		} `json:"usage"`
	}
	read := func(e envelope) {
		if id := firstNonEmptyStr(e.ConversationID, e.SessionID); id != "" {
			res.Resume = id
		}
		res.InputTokens += e.Usage.InputTokens + e.Usage.Prompt
		res.OutputTokens += e.Usage.OutputTokens + e.Usage.Completion
		if len(e.Error) > 0 && string(e.Error) != "null" {
			var s string
			if json.Unmarshal(e.Error, &s) != nil {
				var o struct{ Message string }
				json.Unmarshal(e.Error, &o)
				s = o.Message
			}
			if s != "" {
				failure = s
			}
		}
		if e.Status != "" && e.Status != "success" && e.Status != "ok" && e.Status != "completed" && failure == "" && e.Response == "" {
			failure = e.Status
		}
		switch {
		case e.Response != "":
			text = e.Response
		case e.Type == "result" && e.Result != "":
			text = e.Result
		case e.Role == "assistant" && len(e.Content) > 0:
			var s string
			if json.Unmarshal(e.Content, &s) == nil {
				if e.Delta {
					text += s
				} else {
					text = s
				}
			}
		}
	}
	trimmed := strings.TrimSpace(out)
	var whole envelope
	if json.Unmarshal([]byte(trimmed), &whole) == nil {
		read(whole)
		return
	}
	parsed := false
	for _, line := range strings.Split(trimmed, "\n") {
		var e envelope
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) == nil {
			parsed = true
			read(e)
		}
	}
	if !parsed {
		text = trimmed
	}
	return
}

// writeAntigravityWorkspace puts the opsdeck bridge into the scratch
// folder's workspace settings; the returned func removes the files again
// (they hold the per-turn token).
func writeAntigravityWorkspace(req Request) (func(), error) {
	dir := filepath.Join(req.WorkDir, ".agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	mcpCfg, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"opsdeck": map[string]any{
		"serverUrl":      req.MCPURL,
		"headers":        map[string]string{"Authorization": "Bearer " + req.MCPToken},
		"timeoutSeconds": 3600,
	}}}, "", "  ")
	settings, _ := json.MarshalIndent(map[string]any{
		// opsdeck runs its own approval inside each tool call
		"permissions":             map[string]any{"allow": []string{"mcp(opsdeck/*)"}},
		"enableTerminalSandbox":   true,
		"allowNonWorkspaceAccess": false,
		"enableTelemetry":         false,
	}, "", "  ")
	files := map[string][]byte{"mcp_config.json": mcpCfg, "settings.json": settings}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return nil, err
		}
	}
	return func() {
		for name := range files {
			os.Remove(filepath.Join(dir, name))
		}
	}, nil
}

var agyModelRe = regexp.MustCompile(`^[*>\-\s]*([a-z0-9][a-z0-9._:/-]*[a-z0-9])\b(.*)$`)

// listAntigravityModels runs `agy models`, one model slug per line.
func listAntigravityModels(ctx context.Context, spec Spec, dir string) ([]Model, error) {
	cmd := sysproc.CommandContext(ctx, spec.Command, "models")
	cmd.Env, cmd.Dir = childEnv(spec.Env), dir
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("agy models: %v", err)
	}
	return parseAgyModels(string(b)), nil
}

func parseAgyModels(out string) []Model {
	models := []Model{{ID: "default", Name: "Default", Description: "Antigravity's own choice"}}
	seen := map[string]bool{"default": true}
	for _, line := range strings.Split(out, "\n") {
		m := agyModelRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || seen[m[1]] || !strings.ContainsAny(m[1], "-.0123456789") {
			continue
		}
		seen[m[1]] = true
		desc := strings.Trim(strings.TrimSpace(m[2]), "-–—:() ")
		models = append(models, Model{ID: m[1], Name: m[1], Description: desc})
	}
	return models
}

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
