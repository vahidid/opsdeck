package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"opsdeck/internal/knowledge"
	"opsdeck/internal/llm"
	"opsdeck/internal/skills"
)

// The knowledge index covers what the agent should know before it acts:
// skills, documentation, runbooks, repository docs, reports, memory notes
// and saved scripts. It is rebuilt when one of them changes (by size and
// modification time) and kept per workspace.

type knowledgeCache struct {
	key string
	ix  *knowledge.Index
}

var (
	knowledgeMu     sync.Mutex
	knowledgeCaches = map[string]*knowledgeCache{}
)

const maxIndexedText = 200000 // chars per entry

func knowledgeIndex(env *Env) *knowledge.Index {
	w := env.WS
	var key strings.Builder
	stamp := func(abs string) {
		if info, err := os.Stat(abs); err == nil {
			fmt.Fprintf(&key, "%s:%d:%d;", abs, info.Size(), info.ModTime().UnixNano())
		}
	}
	docs := w.Docs()
	for _, d := range docs {
		fmt.Fprintf(&key, "%s:%d:%d;", d.Path, d.Size, d.Updated.UnixNano())
	}
	all := AllSkills(w)
	for _, s := range all {
		if !s.Builtin {
			stamp(filepath.Join(w.Path, filepath.FromSlash(s.Path)))
		} else {
			key.WriteString(s.Name + ";")
		}
	}
	scripts := AllScripts(env)
	for _, s := range scripts {
		fmt.Fprintf(&key, "%s:%s;", s.Ref(), s.Hash)
	}
	stamp(env.MemoryPath)

	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()
	if c := knowledgeCaches[w.Path]; c != nil && c.key == key.String() {
		return c.ix
	}

	var entries []knowledge.Entry
	for _, s := range all {
		var body string
		if s.Builtin {
			// The library's skills are long general guides: their name,
			// description and headings say what they are for, while their
			// examples mention everything.
			full, _ := skills.Read(s.Name, "")
			body = headings(full)
		} else {
			body, _ = w.ReadDoc(s.Path)
			body = stripFrontmatter(body)
		}
		entries = append(entries, knowledge.Entry{Kind: knowledge.KindSkill, Ref: s.Name, Title: s.Name, Summary: s.Description,
			Text: clip(strings.TrimSpace(s.Category+"\n"+body), maxIndexedText), Own: !s.Builtin})
	}
	for _, d := range docs {
		if d.Path == "OPSDECK.md" || strings.HasPrefix(d.Path, "skills/") { // in the prompt / indexed as skills
			continue
		}
		body, err := w.ReadDoc(d.Path)
		if err != nil {
			continue
		}
		kind := knowledge.KindDoc
		if strings.HasPrefix(d.Path, "reports/") {
			kind = knowledge.KindReport
		}
		entries = append(entries, knowledge.Entry{Kind: kind, Ref: d.Path, Title: d.Title, Summary: strings.NewReplacer("/", " ", "-", " ", "_", " ", ".md", "").Replace(d.Path),
			Text: clip(body, maxIndexedText), Own: true})
	}
	for _, s := range scripts {
		entries = append(entries, knowledge.Entry{Kind: knowledge.KindScript, Ref: s.Name, Title: strings.NewReplacer("-", " ", "_", " ").Replace(s.Name),
			Summary: s.Description, Text: strings.Join(append(append(append([]string{}, s.When...), s.Tags...), s.OS...), " "), Own: true})
	}
	if mem, err := os.ReadFile(env.MemoryPath); err == nil {
		for _, note := range memoryNotes(string(mem)) {
			entries = append(entries, knowledge.Entry{Kind: knowledge.KindMemory, Ref: "memory", Text: note, Own: true})
		}
	}
	ix := knowledge.Build(entries)
	knowledgeCaches[w.Path] = &knowledgeCache{key: key.String(), ix: ix}
	return ix
}

func stripFrontmatter(md string) string {
	if strings.HasPrefix(md, "---") {
		if i := strings.Index(md[3:], "\n---"); i >= 0 {
			return strings.TrimLeft(md[3+i+4:], "\n")
		}
	}
	return md
}

