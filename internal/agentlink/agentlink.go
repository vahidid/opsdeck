// Package agentlink connects agents outside opsdeck (Claude Code, Claude
// Desktop, Codex, Cursor, Gemini CLI, VS Code, Windsurf, Antigravity) to
// opsdeck's MCP server by writing the server into the agent's own
// configuration, so connecting is one click instead of copying a key and a
// snippet. It changes only the entry it owns (named after the workspace,
// e.g. "opsdeck-acme"), keeps everything else in the file, and backs the
// file up before the first change.
package agentlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"opsdeck/internal/sysproc"
)

// Target is what an agent needs to reach a workspace.
type Target struct {
	Name     string // the server's name in the agent's config (opsdeck-<workspace>)
	Endpoint string // streamable HTTP URL of /mcp/ext
	Secret   string // the access key
	Command  string // this opsdeck's executable, for stdio (`<command> mcp`)
}

// Status describes one agent on this computer.
type Status struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Detected bool   `json:"detected"`
	Config   string `json:"config"`          // the file opsdeck writes (or the CLI it runs)
	Skill    string `json:"skill,omitempty"` // skill target (plugins.InstallSkill), "" = none
	Note     string `json:"note,omitempty"`  // what to do after connecting
	Hint     string `json:"hint,omitempty"`  // how to install the agent
}

type client struct {
	id, name, skill, note, hint string
	detect                      func() bool
	path                        func() string
	format                      format
}

type format int

