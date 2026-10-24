package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"opsdeck/internal/audit"
	"opsdeck/internal/connect"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// Background tasks are commands that keep running after the tool call
// returns (a long deploy, a log tail, a watcher). The session is told when
// one finishes, or when a line of its output matches what the agent waits
// for: an idle session then starts a turn by itself to act on it.

const (
	maxBackground  = 10      // running tasks per session
	bgOutputBytes  = 1 << 20 // kept output per task (the latest)
	bgDefaultLimit = 4 * time.Hour
)

// TaskInfo describes a background task.
type TaskInfo struct {
	ID        string    `json:"id"`
	Workspace string    `json:"workspace"`
	Session   string    `json:"session"`
	CallID    string    `json:"callId"`
	Tool      string    `json:"tool"`
	Target    string    `json:"target"`
	Command   string    `json:"command"`
	NotifyOn  string    `json:"notifyOn,omitempty"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitzero"`
	State     string    `json:"state"` // running | done | failed | stopped
	ExitCode  int       `json:"exitCode"`
	Matched   string    `json:"matched,omitempty"` // the first line that matched NotifyOn
	Error     string    `json:"error,omitempty"`
	Lines     int       `json:"lines"`
}

// BgTask is one background task.
type BgTask struct {
	TaskInfo
	mu     sync.Mutex
	out    []byte
	cut    bool
	cancel context.CancelFunc
	done   chan struct{}
}

func (t *BgTask) snapshot() TaskInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.TaskInfo
}

func (t *BgTask) write(line string) {
	t.mu.Lock()
	t.out = append(t.out, line...)
	t.out = append(t.out, '\n')
	if len(t.out) > bgOutputBytes {
		t.out = t.out[len(t.out)-bgOutputBytes:]
		t.cut = true
	}
	t.Lines++
	t.mu.Unlock()
}

// Output returns the kept output (the latest megabyte).
func (t *BgTask) Output() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cut {
		return "... (earlier output dropped)\n" + string(t.out)
	}
	return string(t.out)
}

type bgRegistry struct {
	mu    sync.Mutex
	tasks map[string]*BgTask // id → task
}

func (b *bgRegistry) add(t *BgTask) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tasks == nil {
		b.tasks = map[string]*BgTask{}
	}
	b.tasks[t.ID] = t
}

func (b *bgRegistry) get(ws, session, id string) *BgTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.tasks[id]
	if t == nil || t.Workspace != ws || t.Session != session {
		return nil
	}
	return t
}