// headings are the markdown headings of a text.
func headings(md string) string {
	var out []string
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "#") {
			out = append(out, strings.TrimLeft(line, "# "))
		}
	}
	return strings.Join(out, "\n")
}

// memoryNotes splits memory into its notes: top-level bullets, or
// paragraphs when it is free text.
func memoryNotes(mem string) []string {
	var out []string
	var cur []string
	flush := func() {
		if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
			out = append(out, s)
		}
		cur = nil
	}
	for _, line := range strings.Split(mem, "\n") {
		t := strings.TrimSpace(line)
		top := line == t
		if t == "" || (top && (strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "#"))) {
			flush()
		}
		if t != "" {
			cur = append(cur, line)
		}
	}
	flush()
	return out
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// openWith is how the agent opens a knowledge entry.
func openWith(h knowledge.Hit) string {
	switch h.Kind {
	case knowledge.KindSkill:
		return "load_skill name=" + h.Ref
	case knowledge.KindScript:
		return "script action=read name=" + h.Ref + ", or run_script"
	case knowledge.KindMemory:
		return "in the memory above"
	}
	return "read_doc path=" + h.Ref
}

func toolKnowledge() Tool {
	return Tool{
		Def: llm.Tool{Name: "knowledge", Description: "Search everything this workspace knows — skills (its own and the built-in library), documentation and runbooks, repository docs, reports of earlier work, memory notes and saved scripts — in one call, English or Persian. Returns the best matches with a snippet and how to open each. Use it before an unfamiliar task, when the user refers to earlier work or a procedure, or instead of listing and reading docs one by one.",
			Schema: obj(map[string]any{
				"query": str("What you are looking for, in words (e.g. 'rotate tls certificate nginx', 'backup postgres')"),
				"kind":  enum("Only this kind (default any)", "any", "skill", "doc", "report", "memory", "script"),
				"limit": map[string]any{"type": "integer", "description": "Maximum results (default 8, at most 20)"},
			}, "query")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Query, Kind string
				Limit       int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if in.Kind == "any" {
				in.Kind = ""
			}
			if in.Limit <= 0 {
				in.Limit = 8
			}
			if in.Limit > 20 {
				in.Limit = 20
			}
			return &Call{Risk: RiskRead, Target: "knowledge", Run: func(ctx context.Context) (string, error) {
				ix := knowledgeIndex(env)
				hits := ix.Search(in.Query, in.Kind, in.Limit)
				if len(hits) == 0 {
					return fmt.Sprintf("Nothing matches %q among %d entries. Try other words, or the list of docs (read_doc without a path).", in.Query, ix.Len()), nil
				}
				var b strings.Builder
				for i, h := range hits {
					fmt.Fprintf(&b, "%d. [%s] %s", i+1, h.Kind, firstNonEmpty(h.Title, h.Ref))
					if h.Ref != h.Title && h.Kind != knowledge.KindMemory {
						fmt.Fprintf(&b, " (%s)", h.Ref)
					}
					fmt.Fprintf(&b, " — open with %s\n", openWith(h))
					if h.Summary != "" && h.Kind != knowledge.KindDoc && h.Kind != knowledge.KindReport {
						fmt.Fprintf(&b, "   %s\n", oneLine(h.Summary, 200))
					}
					if h.Snippet != "" {
						fmt.Fprintf(&b, "   > %s\n", h.Snippet)
					}
				}
				return b.String(), nil
			}}, nil
		},
	}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Knowledge attached to the user's message

const (
	contextHits     = 5
	minContextScore = 4.0   // below this a match is a coincidence of common words
	relContextScore = 0.4   // of the best match
	preloadMax      = 12000 // chars of a workspace skill shown in full
)

// Recall is what opsdeck found for a message: shown in the chat and given
// to the model with the message.
type Recall struct {
	Items     []RecallItem `json:"items"`
	Preloaded string       `json:"preloaded,omitempty"` // skill whose text is included
}

type RecallItem struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Title string `json:"title,omitempty"`
}

