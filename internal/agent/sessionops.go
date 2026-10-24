package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"opsdeck/internal/audit"
	"opsdeck/internal/connect"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// maxEventID is the highest event sequence number in events ("e12" → 12):
// transient events take numbers too, so the count is not enough.
func maxEventID(events []Event) int {
	n := 0
	for _, e := range events {
		if id, ok := e["id"].(string); ok {
			if v, err := strconv.Atoi(strings.TrimPrefix(id, "e")); err == nil && v > n {
				n = v
			}
		}
	}
	return n
}

func intOf(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case float64:
		return int(x), true
	}
	return 0, false
}

// Fork copies a session into a new one: all of it, or (atEvent) everything
// before one of the user's messages, whose text is returned so it can be
// edited and sent again. Approvals given for the session are not copied.
func (m *Manager) Fork(w *workspace.Workspace, id, atEvent string) (*Session, string, error) {
	src, err := m.Get(w.Name(), id)
	if err != nil {
		return nil, "", err
	}
	src.mu.Lock()
	if src.cancel != nil {
		src.mu.Unlock()
		return nil, "", ErrBusy
	}
	msgs := make([]llm.Message, len(src.messages))
	for i, msg := range src.messages {
		msgs[i] = llm.Message{Role: msg.Role, Content: append([]llm.Block(nil), msg.Content...)}
	}
	events := append([]Event(nil), src.events...)
	info := src.info
	srcOutputs := src.outputDir()
	src.mu.Unlock()

	text := ""
	if atEvent != "" {
		cut := -1
		for i, e := range events {
			if e["id"] == atEvent {
				cut = i
				break
			}
		}
		if cut < 0 || events[cut]["type"] != "user" {
			return nil, "", errors.New("fork from a message of yours")
		}
		idx, ok := intOf(events[cut]["msgIndex"])
		if !ok || idx > len(msgs) {
			return nil, "", errors.New("this message is from an older version of opsdeck; fork the whole session instead")
		}
		text, _ = events[cut]["text"].(string)
		msgs, events = msgs[:idx], events[:cut]
	}
	now := time.Now().UTC()
	title := info.Title
	if !strings.HasSuffix(title, " (fork)") {
		title += " (fork)"
	}
	s := &Session{
		info: Info{ID: uuid.NewString()[:13], Workspace: w.Name(), Title: title, Model: info.Model, Mode: info.Mode, State: "idle",
			Created: now, Updated: now, ForkedFrom: info.ID},
		messages: msgs, events: events, granted: map[string]bool{}, subs: map[chan Event]struct{}{},
		pending: map[string]*pendingApproval{}, questions: map[string]*pendingQuestion{}, nextID: maxEventID(events),
	}
	s.path = filepath.Join(m.dir(w.Name()), s.info.ID+".json")
	// earlier full outputs stay readable with read_output
	if entries, err := os.ReadDir(srcOutputs); err == nil {
		os.MkdirAll(s.outputDir(), 0o700)
		for _, e := range entries {
			if b, err := os.ReadFile(filepath.Join(srcOutputs, e.Name())); err == nil {
				os.WriteFile(filepath.Join(s.outputDir(), e.Name()), b, 0o600)
			}
		}
	}
	if err := s.save(); err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	m.sessions[w.Name()+"/"+s.info.ID] = s
	m.mu.Unlock()
	return s, text, nil
}

// ---------------------------------------------------------------------------
// export

