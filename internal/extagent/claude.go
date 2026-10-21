package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// runClaude drives Claude Code in print mode with streaming JSON output.
// Built-in tools are disabled (--tools ""); the only MCP server is opsdeck's
// bridge (--strict-mcp-config) and its tools are pre-allowed, so nothing
// prompts. User/project settings, hooks and CLAUDE.md files are not loaded.
func runClaude(ctx context.Context, req Request, emit func(Event)) (Result, error) {
	cfg := map[string]any{"mcpServers": map[string]any{"opsdeck": map[string]any{
		"type": "http", "url": req.MCPURL, "headers": map[string]string{"Authorization": "Bearer " + req.MCPToken},
	}}}
	// The config holds the per-turn token: write it to a private file
	// instead of the command line.
	cfgPath := filepath.Join(req.WorkDir, "mcp.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		return Result{}, err
	}
	defer os.Remove(cfgPath)

	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--tools", "", "--strict-mcp-config", "--mcp-config", cfgPath,
		"--allowedTools", "mcp__opsdeck__*", "--permission-mode", "dontAsk",
		"--setting-sources", "", "--settings", `{"disableAllHooks":true}`, "--disable-slash-commands",
		"--append-system-prompt", req.System}
	if m := req.Spec.Model; m != "" && m != "default" {
		args = append(args, "--model", m)
	}
	if req.Resume != "" {
		args = append(args, "--resume", req.Resume)
	} else {
		// An explicit id: never inherit a session from the environment.
		id := uuid.NewString()
		args = append(args, "--session-id", id)
	}
	args = append(args, req.Spec.Args...)
	env := childEnv(req.Spec.Env)
	// opsdeck tools may wait minutes for a human approval: lift the per-call,
	// idle and auto-background limits. No CLAUDE.md files or auto memory: the
	// workspace context comes from opsdeck.
	env = append(env, "MCP_TOOL_TIMEOUT=3600000", "MCP_TIMEOUT=60000",
		"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0", "CLAUDE_CODE_MCP_AUTO_BACKGROUND_MS=0",
		"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1", "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")

	p, err := start(ctx, req.Spec.Command, args, env, req.WorkDir)
	if err != nil {
		return Result{}, err
	}
	p.stdin.Write([]byte(req.Prompt))
	p.stdin.Close()

	var res Result
	var resultErr string
	curMsg := ""
	for p.stdout.Scan() {
		var ev struct {
			Type      string          `json:"type"`
			Subtype   string          `json:"subtype"`
			SessionID string          `json:"session_id"`
			IsError   bool            `json:"is_error"`
			Result    string          `json:"result"`
			Message   json.RawMessage `json:"message"`
			Event     json.RawMessage `json:"event"`
			Usage     struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				CacheRead    int `json:"cache_read_input_tokens"`
				CacheCreate  int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
			MCPServers []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"mcp_servers"`
		}
		if json.Unmarshal(p.stdout.Bytes(), &ev) != nil {
			continue
		}
		if ev.SessionID != "" {
			res.Resume = ev.SessionID
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" {
				for _, s := range ev.MCPServers {
					if s.Name == "opsdeck" && s.Status != "connected" {
						emit(Event{Kind: "notice", Text: "Claude Code could not connect to the opsdeck tools (status: " + s.Status + "); it can only answer from context in this turn"})
					}
				}
			}
		case "stream_event":
			var se struct {
				Type    string `json:"type"`
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			json.Unmarshal(ev.Event, &se)
			switch {
			case se.Type == "message_start":
				curMsg = se.Message.ID
			case se.Type == "content_block_delta" && se.Delta.Type == "text_delta":
				emit(Event{Kind: "text_delta", MsgID: curMsg, Text: se.Delta.Text})
			}
		case "assistant":
			var m struct {
				ID      string `json:"id"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			json.Unmarshal(ev.Message, &m)
			var parts []string
			for _, c := range m.Content {
				if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
					parts = append(parts, c.Text)
				}
			}
			if len(parts) > 0 {
				emit(Event{Kind: "text", MsgID: m.ID, Text: strings.Join(parts, "\n\n")})
			}
		case "result":
			res.InputTokens = ev.Usage.InputTokens + ev.Usage.CacheRead + ev.Usage.CacheCreate
			res.OutputTokens = ev.Usage.OutputTokens
			if ev.IsError || strings.HasPrefix(ev.Subtype, "error") {
				resultErr = strings.TrimSpace(ev.Result)
				if resultErr == "" {
					resultErr = ev.Subtype
				}
			}
		}
	}
	if err := p.wait(ctx); err != nil {
		if resultErr != "" {
			return res, fmt.Errorf("Claude Code: %s", resultErr)
		}
		return res, err
	}
	if resultErr != "" {
		return res, fmt.Errorf("Claude Code: %s", resultErr)
	}
	return res, nil
}
