package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"opsdeck/internal/audit"
	"opsdeck/internal/connect"
	"opsdeck/internal/docker"
	"opsdeck/internal/kube"
	"opsdeck/internal/llm"
	"opsdeck/internal/mcpx"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// Event is a timeline entry streamed to clients: {"id","ts","type",...}.
type Event map[string]any

// Info describes a session.
type Info struct {
	ID        string    `json:"id"`
	Workspace string    `json:"workspace"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	Mode      string    `json:"mode"`
	State     string    `json:"state"` // idle | running | waiting_approval
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	Usage     llm.Usage `json:"usage"`
}

// Session is one conversation with the agent inside a workspace.
type Session struct {
	mu       sync.Mutex
	info     Info
	messages []llm.Message
	events   []Event
	granted  map[string]bool
	subs     map[chan Event]struct{}
	cancel   context.CancelFunc
	pending  map[string]chan approval
	path     string
	nextID   int
}

type approval struct {
	decision string
	note     string
}

type stored struct {
	Info     Info          `json:"info"`
	Messages []llm.Message `json:"messages"`
	Events   []Event       `json:"events"`
	Granted  []string      `json:"granted"`
}

// Deps are the services sessions use.
type Deps struct {
	Workspaces *workspace.Manager
	Hub        *connect.Hub
	Docker     *docker.Client
	Kube       *kube.Manager
	Vault      *vault.Vault
	MCP        *mcpx.Manager
	Audit      *audit.Log
	Router     func() *llm.Router
	StateDir   func(ws string) string
	// DefaultModel returns the model for new sessions of a workspace.
	DefaultModel func(w *workspace.Workspace) string
}

// Manager owns sessions.
type Manager struct {
	deps     Deps
	redactor *Redactor
	mu       sync.Mutex
	sessions map[string]*Session // ws/id
}

func NewManager(d Deps) *Manager {
	return &Manager{deps: d, redactor: NewRedactor(d.Vault.SecretValues), sessions: map[string]*Session{}}
}

func (m *Manager) dir(ws string) string { return filepath.Join(m.deps.StateDir(ws), "sessions") }

// List returns sessions of a workspace, newest first.
func (m *Manager) List(ws string) []Info {
	entries, _ := os.ReadDir(m.dir(ws))
	out := []Info{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if s, err := m.Get(ws, id); err == nil {
			s.mu.Lock()
			out = append(out, s.info)
			s.mu.Unlock()
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// Create starts a new session.
func (m *Manager) Create(w *workspace.Workspace, model, mode string) (*Session, error) {
	if model == "" {
		model = m.deps.DefaultModel(w)
	}
	if mode == "" {
		mode = ModeAsk
	}
	if mode != ModePlan && mode != ModeAsk && mode != ModeAuto {
		return nil, fmt.Errorf("invalid mode %q", mode)
	}
	now := time.Now().UTC()
	s := &Session{
		info:    Info{ID: uuid.NewString()[:13], Workspace: w.Name(), Title: "New session", Model: model, Mode: mode, State: "idle", Created: now, Updated: now},
		granted: map[string]bool{}, subs: map[chan Event]struct{}{}, pending: map[string]chan approval{},
	}
	s.path = filepath.Join(m.dir(w.Name()), s.info.ID+".json")
	if err := s.save(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[w.Name()+"/"+s.info.ID] = s
	m.mu.Unlock()
	return s, nil
}

// Get loads a session (cached in memory once loaded).
func (m *Manager) Get(ws, id string) (*Session, error) {
	if strings.ContainsAny(id, "/\\.") {
		return nil, errors.New("invalid session id")
	}
	key := ws + "/" + id
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[key]; s != nil {
		return s, nil
	}
	path := filepath.Join(m.dir(ws), id+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("session not found")
	}
	var st stored
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	s := &Session{info: st.Info, messages: st.Messages, events: st.Events, granted: map[string]bool{},
		subs: map[chan Event]struct{}{}, pending: map[string]chan approval{}, path: path, nextID: len(st.Events)}
	for _, g := range st.Granted {
		s.granted[g] = true
	}
	if s.info.State != "idle" { // the process stopped mid-run
		s.info.State = "idle"
	}
	m.sessions[key] = s
	return s, nil
}

// Delete removes a session.
func (m *Manager) Delete(ws, id string) error {
	s, err := m.Get(ws, id)
	if err != nil {
		return err
	}
	s.Interrupt()
	m.mu.Lock()
	delete(m.sessions, ws+"/"+id)
	m.mu.Unlock()
	return os.Remove(s.path)
}

// InterruptAll stops every running session (vault lock, shutdown).
func (m *Manager) InterruptAll() {
	m.mu.Lock()
	var all []*Session
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.Interrupt()
	}
}

// ---------------------------------------------------------------------------
// Session methods

func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Events returns the persisted timeline.
func (s *Session) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

// Update changes title/model/mode.
func (s *Session) Update(title, model, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode != "" {
		if mode != ModePlan && mode != ModeAsk && mode != ModeAuto {
			return fmt.Errorf("invalid mode %q", mode)
		}
		s.info.Mode = mode
	}
	if model != "" {
		s.info.Model = model
	}
	if title != "" {
		s.info.Title = title
	}
	s.info.Updated = time.Now().UTC()
	return s.saveLocked()
}

// Subscribe returns a channel of new events and an unsubscribe func.
func (s *Session) Subscribe() (chan Event, func()) {
	ch := make(chan Event, 512)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

// emit records (unless transient) and broadcasts an event.
func (s *Session) emit(typ string, data map[string]any, persist bool) Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	ev := Event{"id": fmt.Sprintf("e%d", s.nextID), "ts": time.Now().UTC().Format(time.RFC3339Nano), "type": typ}
	for k, v := range data {
		ev[k] = v
	}
	if persist {
		s.events = append(s.events, ev)
	}
	for ch := range s.subs {
		select {
		case ch <- ev:
		default: // slow client; it will resync from the event list
		}
	}
	return ev
}

func (s *Session) setState(state string) {
	s.mu.Lock()
	s.info.State = state
	s.info.Updated = time.Now().UTC()
	usage := s.info.Usage
	s.mu.Unlock()
	s.emit("status", map[string]any{"state": state, "usage": usage}, false)
}

// Interrupt cancels the running turn.
func (s *Session) Interrupt() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Resolve answers a pending approval.
func (s *Session) Resolve(approvalID, decision, note string) error {
	if decision != "approve" && decision != "deny" && decision != "always" {
		return fmt.Errorf("invalid decision %q", decision)
	}
	s.mu.Lock()
	ch := s.pending[approvalID]
	delete(s.pending, approvalID)
	s.mu.Unlock()
	if ch == nil {
		return errors.New("no such pending approval")
	}
	ch <- approval{decision, note}
	return nil
}

func (s *Session) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Session) saveLocked() error {
	granted := make([]string, 0, len(s.granted))
	for g := range s.granted {
		granted = append(granted, g)
	}
	b, err := json.Marshal(stored{Info: s.info, Messages: s.messages, Events: s.events, Granted: granted})
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