const (
	fmtMCPServersHTTP      format = iota // {"mcpServers": {name: {"type":"http","url","headers"}}}
	fmtMCPServersURL                     // Cursor: {"mcpServers": {name: {"url","headers"}}}
	fmtMCPServersHTTPURL                 // Gemini CLI: {"mcpServers": {name: {"httpUrl","headers","timeout"}}}
	fmtMCPServersServerURL               // Windsurf, Antigravity: {"mcpServers": {name: {"serverUrl","headers"}}}
	fmtMCPServersStdio                   // Claude Desktop: {"mcpServers": {name: {"command","args","env"}}}
	fmtServersHTTP                       // VS Code: {"servers": {name: {"type":"http","url","headers"}}}
	fmtCodexTOML                         // ~/.codex/config.toml [mcp_servers.<name>]
	fmtClaudeCLI                         // `claude mcp add --scope user`
)

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// userConfig is ~/.config, ~/Library/Application Support or %APPDATA%.
func userConfig() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(home(), ".config")
	}
	return d
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func onPath(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// lookPath is replaced in tests.
var lookPath = exec.LookPath

func clients() []client {
	h := home()
	return []client{
		{id: "claude-code", name: "Claude Code", skill: "claude-code", format: fmtClaudeCLI,
			detect: func() bool { _, err := lookPath("claude"); return err == nil },
			path:   func() string { return filepath.Join(h, ".claude.json") },
			note:   "Start a new Claude Code session (or run /mcp) to pick up the server.",
			hint:   "npm install -g @anthropic-ai/claude-code"},
		{id: "claude-desktop", name: "Claude Desktop", format: fmtMCPServersStdio,
			detect: func() bool { return exists(filepath.Join(userConfig(), "Claude")) },
			path:   func() string { return filepath.Join(userConfig(), "Claude", "claude_desktop_config.json") },
			note:   "Quit and reopen Claude Desktop to load the server.",
			hint:   "claude.ai/download"},
		{id: "codex", name: "Codex", skill: "codex", format: fmtCodexTOML,
			detect: func() bool { return onPath("codex") || exists(filepath.Join(h, ".codex")) },
			path:   func() string { return filepath.Join(h, ".codex", "config.toml") },
			note:   "New Codex sessions load the server.",
			hint:   "npm install -g @openai/codex"},
		{id: "cursor", name: "Cursor", format: fmtMCPServersURL,
			detect: func() bool { return exists(filepath.Join(h, ".cursor")) },
			path:   func() string { return filepath.Join(h, ".cursor", "mcp.json") },
			note:   "Cursor picks it up in Settings › MCP; turn the server on there if it is off.",
			hint:   "cursor.com"},
		{id: "gemini-cli", name: "Gemini CLI", skill: "gemini", format: fmtMCPServersHTTPURL,
			detect: func() bool { return onPath("gemini") || exists(filepath.Join(h, ".gemini", "settings.json")) },
			path:   func() string { return filepath.Join(h, ".gemini", "settings.json") },
			note:   "Start a new Gemini CLI session; /mcp lists the server.",
			hint:   "npm install -g @google/gemini-cli"},
		{id: "antigravity-cli", name: "Antigravity CLI", skill: "antigravity", format: fmtMCPServersServerURL,
			detect: func() bool { return onPath("agy") || exists(filepath.Join(h, ".gemini", "antigravity-cli")) },
			path:   func() string { return filepath.Join(h, ".gemini", "config", "mcp_config.json") },
			note:   "Start a new agy session; /mcp lists the server.",
			hint:   "curl -fsSL https://antigravity.google/cli/install.sh | bash"},
		{id: "antigravity", name: "Antigravity", format: fmtMCPServersServerURL,
			detect: func() bool { return exists(filepath.Join(h, ".gemini", "antigravity")) },
			path:   func() string { return filepath.Join(h, ".gemini", "antigravity", "mcp_config.json") },
			note:   "In Antigravity open the agent panel › … › MCP Servers › Refresh.",
			hint:   "antigravity.google"},
		{id: "vscode", name: "VS Code", format: fmtServersHTTP,
			detect: func() bool { return exists(filepath.Join(userConfig(), "Code", "User")) },
			path:   func() string { return filepath.Join(userConfig(), "Code", "User", "mcp.json") },
			note:   "VS Code starts the server when Copilot Chat (agent mode) first needs it.",
			hint:   "code.visualstudio.com"},
		{id: "windsurf", name: "Windsurf", format: fmtMCPServersServerURL,
			detect: func() bool { return exists(filepath.Join(h, ".codeium", "windsurf")) },
			path:   func() string { return filepath.Join(h, ".codeium", "windsurf", "mcp_config.json") },
			note:   "In Windsurf open Cascade › MCP servers › Refresh.",
			hint:   "windsurf.com"},
	}
}

func find(id string) (client, error) {
	for _, c := range clients() {
		if c.id == id {
			return c, nil
		}
	}
	return client{}, fmt.Errorf("opsdeck cannot set up %q by itself; use the manual setup", id)
}

// Known reports whether id is an agent opsdeck can set up.
func Known(id string) bool {
	_, err := find(id)
	return err == nil
}

// Clients lists the agents opsdeck can set up and whether each is on this
// computer.
func Clients() []Status {
	var out []Status
	for _, c := range clients() {
		st := Status{ID: c.id, Name: c.name, Detected: c.detect(), Config: c.path(), Skill: c.skill, Note: c.note, Hint: c.hint}
		if c.format == fmtClaudeCLI {
			st.Config = "claude mcp add --scope user"
		}
		out = append(out, st)
	}
	return out
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ServerName is the name of a workspace's server in an agent's config.
func ServerName(workspace string) string {
	n := "opsdeck-" + regexp.MustCompile(`[^A-Za-z0-9_-]+`).ReplaceAllString(workspace, "-")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// Install writes the server into the agent's config (replacing an entry of
// the same name) and returns the file it changed.
func Install(ctx context.Context, id string, t Target) (string, error) {
	c, err := find(id)
	if err != nil {
		return "", err
	}
	if !nameRe.MatchString(t.Name) {
		return "", fmt.Errorf("invalid server name %q", t.Name)
	}
	if c.format == fmtClaudeCLI {
		return c.path(), claudeAdd(ctx, t)
	}
	path := c.path()
	if c.format == fmtCodexTOML {
		return path, editText(path, func(s string) string { return codexSet(s, t) })
	}
	return path, editJSON(path, func(doc map[string]any) {
		key, entry := jsonEntry(c.format, t)
		servers, _ := doc[key].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers[t.Name] = entry
		doc[key] = servers
	})
}

// Uninstall removes the server named name from the agent's config.
func Uninstall(ctx context.Context, id, name string) error {
	c, err := find(id)
	if err != nil {
		return err
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid server name %q", name)
	}
	if !Installed(id, name) {
		return nil
	}
	switch c.format {
	case fmtClaudeCLI:
		return claudeRun(ctx, "mcp", "remove", "--scope", "user", name)
	case fmtCodexTOML:
		return editText(c.path(), func(s string) string { return codexRemove(s, name) })
	}
	return editJSON(c.path(), func(doc map[string]any) {
		key, _ := jsonEntry(c.format, Target{})
		if servers, ok := doc[key].(map[string]any); ok {
			delete(servers, name)
		}
	})
}

// Installed reports whether the agent's config has a server named name.
func Installed(id, name string) bool {
	c, err := find(id)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(c.path())
	if err != nil {
		return false
	}
	if c.format == fmtCodexTOML {
		return codexHeader(name).Match(b)
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return false
	}
	key := "mcpServers"
	if c.format == fmtServersHTTP {
		key = "servers"
	}
	servers, _ := doc[key].(map[string]any)
	_, ok := servers[name]
	return ok
}

// jsonEntry is the config key holding the servers and this server's entry.
func jsonEntry(f format, t Target) (string, map[string]any) {
	auth := map[string]any{"Authorization": "Bearer " + t.Secret}
	switch f {
	case fmtMCPServersURL:
		return "mcpServers", map[string]any{"url": t.Endpoint, "headers": auth}
	case fmtMCPServersHTTPURL:
		// opsdeck's calls may wait for an approval: a long timeout (ms)
		return "mcpServers", map[string]any{"httpUrl": t.Endpoint, "headers": auth, "timeout": 3600000}
	case fmtMCPServersServerURL:
		return "mcpServers", map[string]any{"serverUrl": t.Endpoint, "headers": auth}
	case fmtMCPServersStdio:
		return "mcpServers", map[string]any{"command": t.Command, "args": []string{"mcp"}, "env": map[string]any{"OPSDECK_TOKEN": t.Secret}}
	case fmtServersHTTP:
		return "servers", map[string]any{"type": "http", "url": t.Endpoint, "headers": auth}
	}
	return "mcpServers", map[string]any{"type": "http", "url": t.Endpoint, "headers": auth}
}

// editJSON changes a JSON config file in place: everything but what edit
// changes is kept (key order may change). A file opsdeck cannot parse
// (JSON with comments) is left alone.
func editJSON(path string, edit func(map[string]any)) error {
	doc := map[string]any{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil && len(bytes.TrimSpace(b)) > 0:
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s is not plain JSON (comments?), so opsdeck leaves it alone: add the server by hand with the manual setup", path)
		}
		if doc == nil {
			doc = map[string]any{}
		}
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	edit(doc)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, b, append(out, '\n'))
}

func editText(path string, edit func(string) string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeFile(path, b, []byte(edit(string(b))))
}

// writeFile replaces path atomically with mode 0600 (the file holds a key),
// after keeping the old content once as <path>.opsdeck-backup.
func writeFile(path string, old, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(old) > 0 {
		if bak := path + ".opsdeck-backup"; !exists(bak) {
			os.WriteFile(bak, old, 0o600)
		}
	}
	tmp := fmt.Sprintf("%s.opsdeck-%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// --- Codex: ~/.codex/config.toml

func codexHeader(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\s*\[mcp_servers\.("?)` + regexp.QuoteMeta(name) + `("?)(\.[^\]]*)?\]\s*$`)
}

// codexRemove drops the [mcp_servers.<name>] table and its sub-tables.
func codexRemove(s, name string) string {
	hdr := codexHeader(name)
	anyHdr := regexp.MustCompile(`^\s*\[`)
	var out []string
	skip := false
	for _, line := range strings.SplitAfter(s, "\n") {
		if anyHdr.MatchString(line) {
			skip = hdr.MatchString(strings.TrimRight(line, "\r\n"))
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "")
}

func tomlString(s string) string {
	b, _ := json.Marshal(s) // a JSON string is a valid TOML basic string
	return string(b)
}

func codexSet(s string, t Target) string {
	s = strings.TrimRight(codexRemove(s, t.Name), "\n")
	if s != "" {
		s += "\n\n"
	}
	return s + fmt.Sprintf("[mcp_servers.%s]\ncommand = %s\nargs = [\"mcp\"]\nenv = { OPSDECK_TOKEN = %s }\n# opsdeck's calls may wait for your approval\ntool_timeout_sec = 3600\n",
		t.Name, tomlString(t.Command), tomlString(t.Secret))
}

// --- Claude Code: its own CLI keeps ~/.claude.json consistent

func claudeRun(ctx context.Context, args ...string) error {
	bin, err := lookPath("claude")
	if err != nil {
		return errors.New("the claude command is not on PATH: install Claude Code, or use the manual setup")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := sysproc.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude %s: %v: %s", args[1], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func claudeAdd(ctx context.Context, t Target) error {
	claudeRun(ctx, "mcp", "remove", "--scope", "user", t.Name) // an older entry of this name
	return claudeRun(ctx, "mcp", "add", "--scope", "user", "--transport", "http", t.Name, t.Endpoint, "--header", "Authorization: Bearer "+t.Secret)
}
