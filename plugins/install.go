package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SkillTargets are the agents InstallSkill knows, for help texts.
var SkillTargets = []string{"claude-code", "codex", "gemini", "antigravity", "cursor", "dir"}

// SkillPath is where InstallSkill puts the skill for an agent ("" when the
// agent has no fixed place, like cursor or dir).
func SkillPath(agent string) string {
	home, _ := os.UserHomeDir()
	switch agent {
	case "claude-code", "claude":
		return filepath.Join(home, ".claude", "skills", "opsdeck")
	case "codex":
		return filepath.Join(home, ".codex", "skills", "opsdeck")
	case "gemini":
		return filepath.Join(home, ".gemini", "extensions", "opsdeck")
	case "antigravity":
		return filepath.Join(home, ".gemini", "antigravity-cli", "skills", "opsdeck")
	}
	return ""
}

// InstallSkill writes the opsdeck skill where an agent looks for it and
// returns the folder (or file) it wrote. dir overrides the default place.
func InstallSkill(agent, dir string, force bool) (string, error) {
	skill := Skill()
	switch agent {
	case "claude-code", "claude", "codex", "antigravity":
		return copySkill(skill, firstNonEmpty(dir, SkillPath(agent)), force)
	case "dir":
		if dir == "" {
			return "", errors.New("give the folder with --dir")
		}
		return copySkill(skill, dir, force)
	case "gemini":
		// an extension: its context file is the skill without front matter
		to := firstNonEmpty(dir, SkillPath("gemini"))
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
	return "", fmt.Errorf("unknown agent %q (claude-code, codex, gemini, antigravity, cursor or dir)", agent)
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

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
