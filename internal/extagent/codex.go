package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// codexDisabled are Codex features that would let it act outside opsdeck:
// its own shell, local tools, browser, computer use, apps and plugins.
var codexDisabled = []string{
	"shell_tool", "unified_exec", "unified_exec_tty", "shell_snapshot", "view_image",
	"apps", "plugins", "remote_plugin", "browser_use", "browser_use_external", "browser_use_full_cdp_access",
	"in_app_browser", "computer_use", "skill_search", "tool_suggest", "multi_agent", "goals",
}

func tomlString(s string) string { return strconv.Quote(s) }

// runCodex drives the OpenAI Codex CLI (`codex exec --json`), which uses the
// user's own ChatGPT login (`codex login`) or API key. User config, rules and
// MCP servers are ignored; built-in tools are disabled and, as a second
// layer, the sandbox is read-only with approvals off (this also blocks
// apply_patch, which cannot be switched off). opsdeck's bridge is the
// only MCP server.
func runCodex(ctx context.Context, req Request, emit func(Event)) (Result, error) {
	// codex exec [options] [resume <thread>] -
	args := []string{"exec"}
	args = append(args, "--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
		"-c", "approval_policy=\"never\"", "-c", "web_search=\"disabled\"",
		"-c", "mcp_servers.opsdeck.url="+tomlString(req.MCPURL),
		"-c", "mcp_servers.opsdeck.bearer_token_env_var=\"OPSDECK_CODEX_TOKEN\"",
		"-c", "mcp_servers.opsdeck.tool_timeout_sec=3600",
		"-c", "mcp_servers.opsdeck.startup_timeout_sec=60",
		// opsdeck runs its own approval inside each tool call
		"-c", "mcp_servers.opsdeck.default_tools_approval_mode=\"approve\"",
		"-c", "developer_instructions="+tomlString(req.System))
	args = append(args, "--sandbox", "read-only", "--cd", req.WorkDir)
	for _, f := range codexDisabled {
		args = append(args, "--disable", f)
	}
	if m := req.Spec.Model; m != "" && m != "default" {
		args = append(args, "--model", m)
	}
	args = append(args, req.Spec.Args...)
	if req.Resume != "" {
		args = append(args, "resume", req.Resume)
	}
	args = append(args, "-") // prompt from stdin

	env := childEnv(req.Spec.Env)
	env = append(env, "OPSDECK_CODEX_TOKEN="+req.MCPToken)
	p, err := start(ctx, req.Spec.Command, args, env, req.WorkDir)
	if err != nil {
		return Result{}, err
	}
	p.stdin.Write([]byte(req.Prompt))
	p.stdin.Close()

	var res Result
	var failure string
	n := 0
	for p.stdout.Scan() {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Message  string `json:"message"`
			Item     struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Text    string `json:"text"`
				Message string `json:"message"`
				Server  string `json:"server"`
				Tool    string `json:"tool"`
				Status  string `json:"status"`
				Command string `json:"command"`
			} `json:"item"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				CachedInput  int `json:"cached_input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(p.stdout.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			res.Resume = ev.ThreadID
		case "item.completed":
			switch ev.Item.Type {
			case "agent_message":
				n++
				if strings.TrimSpace(ev.Item.Text) != "" {
					emit(Event{Kind: "text", MsgID: fmt.Sprintf("codex-%s-%d", res.Resume, n), Text: ev.Item.Text})
				}
			case "command_execution":
				// Should never happen with the shell disabled; surface it.
				emit(Event{Kind: "notice", Text: "Codex tried to run a local command (blocked by the read-only sandbox): " + ev.Item.Command})
			}
		case "turn.completed":
			res.InputTokens += ev.Usage.InputTokens
			res.OutputTokens += ev.Usage.OutputTokens
		case "turn.failed":
			failure = ev.Error.Message
		case "error":
			// transient "Reconnecting…" messages are noise; keep the last one
			if !strings.HasPrefix(ev.Message, "Reconnecting") {
				failure = ev.Message
			}
		}
	}
	err = p.wait(ctx)
	if failure != "" {
		if strings.Contains(strings.ToLower(failure), "401") || strings.Contains(strings.ToLower(failure), "login") {
			failure += " — sign in with `codex login`"
		}
		return res, fmt.Errorf("Codex: %s", failure)
	}
	return res, err
}