// Transcript renders the session as Markdown: messages, tool calls with
// their input and (shortened) output, approvals and errors.
func (s *Session) Transcript(perOutput int) string {
	s.mu.Lock()
	events := append([]Event(nil), s.events...)
	info := s.info
	s.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s · model %s · mode %s · started %s\n", info.ID, info.Model, info.Mode, info.Created.Format("2006-01-02 15:04 MST"))
	calls := map[string]Event{}
	str := func(v any) string { s, _ := v.(string); return s }
	for _, e := range events {
		if _, nested := e["parent"]; nested {
			continue // subagent detail: the delegate result carries the report
		}
		switch e["type"] {
		case "user":
			fmt.Fprintf(&b, "\n## User\n\n%s\n", str(e["text"]))
		case "notification":
			fmt.Fprintf(&b, "\n## Notification\n\n%s\n", str(e["text"]))
		case "assistant":
			fmt.Fprintf(&b, "\n## Agent\n\n%s\n", str(e["text"]))
		case "shell":
			fmt.Fprintf(&b, "\n## User ran\n\n```\n$ %s   (on %s)\n%s\n```\n", str(e["command"]), firstNonEmpty(str(e["target"]), "this computer"), truncate(str(e["output"]), perOutput))
		case "tool_call":
			calls[str(e["callId"])] = e
		case "tool_result":
			c := calls[str(e["callId"])]
			in, _ := json.MarshalIndent(c["input"], "", "  ")
			head := fmt.Sprintf("**%s**", str(c["tool"]))
			if t := str(c["target"]); t != "" {
				head += " on `" + t + "`"
			}
			if r := str(c["risk"]); r != "" {
				head += " (" + r + ")"
			}
			fmt.Fprintf(&b, "\n%s\n\n```json\n%s\n```\n", head, in)
			label := "Output"
			if isErr, _ := e["isError"].(bool); isErr {
				label = "Error"
			}
			if out := strings.TrimSpace(str(e["output"])); out != "" {
				fmt.Fprintf(&b, "\n%s:\n\n```\n%s\n```\n", label, truncate(out, perOutput))
			}
		case "approval_resolved":
			fmt.Fprintf(&b, "\n> Approval: %s %s\n", str(e["decision"]), str(e["note"]))
		case "error":
			fmt.Fprintf(&b, "\n> Error: %s\n", str(e["message"]))
		case "compaction":
			fmt.Fprintf(&b, "\n> Earlier conversation summarized:\n>\n> %s\n", strings.ReplaceAll(str(e["summary"]), "\n", "\n> "))
		}
	}
	return b.String()
}

var exportPrompts = map[string]string{
	"runbook": `Turn this infrastructure operations session into a reusable runbook in Markdown. Structure: a title (# …), when to use it, prerequisites, numbered steps with the exact commands (replace host names, addresses and versions that change from run to run with placeholders like <host>), how to verify each step, how to roll back, and pitfalls seen in the session. Leave out dead ends and failed attempts, keep what worked. Never include secrets. Answer with the runbook only.`,
	"skill":   `Turn this infrastructure operations session into an agent skill: a SKILL.md file. Start with YAML frontmatter (--- name: <kebab-case-name> / description: <one sentence: what it does and when to use it> ---), then the procedure for an agent: when it applies, what to check first, numbered steps with exact commands (placeholders like <host> for what changes), verification, rollback and pitfalls seen in the session. Leave out dead ends. Never include secrets. Answer with the file content only.`,
}

