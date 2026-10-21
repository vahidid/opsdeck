// Package extagent drives locally installed AI agent CLIs (Claude Code,
// OpenAI Codex, Gemini CLI, any Agent Client Protocol agent) as the "model"
// of an opsdeck session. The external agent runs its own loop with the user's
// own login (subscription or API key), but its built-in tools are disabled:
// the only tools it gets are opsdeck's, through the session's MCP bridge, so
// opsdeck's permissions, approvals, redaction and audit still apply.
package extagent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Kinds of external agents.
const (
	ClaudeCode = "claude-code"
	Codex      = "codex"
	GeminiCLI  = "gemini-cli"
	ACP        = "acp"
)

// IsKind reports whether a provider type is an external agent.
func IsKind(t string) bool {
	switch t {
	case ClaudeCode, Codex, GeminiCLI, ACP:
		return true
	}
	return false
}

// DefaultCommand is the executable looked up on PATH when none is configured.
func DefaultCommand(kind string) string {
	switch kind {
	case ClaudeCode:
		return "claude"
	case Codex:
		return "codex"
	case GeminiCLI:
		return "gemini"
	}
	return ""
}

// Spec configures one external agent.
type Spec struct {
	Kind    string
	Command string   // executable; empty = DefaultCommand(Kind)
	Args    []string // extra arguments (ACP: the full agent command line after Command)
	Env     map[string]string
	Model   string // "" or "default" = the agent's own default
}

// Request is one user turn.
type Request struct {
	Spec       Spec
	Prompt     string
	System     string // opsdeck instructions and workspace context
	FirstTurn  bool   // agents without a system prompt option get System prepended
	Resume     string // external session/thread id from the previous turn
	MCPURL     string // the session's opsdeck MCP bridge (streamable HTTP)
	MCPToken   string
	OpsdeckBin string   // opsdeck executable, for the stdio relay
	ToolNames  []string // opsdeck tool names offered through the bridge
	WorkDir    string   // empty scratch directory for the agent process
}

// Event is streamed to the session timeline.
type Event struct {
	Kind  string // text_delta | text | notice
	MsgID string
	Text  string
}

// Result of a turn.
type Result struct {
	Resume       string
	InputTokens  int
	OutputTokens int
}

// Run executes one turn.
func Run(ctx context.Context, req Request, emit func(Event)) (Result, error) {
	if req.Spec.Command == "" {
		req.Spec.Command = DefaultCommand(req.Spec.Kind)
	}
	if req.Spec.Command == "" {
		return Result{}, errors.New("no command configured for this agent")
	}
	if _, err := exec.LookPath(req.Spec.Command); err != nil {
		return Result{}, fmt.Errorf("%s not found: install it or set its path in Settings → Model providers (%v)", req.Spec.Command, err)
	}
	switch req.Spec.Kind {
	case ClaudeCode:
		return runClaude(ctx, req, emit)
	case Codex:
		return runCodex(ctx, req, emit)
	case GeminiCLI, ACP:
		return runACP(ctx, req, emit)
	}
	return Result{}, fmt.Errorf("unknown agent kind %q", req.Spec.Kind)
}

// childEnv is the agent's environment: ours minus variables that would make
// a nested Claude Code session think it is the parent, plus the spec's.
func childEnv(extra map[string]string) []string {
	drop := map[string]bool{"CLAUDECODE": true, "CLAUDE_CODE_SESSION_ID": true, "CLAUDE_CODE_CHILD_SESSION": true,
		"CLAUDE_CODE_ENTRYPOINT": true, "OPSDECK_MCP_TOKEN": true}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// process runs a command, feeding stdin and calling onLine for each stdout
// line. Stderr is kept (tail) for error messages.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	mu     sync.Mutex
	stderr strings.Builder
}

func start(ctx context.Context, name string, args []string, env []string, dir string) (*process, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env, cmd.Dir = env, dir
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5e9
	p := &process{cmd: cmd}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			p.mu.Lock()
			if p.stderr.Len() < 16*1024 {
				p.stderr.WriteString(sc.Text() + "\n")
			}
			p.mu.Unlock()
		}
	}()
	p.stdin = stdin
	p.stdout = bufio.NewScanner(stdout)
	p.stdout.Buffer(make([]byte, 256*1024), 32*1024*1024)
	return p, nil
}

func (p *process) stderrTail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := strings.TrimSpace(p.stderr.String())
	if len(s) > 2000 {
		s = "…" + s[len(s)-2000:]
	}
	return s
}

// wait returns a descriptive error when the process failed.
func (p *process) wait(ctx context.Context) error {
	err := p.cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if tail := p.stderrTail(); tail != "" {
			return fmt.Errorf("%s: %v\n%s", p.cmd.Path, err, tail)
		}
		return fmt.Errorf("%s: %v", p.cmd.Path, err)
	}
	return nil
}
