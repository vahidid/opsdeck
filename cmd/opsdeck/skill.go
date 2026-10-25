package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"opsdeck/plugins"
)

// opsdeck skill: install the opsdeck skill (how to work through opsdeck's
// MCP tools) for another agent. See docs/agents.md.

func skillUsage() {
	fmt.Fprint(os.Stderr, `Usage:
  opsdeck skill install <agent> [--dir DIR] [--force]
  opsdeck skill print            print SKILL.md

Agents and where the skill goes:
  claude-code   ~/.claude/skills/opsdeck/            (or install the plugin: /plugin marketplace add vahidid/opsdeck)
  codex         ~/.codex/skills/opsdeck/
  gemini        ~/.gemini/extensions/opsdeck/         (an extension with GEMINI.md)
  cursor        ./.cursor/rules/opsdeck.mdc           (run it in your project)
  dir           the folder given with --dir           (SKILL.md and references/)

The skill tells the agent how to use opsdeck's tools; connect the agent to opsdeck's
MCP server too (App settings › Agent access in opsdeck).
`)
}

func skillCommand(args []string) int {
	if len(args) == 0 {
		skillUsage()
		return 2
	}
	switch args[0] {
	case "print":
		b, _ := fs.ReadFile(plugins.Skill(), "SKILL.md")
		os.Stdout.Write(b)
		return 0
	case "install":
	default:
		skillUsage()
		return 2
	}
	fsFlags := flag.NewFlagSet("skill install", flag.ContinueOnError)
	dir := fsFlags.String("dir", "", "install into this folder (agent dir, or to override the default)")
	force := fsFlags.Bool("force", false, "replace an existing installation")
	fsFlags.Usage = skillUsage
	rest := args[1:]
	agent := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		agent, rest = rest[0], rest[1:]
	}
	if err := fsFlags.Parse(rest); err != nil {
		return 2
	}
	if agent == "" {
		skillUsage()
		return 2
	}
	where, err := installSkill(agent, *dir, *force)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opsdeck skill:", err)
		return 1
	}
	fmt.Printf("Installed the opsdeck skill for %s in %s\n", agent, where)
	fmt.Println("Connect the agent to opsdeck too: opsdeck › App settings › Agent access › Connect an agent.")
	return 0
}

func installSkill(agent, dir string, force bool) (string, error) {
	home, _ := os.UserHomeDir()
	skill := plugins.Skill()
	switch agent {
	case "claude-code", "claude":
		return copySkill(skill, firstNonEmpty(dir, filepath.Join(home, ".claude", "skills", "opsdeck")), force)
	case "codex":
		return copySkill(skill, firstNonEmpty(dir, filepath.Join(home, ".codex", "skills", "opsdeck")), force)
	case "dir":
		if dir == "" {
			return "", errors.New("give the folder with --dir")
		}
		return copySkill(skill, dir, force)
	case "gemini":
		// an extension: its context file is the skill without front matter
		to := firstNonEmpty(dir, filepath.Join(home, ".gemini", "extensions", "opsdeck"))
		if _, err := os.Stat(filepath.Join(to, "GEMINI.md")); err == nil && !force {
			return "", fmt.Errorf("%s already has the extension (use --force to replace it)", to)
		}
		if _, err := copySkill(skill, to, true); err != nil {
			return "", err
		}
		body, _ := fs.ReadFile(skill, "SKILL.md")
		os.Remove(filepath.Join(to, "SKILL.md"))
		os.WriteFile(filepath.Join(to, "GEMINI.md"), []byte(stripFrontMatter(string(body))), 0o644)
		manifest := `{
  "name": "opsdeck",
  "version": "1.0.0",
  "description": "Work on infrastructure through opsdeck's MCP tools",
  "contextFileName": "GEMINI.md"
}
`
		return to, os.WriteFile(filepath.Join(to, "gemini-extension.json"), []byte(manifest), 0o644)
	case "cursor":
		to := filepath.Join(firstNonEmpty(dir, "."), ".cursor", "rules", "opsdeck.mdc")
		if _, err := os.Stat(to); err == nil && !force {
			return "", fmt.Errorf("%s exists (use --force to replace it)", to)
		}
		body, _ := fs.ReadFile(skill, "SKILL.md")
		rule := "---\ndescription: " + frontMatterField(string(body), "description") + "\nalwaysApply: false\n---\n" + stripFrontMatter(string(body))
		os.MkdirAll(filepath.Dir(to), 0o755)
		return to, os.WriteFile(to, []byte(rule), 0o644)
	}
	return "", fmt.Errorf("unknown agent %q (claude-code, codex, gemini, cursor or dir)", agent)
}

// copySkill writes the skill folder to dir.
func copySkill(skill fs.FS, dir string, force bool) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !force {
		return "", fmt.Errorf("%s already has the skill (use --force to replace it)", dir)
	}
	err := fs.WalkDir(skill, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		b, err := fs.ReadFile(skill, p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o644)
	})
	return dir, err
}

func stripFrontMatter(s string) string {
	if strings.HasPrefix(s, "---\n") {
		if i := strings.Index(s[4:], "\n---\n"); i >= 0 {
			return strings.TrimLeft(s[4+i+5:], "\n")
		}
	}
	return s
}

func frontMatterField(s, key string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		}
		if line == "---" && !strings.HasPrefix(s, line) {
			break
		}
	}
	return ""
}
