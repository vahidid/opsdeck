package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for agent, file := range map[string]string{
		"claude-code": ".claude/skills/opsdeck/SKILL.md",
		"codex":       ".codex/skills/opsdeck/SKILL.md",
		"gemini":      ".gemini/extensions/opsdeck/GEMINI.md",
	} {
		if _, err := installSkill(agent, "", false); err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		b, err := os.ReadFile(filepath.Join(home, file))
		if err != nil || !strings.Contains(string(b), "Working with opsdeck") {
			t.Fatalf("%s: %q %v", agent, b, err)
		}
		if _, err := installSkill(agent, "", false); err == nil {
			t.Fatalf("%s: a second install without --force must fail", agent)
		}
		if _, err := installSkill(agent, "", true); err != nil {
			t.Fatalf("%s --force: %v", agent, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/opsdeck/references/tools.md")); err != nil {
		t.Fatal("references are not installed")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".gemini/extensions/opsdeck/GEMINI.md")); strings.HasPrefix(string(b), "---") {
		t.Fatal("GEMINI.md keeps the front matter")
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini/extensions/opsdeck/SKILL.md")); err == nil {
		t.Fatal("the gemini extension keeps SKILL.md")
	}
	proj := t.TempDir()
	to, err := installSkill("cursor", proj, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(to)
	if !strings.HasPrefix(string(b), "---\ndescription: Work on real infrastructure") || !strings.Contains(string(b), "alwaysApply: false") {
		t.Fatalf("cursor rule: %.200s", b)
	}
	if _, err := installSkill("nope", "", false); err == nil {
		t.Fatal("an unknown agent must fail")
	}
}
