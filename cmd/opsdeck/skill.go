package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
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
  antigravity   ~/.gemini/antigravity-cli/skills/opsdeck/
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
	where, err := plugins.InstallSkill(agent, *dir, *force)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opsdeck skill:", err)
		return 1
	}
	fmt.Printf("Installed the opsdeck skill for %s in %s\n", agent, where)
	fmt.Println("Connect the agent to opsdeck too: opsdeck › App settings › Agent access › Connect an agent.")
	return 0
}