// Export saves a session to the workspace: as a report (the transcript), or
// as a runbook or skill written by a model from it. It returns the path.
func (m *Manager) Export(ctx context.Context, w *workspace.Workspace, id, as, title string) (string, error) {
	s, err := m.Get(w.Name(), id)
	if err != nil {
		return "", err
	}
	info := s.Info()
	title = strings.TrimSpace(firstNonEmpty(title, info.Title, "Agent session"))
	now := time.Now()
	record := func(path string, err error) (string, error) {
		e := audit.Entry{Workspace: w.Name(), Actor: "user", Action: "session.export", Target: path, Detail: map[string]any{"session": id, "as": as}}
		if err != nil {
			e.Result, e.Detail["error"] = "error", err.Error()
		}
		m.deps.Audit.Record(e)
		return path, err
	}
	if as == "report" || as == "" {
		body := m.redactor.Redact(s.Transcript(4000))
		return record(w.WriteReport(workspace.ReportMeta{Title: title, Session: id, Model: info.Model, Tags: []string{"session"}}, body, now))
	}
	prompt, ok := exportPrompts[as]
	if !ok {
		return "", fmt.Errorf("export as report, runbook or skill, not %q", as)
	}
	model := m.subagentModel(w, "", info.Model)
	if model == "" {
		return "", errors.New("writing a runbook or skill needs an API model (local agent CLIs cannot); export as a report instead")
	}
	p, bare, err := m.deps.Router().Resolve(model)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	transcript := truncate(m.redactor.Redact(s.Transcript(2500)), 150000)
	resp, err := p.Chat(ctx, llm.Request{Model: bare, MaxTokens: 8000, System: prompt,
		Messages: []llm.Message{{Role: "user", Content: []llm.Block{{Type: "text", Text: "Title: " + title + "\n\n" + transcript}}}}}, nil)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, b := range resp.Content {
		if b.Type == "text" {
			out.WriteString(b.Text)
		}
	}
	doc := strings.TrimSpace(stripFence(out.String()))
	if doc == "" {
		return "", errors.New("the model returned nothing")
	}
	doc = m.redactor.Redact(doc)
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 50 {
		slug = strings.Trim(slug[:50], "-")
	}
	slug = firstNonEmpty(slug, as)
	var rel string
	if as == "skill" {
		if name, _ := frontmatterName(doc); name != "" {
			slug = name
		}
		rel = uniqueDoc(w, "skills/"+slug+"/SKILL.md")
	} else {
		rel = uniqueDoc(w, "docs/runbooks/"+slug+".md")
		doc += fmt.Sprintf("\n\n---\n*Written by opsdeck from agent session %s on %s.*\n", id, now.Format("2006-01-02"))
	}
	return record(rel, w.WriteDoc(rel, doc+"\n"))
}

var nameLine = regexp.MustCompile(`(?m)^name:\s*["']?([a-z0-9][a-z0-9-]{0,62})["']?\s*$`)

func frontmatterName(doc string) (string, bool) {
	if !strings.HasPrefix(doc, "---") {
		return "", false
	}
	if m := nameLine.FindStringSubmatch(doc); m != nil {
		return m[1], true
	}
	return "", false
}

// stripFence removes a code fence the model may wrap its answer in.
func stripFence(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "```") && strings.HasSuffix(t, "```") {
		if i := strings.Index(t, "\n"); i > 0 {
			return strings.TrimSuffix(t[i+1:], "```")
		}
	}
	return s
}

// uniqueDoc returns rel, or rel with -2, -3… when it exists.
func uniqueDoc(w *workspace.Workspace, rel string) string {
	if _, err := w.ReadDoc(rel); err != nil {
		return rel
	}
	dir, file := filepath.Split(rel)
	if file == "SKILL.md" {
		base := strings.TrimSuffix(dir, "/")
		for i := 2; ; i++ {
			c := fmt.Sprintf("%s-%d/SKILL.md", base, i)
			if _, err := w.ReadDoc(c); err != nil {
				return c
			}
		}
	}
	ext := filepath.Ext(file)
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s%s-%d%s", dir, strings.TrimSuffix(file, ext), i, ext)
		if _, err := w.ReadDoc(c); err != nil {
			return c
		}
	}
}

// ---------------------------------------------------------------------------
// ! shell

