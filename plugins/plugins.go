// Package plugins holds the opsdeck skill pack for other agents (Claude Code,
// Codex, Gemini CLI, Cursor…). The folder is also a Claude Code plugin
// (plugins/opsdeck, listed in .claude-plugin/marketplace.json); the binary
// embeds the skill so `opsdeck skill install` can write it anywhere.
package plugins

import (
	"embed"
	"io/fs"
)

//go:embed opsdeck/skills/opsdeck
var files embed.FS

// Skill is the skill folder: SKILL.md and references/.
func Skill() fs.FS {
	sub, _ := fs.Sub(files, "opsdeck/skills/opsdeck")
	return sub
}