func (b *bgRegistry) list(ws, session string) []*BgTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*BgTask
	for _, t := range b.tasks {
		if t.Workspace == ws && (session == "" || t.Session == session) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Tasks returns the background tasks of a session.
func (m *Manager) Tasks(ws, session string) []TaskInfo {
	out := []TaskInfo{}
	for _, t := range m.bg.list(ws, session) {
		out = append(out, t.snapshot())
	}
	return out
}

// Task returns one background task and its output.
func (m *Manager) Task(ws, session, id string) (TaskInfo, string, error) {
	t := m.bg.get(ws, session, id)
	if t == nil {
		return TaskInfo{}, "", fmt.Errorf("background task %s not found", id)
	}
	return t.snapshot(), m.redactor.Redact(t.Output()), nil
}

// StopTask stops a running background task (the user's stop button).
func (m *Manager) StopTask(ws, session, id string) error {
	t := m.bg.get(ws, session, id)
	if t == nil {
		return fmt.Errorf("background task %s not found", id)
	}
	t.mu.Lock()
	running := t.State == "running"
	if running {
		t.State = "stopped"
	}
	t.mu.Unlock()
	if running {
		t.cancel()
	}
	return nil
}

// stopTasks ends the background tasks of a session, or every one with
// empty arguments (vault lock, shutdown).
func (m *Manager) stopTasks(ws, session string) {
	m.bg.mu.Lock()
	var all []*BgTask
	for _, t := range m.bg.tasks {
		if ws == "" || (t.Workspace == ws && t.Session == session) {
			all = append(all, t)
		}
	}
	m.bg.mu.Unlock()
	for _, t := range all {
		t.mu.Lock()
		if t.State == "running" {
			t.State = "stopped"
		}
		t.mu.Unlock()
		t.cancel()
	}
}

// bgKey carries what a tool needs to start a background task.
type bgKey struct{}

type bgRun func(ctx context.Context, onLine func(string)) (int, error)

type bgStarter func(tool, target, command, notifyOn string, limit time.Duration, run bgRun) (*BgTask, error)

// startBg starts a background task from a tool's Run.
func startBg(ctx context.Context, tool, target, command, notifyOn string, limit time.Duration, run bgRun) (string, error) {
	st, _ := ctx.Value(bgKey{}).(bgStarter)
	if st == nil {
		return "", errors.New("background tasks are not available here; run it without background")
	}
	t, err := st(tool, target, command, notifyOn, limit, run)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Started background task %s on %s. You will be told when it finishes", t.ID, target)
	if notifyOn != "" {
		msg += fmt.Sprintf(" or prints a line matching %q", notifyOn)
	}
	return msg + fmt.Sprintf("; meanwhile read its output with tasks action=output id=%s (or wait/stop). It keeps running when this turn ends.", t.ID), nil
}

// startBackground runs a command after its call returns. The task keeps the
// turn's actor (vault policies apply) but not its context: interrupting the
// turn does not stop it; the tasks tool, the chat and locking opsdeck do.
func (r *runner) startBackground(ctx context.Context, callID, tool, target, command, notifyOn string, limit time.Duration, run bgRun) (*BgTask, error) {
	s, m := r.s, r.m
	info := s.Info()
	running := 0
	for _, t := range m.bg.list(r.w.Name(), info.ID) {
		if t.snapshot().State == "running" {
			running++
		}
	}
	if running >= maxBackground {
		return nil, fmt.Errorf("%d background tasks are already running in this session; stop one first (tasks action=stop)", running)
	}
	var re *regexp.Regexp
	if notifyOn != "" {
		var err error
		if re, err = regexp.Compile(notifyOn); err != nil {
			return nil, fmt.Errorf("invalid notify_on pattern: %v", err)
		}
	}
	if limit <= 0 {
		limit = bgDefaultLimit
	}
	bctx, cancel := context.WithTimeout(connect.WithActor(context.Background(), connect.ActorFrom(ctx)), limit)
	t := &BgTask{TaskInfo: TaskInfo{ID: "bg-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:6], Workspace: r.w.Name(), Session: info.ID, CallID: callID, Tool: tool,
		Target: target, Command: m.redactor.Redact(command), NotifyOn: notifyOn, Started: time.Now().UTC(), State: "running"}, cancel: cancel, done: make(chan struct{})}
	m.bg.add(t)
	r.emit("task", map[string]any{"task": t.snapshot()}, true)
	go func() {
		defer cancel()
		defer close(t.done)
		code, err := run(bctx, func(line string) {
			t.write(line)
			if re != nil && re.MatchString(line) {
				t.mu.Lock()
				first := t.Matched == ""
				if first {
					t.Matched = m.redactor.Redact(oneLine(line, 400))
				}
				t.mu.Unlock()
				if first {
					m.notify(s, r.w, fmt.Sprintf("Background task %s (%s on %s) printed a line matching %q:\n%s\nIt keeps running; see more with tasks action=output id=%s.",
						t.ID, tool, target, notifyOn, t.snapshot().Matched, t.ID))
				}
			}
		})
		t.mu.Lock()
		t.Ended = time.Now().UTC()
		t.ExitCode = code
		stopped := t.State == "stopped"
		switch {
		case stopped:
		case err != nil:
			t.State, t.Error = "failed", m.redactor.Redact(err.Error())
		case bctx.Err() == context.DeadlineExceeded:
			t.State, t.Error = "failed", "stopped after "+limit.String()
		case code == 0:
			t.State = "done"
		default:
			t.State = "failed"
		}
		snap := t.snapshotLocked()
		t.mu.Unlock()
		r.emit("task", map[string]any{"task": snap}, true)
		m.deps.Audit.Record(audit.Entry{Workspace: r.w.Name(), Actor: "agent:" + info.ID, Action: "task." + snap.State, Target: target,
			Detail: map[string]any{"task": t.ID, "command": snap.Command, "exitCode": code, "session": info.ID}})
		if stopped {
			return
		}
		tail := lastLines(m.redactor.Redact(t.Output()), 30, 6000)
		msg := fmt.Sprintf("Background task %s (%s on %s: %s) %s after %s", t.ID, tool, target, oneLine(snap.Command, 200), snap.State,
			snap.Ended.Sub(snap.Started).Round(time.Second))
		if snap.Error != "" {
			msg += " (" + snap.Error + ")"
		} else {
			msg += fmt.Sprintf(" with exit code %d", code)
		}
		msg += ".\nLast output:\n" + firstNonEmpty(tail, "(no output)")
		m.notify(s, r.w, msg)
	}()
	return t, nil
}

func (t *BgTask) snapshotLocked() TaskInfo { return t.TaskInfo }

func lastLines(s string, n, maxBytes int) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxBytes {
		out = "…" + out[len(out)-maxBytes:]
	}
	return out
}