// recall finds the knowledge matching a user message. history is the
// conversation so far (a skill already loaded there is not included again).
func recall(env *Env, text string, history []llm.Message) (*Recall, string) {
	ix := knowledgeIndex(env)
	var hits []knowledge.Hit
	for _, h := range ix.Search(text, "", 12) {
		if h.Kind == knowledge.KindMemory { // memory is in the system prompt already
			continue
		}
		if h.Score < minContextScore || (len(hits) > 0 && h.Score < relContextScore*hits[0].Score) {
			break
		}
		hits = append(hits, h)
		if len(hits) == contextHits {
			break
		}
	}
	if len(hits) == 0 {
		return nil, ""
	}
	r := &Recall{}
	// The workspace skill that clearly fits best is included in full: one
	// round trip less, and it is the team's own procedure.
	var preload string
	if top, ok := bestSkill(hits); ok && !skillLoaded(history, top.Ref) {
		for _, s := range AllSkills(env.WS) {
			if s.Name != top.Ref || s.Builtin {
				continue
			}
			if body, err := env.WS.ReadDoc(s.Path); err == nil && len(body) <= preloadMax {
				r.Preloaded = s.Name
				preload = fmt.Sprintf("\n<skill name=%q>\n%s\n</skill>\n", s.Name, strings.TrimSpace(body))
				if len(s.Files) > 0 {
					preload += fmt.Sprintf("Supporting files (load_skill name=%s file=<path>): %s\n", s.Name, strings.Join(s.Files, ", "))
				}
			}
		}
	}
	var b strings.Builder
	b.WriteString("<opsdeck-context>\nWorkspace knowledge matching this message (found by opsdeck, not written by the user). Use what applies before you start: follow a matching skill, runbook or script rather than improvising; ignore what does not fit.\n")
	for _, h := range hits {
		r.Items = append(r.Items, RecallItem{Kind: h.Kind, Ref: h.Ref, Title: h.Title})
		title := h.Title
		if h.Kind == knowledge.KindSkill || h.Kind == knowledge.KindScript {
			title = h.Ref
		}
		fmt.Fprintf(&b, "- %s %s", h.Kind, title)
		if h.Summary != "" && h.Kind != knowledge.KindDoc && h.Kind != knowledge.KindReport {
			b.WriteString(": " + oneLine(h.Summary, 200))
		} else if h.Ref != title {
			b.WriteString(" (" + h.Ref + ")")
		}
		if h.Kind == knowledge.KindSkill && h.Ref == r.Preloaded {
			b.WriteString(" — included below\n")
			continue
		}
		b.WriteString(" — " + openWith(h) + "\n")
		if h.Snippet != "" {
			b.WriteString("  > " + h.Snippet + "\n")
		}
	}
	b.WriteString(preload)
	b.WriteString("</opsdeck-context>")
	return r, b.String()
}

// bestSkill is the best matching skill when it is close to the best match
// overall and well ahead of the other skills.
func bestSkill(hits []knowledge.Hit) (knowledge.Hit, bool) {
	var skills []knowledge.Hit
	for _, h := range hits {
		if h.Kind == knowledge.KindSkill {
			skills = append(skills, h)
		}
	}
	if len(skills) == 0 || skills[0].Score < 0.6*hits[0].Score {
		return knowledge.Hit{}, false
	}
	if len(skills) > 1 && skills[0].Score < 1.5*skills[1].Score {
		return knowledge.Hit{}, false
	}
	return skills[0], true
}

func skillLoaded(history []llm.Message, name string) bool {
	marker := fmt.Sprintf("<skill name=%q>", name)
	for _, m := range history {
		for _, bl := range m.Content {
			if bl.Type == "tool_use" && bl.Name == "load_skill" {
				var in struct{ Name, File string }
				if json.Unmarshal(bl.Input, &in) == nil && in.Name == name && in.File == "" {
					return true
				}
			}
			if bl.Type == "text" && strings.Contains(bl.Text, marker) {
				return true
			}
		}
	}
	return false
}
