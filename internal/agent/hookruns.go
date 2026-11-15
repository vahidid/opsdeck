package agent

import (
	"sync"
	"time"
)

// HookRun is the last time a hook ran and what came of it, for the hook
// editors. Kept in memory since opsdeck started; a changed hook is a new
// hook (its key is a hash of its definition) and starts without one.
type HookRun struct {
	At         time.Time `json:"at"`
	Outcome    string    `json:"outcome"` // ok | deny | ask | allow | error
	Message    string    `json:"message,omitempty"`
	DurationMs int64     `json:"durationMs"`
	Workspace  string    `json:"workspace"`
	Session    string    `json:"session,omitempty"`
	Tool       string    `json:"tool,omitempty"`
}

type hookRuns struct {
	mu   sync.Mutex
	last map[string]HookRun
}

func (h *hookRuns) record(key string, run HookRun) {
	if key == "" {
		return
	}
	h.mu.Lock()
	if h.last == nil {
		h.last = map[string]HookRun{}
	}
	h.last[key] = run
	h.mu.Unlock()
}

// HookRuns returns the last run of each hook key (nil: not run since
// opsdeck started).
func (m *Manager) HookRuns(keys []string) []*HookRun {
	m.hookRuns.mu.Lock()
	defer m.hookRuns.mu.Unlock()
	out := make([]*HookRun, len(keys))
	for i, k := range keys {
		if r, ok := m.hookRuns.last[k]; ok {
			r := r
			out[i] = &r
		}
	}
	return out
}