// notify tells the session something happened. An idle session starts a
// turn with it; a busy one gets it when its turn ends.
func (m *Manager) notify(s *Session, w *workspace.Workspace, text string) {
	s.mu.Lock()
	if s.cancel != nil {
		s.inbox = append(s.inbox, text)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if err := m.start(s, w, turnInput{Text: text, Notification: true}); errors.Is(err, ErrBusy) {
		s.mu.Lock()
		s.inbox = append(s.inbox, text)
		s.mu.Unlock()
	}
}

// deliverInbox starts a turn with the notifications that arrived while the
// last one ran.
func (m *Manager) deliverInbox(s *Session, w *workspace.Workspace) {
	s.mu.Lock()
	inbox := s.inbox
	s.inbox = nil
	s.mu.Unlock()
	if len(inbox) == 0 {
		return
	}
	if err := m.start(s, w, turnInput{Text: strings.Join(inbox, "\n\n"), Notification: true}); err != nil {
		s.mu.Lock()
		s.inbox = append(inbox, s.inbox...)
		s.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// the tasks tool

func toolTasks() Tool {
	return Tool{
		Def: llm.Tool{Name: "tasks", Description: "The session's background tasks (started with exec or local_exec background=true): list them, read a task's output (the latest lines, or grep), wait for one to finish, or stop one. You are told by yourself when a task finishes or prints a line matching its notify_on pattern, so you rarely need to wait.",
			Schema: obj(map[string]any{
				"action":      enum("What to do", "list", "output", "wait", "stop"),
				"id":          str("Task id (bg-…)"),
				"lines":       num("output: number of last lines (default 100)"),
				"grep":        str("output: only lines matching this regular expression"),
				"timeout_sec": num("wait: how long to wait (default 300, max 1800)"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, ID, Grep string
				Lines            int
				TimeoutSec       int `json:"timeout_sec"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if env.Tasks == nil {
				return nil, errors.New("background tasks are not available here")
			}
			c := &Call{Risk: RiskRead, Target: "session", Scope: ScopeLocal, Match: in.Action + " " + in.ID}
			get := func() (*BgTask, error) {
				t := env.Tasks(in.ID)
				if t == nil {
					return nil, fmt.Errorf("no background task %q in this session (tasks action=list)", in.ID)
				}
				return t, nil
			}
			switch in.Action {
			case "list":
				c.Run = func(ctx context.Context) (string, error) {
					var b strings.Builder
					for _, t := range env.AllTasks() {
						s := t.snapshot()
						fmt.Fprintf(&b, "- %s [%s] %s on %s: %s (started %s", s.ID, s.State, s.Tool, s.Target, oneLine(s.Command, 160), s.Started.Format("15:04:05"))
						if !s.Ended.IsZero() {
							fmt.Fprintf(&b, ", ended %s, exit %d", s.Ended.Format("15:04:05"), s.ExitCode)
						}
						fmt.Fprintf(&b, ", %d lines)\n", s.Lines)
					}
					if b.Len() == 0 {
						return "No background tasks in this session.", nil
					}
					return b.String(), nil
				}
			case "output":
				t, err := get()
				if err != nil {
					return nil, err
				}
				var re *regexp.Regexp
				if in.Grep != "" {
					if re, err = regexp.Compile("(?i)" + in.Grep); err != nil {
						return nil, fmt.Errorf("invalid grep pattern: %v", err)
					}
				}
				c.Run = func(ctx context.Context) (string, error) {
					s := t.snapshot()
					out := t.Output()
					head := fmt.Sprintf("%s [%s] %d lines\n", s.ID, s.State, s.Lines)
					if re != nil {
						return head + sliceLines(out, 0, 400, re, 0), nil
					}
					return head + lastLines(out, firstPositive(in.Lines, 100), toolOutputToModel-500), nil
				}
			case "wait":
				t, err := get()
				if err != nil {
					return nil, err
				}
				limit := time.Duration(max(1, min(firstPositive(in.TimeoutSec, 300), 1800))) * time.Second
				c.Run = func(ctx context.Context) (string, error) {
					tick := time.NewTicker(10 * time.Second)
					defer tick.Stop()
					deadline := time.After(limit)
					for {
						select {
						case <-t.done:
							s := t.snapshot()
							return fmt.Sprintf("%s %s with exit code %d.\nLast output:\n%s", s.ID, s.State, s.ExitCode, lastLines(t.Output(), 40, 8000)), nil
						case <-tick.C:
							reportProgress(ctx, "%s still running, %d lines", t.ID, t.snapshot().Lines)
						case <-deadline:
							return fmt.Sprintf("%s is still running after %s.", t.ID, limit), nil
						case <-ctx.Done():
							return "", ctx.Err()
						}
					}
				}
			case "stop":
				t, err := get()
				if err != nil {
					return nil, err
				}
				c.Risk, c.Preview = RiskChange, "stop background task "+t.ID+": "+t.Command
				c.Run = func(ctx context.Context) (string, error) {
					t.mu.Lock()
					running := t.State == "running"
					if running {
						t.State = "stopped"
					}
					t.mu.Unlock()
					if !running {
						return t.ID + " is not running.", nil
					}
					t.cancel()
					<-t.done
					return "stopped " + t.ID, nil
				}
			default:
				return nil, fmt.Errorf("unknown action %q", in.Action)
			}
			return c, nil
		},
	}
}
