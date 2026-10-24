package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentDef is a subagent of the workspace (agents/<name>.md): instructions
// the main agent can hand a task to, with its own tools and model.
//
//	---
//	description: Checks TLS certificates on all web hosts
//	mode: read-only          # or: changes (asks like the session)
//	model: ollama/qwen2.5    # optional
//	tools: exec, net_check   # optional: limit the tools
//	---
//	You check certificates…
type AgentDef struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ReadOnly    bool     `json:"readOnly"`
	Model       string   `json:"model,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Prompt      string   `json:"prompt"`
	Path        string   `json:"path"`
}

// Command is a slash command of the workspace (commands/<name>.md): a
// prompt template; $ARGUMENTS is replaced by what follows the command.
type Command struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	Prompt       string `json:"prompt"`
	Path         string `json:"path"`
}

// frontmatter splits "---\nkey: value\n---\nbody" into its fields and body.
func frontmatter(text string) (map[string]string, string) {
	fields := map[string]string{}
	text = strings.TrimPrefix(text, "\uFEFF")
	if !strings.HasPrefix(text, "---") {
		return fields, text
	}
	end := strings.Index(text[3:], "\n---")
	if end < 0 {
		return fields, text
	}
	for _, line := range strings.Split(text[3:3+end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if i := strings.Index(v, " #"); i >= 0 {
			v = v[:i]
		}
		fields[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	body := text[3+end+4:]
	return fields, strings.TrimLeft(body, "\r\n")
}

func listField(v string) []string {
	v = strings.Trim(strings.TrimSpace(v), "[]")
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.Trim(strings.TrimSpace(p), `"'`); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (w *Workspace) mdFiles(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(w.Path, dir, "*.md"))
	sort.Strings(matches)
	return matches
}

// Agents lists the workspace's subagents.
func (w *Workspace) Agents() []AgentDef {
	out := []AgentDef{}
	for _, abs := range w.mdFiles("agents") {
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		f, body := frontmatter(string(b))
		name := firstNonEmptyStr(f["name"], strings.TrimSuffix(filepath.Base(abs), ".md"))
		mode := strings.ToLower(f["mode"])
		rel, _ := filepath.Rel(w.Path, abs)
		out = append(out, AgentDef{Name: name, Description: firstNonEmptyStr(f["description"], mdTitle(abs)),
			ReadOnly: mode == "" || mode == "read-only" || mode == "readonly" || mode == "plan",
			Model:    f["model"], Tools: listField(f["tools"]), Prompt: strings.TrimSpace(body), Path: filepath.ToSlash(rel)})
	}
	return out
}

// Commands lists the workspace's slash commands.
func (w *Workspace) Commands() []Command {
	out := []Command{}
	for _, abs := range w.mdFiles("commands") {
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		f, body := frontmatter(string(b))
		name := strings.TrimPrefix(firstNonEmptyStr(f["name"], strings.TrimSuffix(filepath.Base(abs), ".md")), "/")
		rel, _ := filepath.Rel(w.Path, abs)
		out = append(out, Command{Name: name, Description: firstNonEmptyStr(f["description"], mdTitle(abs)), ArgumentHint: f["argument-hint"],
			Prompt: strings.TrimSpace(body), Path: filepath.ToSlash(rel)})
	}
	return out
}

func firstNonEmptyStr(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
