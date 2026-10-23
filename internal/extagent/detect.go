package extagent

import (
	"context"
	"os/exec"
	"strings"

	"opsdeck/internal/sysproc"
)

// Installed describes an agent CLI found on this machine.
type Installed struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Path    string   `json:"path"`
	Version string   `json:"version"`
	Login   string   `json:"login"`  // how the user signs in (shown in the UI)
	Models  []string `json:"models"` // built-in list; ListModels asks the CLI
}

type candidate struct {
	Installed
	versionArgs []string
}

// Known agents. ACP entries run an agent that speaks the Agent Client Protocol.
var candidates = []candidate{
	{Installed: Installed{Kind: ClaudeCode, Name: "Claude Code", Command: "claude", Login: "Your Claude subscription or API key — run `claude` once and log in", Models: []string{"default", "opus", "fable", "sonnet", "haiku"}}, versionArgs: []string{"--version"}},
	{Installed: Installed{Kind: Codex, Name: "Codex (ChatGPT)", Command: "codex", Login: "Your ChatGPT account — run `codex login`", Models: []string{"default"}}, versionArgs: []string{"--version"}},
	{Installed: Installed{Kind: GeminiCLI, Name: "Gemini CLI", Command: "gemini", Login: "Your Google account — run `gemini` once and sign in", Models: []string{"default", "auto", "pro", "flash", "flash-lite"}}, versionArgs: []string{"--version"}},
	{Installed: Installed{Kind: ACP, Name: "opencode", Command: "opencode", Args: []string{"acp"}, Login: "The providers configured in opencode (`opencode auth login`)", Models: []string{"default"}}, versionArgs: []string{"--version"}},
	{Installed: Installed{Kind: ACP, Name: "Qwen Code", Command: "qwen", Args: []string{"--acp"}, Login: "Your Qwen account — run `qwen` once", Models: []string{"default"}}, versionArgs: []string{"--version"}},
	{Installed: Installed{Kind: ACP, Name: "GitHub Copilot CLI", Command: "copilot", Args: []string{"--acp"}, Login: "Your GitHub Copilot subscription — run `copilot` once and /login", Models: []string{"default"}}, versionArgs: []string{"--version"}},
}

// Detect lists the agent CLIs installed on PATH.
func Detect(ctx context.Context) []Installed {
	out := []Installed{}
	for _, c := range candidates {
		path, err := exec.LookPath(c.Command)
		if err != nil {
			continue
		}
		in := c.Installed
		in.Path = path
		if b, err := sysproc.CommandContext(ctx, path, c.versionArgs...).Output(); err == nil {
			in.Version = strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
		}
		out = append(out, in)
	}
	return out
}
