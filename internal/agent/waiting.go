package agent

import (
	"sort"
	"sync"
	"time"
)

// Waiting is something an agent waits for the user to decide, in any
// session of any workspace: an approval card or a question (ask_user).
// The UI shows them wherever the user is, and opsdeck notifies the desktop,
// so an agent outside opsdeck (Claude Code over MCP) is not left waiting
// for an approval nobody sees.
type Waiting struct {
	Kind         string    `json:"kind"` // approval | question
	ID           string    `json:"id"`   // approvalId or questionId
	Workspace    string    `json:"workspace"`
	Session      string    `json:"session"`
	SessionTitle string    `json:"sessionTitle"`
	Client       string    `json:"client,omitempty"` // outside agent (access key label)
	Created      time.Time `json:"created"`

	// approval
	Tool        string   `json:"tool,omitempty"`
	Target      string   `json:"target,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Risk        string   `json:"risk,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Preview     string   `json:"preview,omitempty"`
	Input       any      `json:"input,omitempty"`
	Hosts       []string `json:"hosts,omitempty"`
	GrantLabel  string   `json:"grantLabel,omitempty"`
	// Secret is set when approving needs a password typed into the card;
	// Stage is "next" for a "continue?" step (canary, plan steps).
	Secret bool   `json:"secret,omitempty"`
	Stage  string `json:"stage,omitempty"`
	Title  string `json:"title,omitempty"`

	// question
	Question string `json:"question,omitempty"`
	Options  any    `json:"options,omitempty"`
	Multiple bool   `json:"multiple,omitempty"`
}

type waitingRegistry struct {
	mu    sync.Mutex
	items map[string]Waiting // ws/session/id
	subs  map[chan struct{}]struct{}
	// onAdd runs (in its own goroutine) for each new item: desktop
	// notifications.
	onAdd func(Waiting)
}

func waitKey(w Waiting) string { return w.Workspace + "/" + w.Session + "/" + w.ID }

func (r *waitingRegistry) add(w Waiting) {
	r.mu.Lock()
	if r.items == nil {
		r.items = map[string]Waiting{}
	}
	r.items[waitKey(w)] = w
	onAdd := r.onAdd
	r.notifyLocked()
	r.mu.Unlock()
	if onAdd != nil {
		go onAdd(w)
	}
}

func (r *waitingRegistry) remove(ws, session, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := ws + "/" + session + "/" + id
	if _, ok := r.items[k]; ok {
		delete(r.items, k)
		r.notifyLocked()
	}
}

func (r *waitingRegistry) notifyLocked() {
	for ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Waiting lists what agents wait for, oldest first.
func (m *Manager) Waiting() []Waiting {
	r := &m.waiting
	r.mu.Lock()
	out := make([]Waiting, 0, len(r.items))
	for _, w := range r.items {
		out = append(out, w)
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// WatchWaiting returns a channel that gets a signal whenever the list
// changes, and the func to stop watching.
func (m *Manager) WatchWaiting() (<-chan struct{}, func()) {
	r := &m.waiting
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	if r.subs == nil {
		r.subs = map[chan struct{}]struct{}{}
	}
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

// OnWaiting sets what runs when an agent starts to wait for the user.
func (m *Manager) OnWaiting(f func(Waiting)) {
	m.waiting.mu.Lock()
	m.waiting.onAdd = f
	m.waiting.mu.Unlock()
}

// waitingFor describes a session's approval card or question.
func (r *runner) waitingFor(kind, id string, req map[string]any) Waiting {
	info := r.s.Info()
	w := Waiting{Kind: kind, ID: id, Workspace: r.w.Name(), Session: info.ID, SessionTitle: info.Title, Client: info.Client, Created: time.Now().UTC()}
	str := func(k string) string { s, _ := req[k].(string); return s }
	switch kind {
	case "approval":
		w.Tool, w.Target, w.Environment, w.Reason, w.Preview = str("tool"), str("target"), str("environment"), str("reason"), str("preview")
		w.GrantLabel, w.Stage, w.Title = str("grantLabel"), str("stage"), str("title")
		w.Risk = str("risk")
		w.Input = req["input"]
		w.Hosts, _ = req["hosts"].([]string)
		_, w.Secret = req["secret"]
	case "question":
		w.Question, w.Options = str("question"), req["options"]
		w.Multiple, _ = req["multiple"].(bool)
	}
	if w.SessionTitle == "" {
		w.SessionTitle = "New session"
	}
	return w
}