// Shell runs a command the user typed with ! in the chat, on this computer
// or on workspace hosts, as the user (no approval: they typed it). Its
// output goes to the model with the user's next message.
func (m *Manager) Shell(ctx context.Context, w *workspace.Workspace, id, target, command string) (Event, error) {
	s, err := m.Get(w.Name(), id)
	if err != nil {
		return nil, err
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errors.New("the command is empty")
	}
	ctx = connect.WithActor(ctx, connect.Actor{Kind: "user"})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	start := time.Now()
	var out string
	code := 0
	label := "this computer"
	if target == "" {
		lp := m.local()
		if lp == nil {
			return nil, errors.New("access to this computer is off (App settings → This computer); run it on a host with !@host …")
		}
		out = runLocal(ctx, command, lp.Home)
	} else {
		env := m.env(w)
		hosts, err := env.hosts(target)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, h := range hosts {
			names = append(names, h.Name)
		}
		label = strings.Join(names, ", ")
		parts := make([]string, len(hosts))
		var wg sync.WaitGroup
		for i, h := range hosts {
			wg.Add(1)
			go func(i int, h *workspace.Resource) {
				defer wg.Done()
				res, err := m.deps.Hub.Exec(ctx, w, h, command, nil, 1<<20)
				parts[i] = formatExec(res, err)
				if len(hosts) > 1 {
					parts[i] = "=== " + h.Name + " ===\n" + parts[i]
				}
			}(i, h)
		}
		wg.Wait()
		out = strings.Join(parts, "\n")
	}
	if strings.HasPrefix(out, "exit code: ") {
		fmt.Sscanf(out, "exit code: %d", &code)
	}
	out = m.redactor.Redact(out)
	dur := time.Since(start).Milliseconds()
	m.deps.Audit.Record(audit.Entry{Workspace: w.Name(), Actor: "user", Action: "shell", Target: label,
		Detail: map[string]any{"command": m.redactor.Redact(command), "session": id, "exitCode": code}})
	ctxText := fmt.Sprintf("<user-shell target=%q>\nThe user ran this themselves:\n$ %s\n%s</user-shell>", label, command, truncate(out, 20000))
	s.mu.Lock()
	s.context = append(s.context, m.redactor.Redact(ctxText))
	s.mu.Unlock()
	ev := s.emit("shell", map[string]any{"command": m.redactor.Redact(command), "target": target, "label": label, "output": truncate(out, 200000),
		"exitCode": code, "durationMs": dur}, true)
	s.save()
	return ev, nil
}

// ---------------------------------------------------------------------------
// slash commands

// expandCommand turns "/name arguments" into the prompt of the workspace's
// command name ($ARGUMENTS, $1…$9); ok is false for other text.
func expandCommand(w *workspace.Workspace, text string) (prompt, name string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	head, args, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	head = strings.TrimSpace(head)
	args = strings.TrimSpace(args)
	for _, c := range w.Commands() {
		if c.Name != head {
			continue
		}
		p := c.Prompt
		fields := strings.Fields(args)
		for i := 9; i >= 1; i-- {
			v := ""
			if i <= len(fields) {
				v = fields[i-1]
			}
			p = strings.ReplaceAll(p, "$"+strconv.Itoa(i), v)
		}
		if strings.Contains(p, "$ARGUMENTS") {
			p = strings.ReplaceAll(p, "$ARGUMENTS", args)
		} else if args != "" {
			p += "\n\n" + args
		}
		return p, c.Name, true
	}
	return "", "", false
}

// ---------------------------------------------------------------------------
// compaction on request

// Compact summarizes the session's history now (the /compact command).
func (m *Manager) Compact(ctx context.Context, w *workspace.Workspace, id string) error {
	s, err := m.Get(w.Name(), id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	busy := s.cancel != nil
	n := len(s.messages)
	model := s.info.Model
	s.mu.Unlock()
	if busy {
		return ErrBusy
	}
	if n < 4 {
		return errors.New("the conversation is too short to summarize")
	}
	model = m.subagentModel(w, "", model)
	if model == "" {
		return errors.New("summarizing needs an API model (local agent CLIs keep their own context)")
	}
	p, bare, err := m.deps.Router().Resolve(model)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if err := m.compact(ctx, s, p, bare, true); err != nil {
		return err
	}
	return s.save()
}

// HarnessInfo is what the chat needs to know about a workspace's harness.
type HarnessInfo struct {
	Commands  []workspace.Command  `json:"commands"`
	Subagents []workspace.AgentDef `json:"subagents"`
	Web       string               `json:"web"`
	Search    string               `json:"search"`
	Local     bool                 `json:"local"`
	Models    []string             `json:"models"`
}

// Harness describes a workspace's slash commands, subagents and access.
func (m *Manager) Harness(w *workspace.Workspace) HarnessInfo {
	h := HarnessInfo{Commands: w.Commands(), Subagents: m.agents(w), Web: m.webAccess(w), Local: m.local() != nil, Models: w.Settings.Models}
	if h.Subagents == nil {
		h.Subagents = []workspace.AgentDef{}
	}
	if h.Models == nil {
		h.Models = []string{}
	}
	if m.deps.Web != nil && h.Web != WebOff {
		h.Search = m.deps.Web().SearchLabel()
	}
	return h
}
