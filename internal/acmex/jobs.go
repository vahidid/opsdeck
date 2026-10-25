package acmex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/workspace"
)

// Job is a running or finished issuance with its log.
type Job struct {
	ID        string
	Workspace string
	Cert      string
	Actor     string

	mu      sync.Mutex
	state   string // running | done | error
	lines   []string
	err     string
	result  *Issued
	started time.Time
	ended   time.Time
	done    chan struct{}
}

// JobView is a job as the API returns it.
type JobView struct {
	ID        string    `json:"id"`
	Cert      string    `json:"cert"`
	State     string    `json:"state"`
	Lines     []string  `json:"lines"`
	Total     int       `json:"total"`
	Error     string    `json:"error,omitempty"`
	Result    *Issued   `json:"result,omitempty"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitempty"`
	Actor     string    `json:"actor"`
	Workspace string    `json:"workspace"`
}

func (j *Job) log(line string) {
	j.mu.Lock()
	j.lines = append(j.lines, time.Now().Format("15:04:05")+"  "+line)
	j.mu.Unlock()
}

// View returns the job's state and the lines from index since.
func (j *Job) View(since int) JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	if since < 0 || since > len(j.lines) {
		since = 0
	}
	v := JobView{ID: j.ID, Cert: j.Cert, State: j.state, Lines: append([]string{}, j.lines[since:]...), Total: len(j.lines), Error: j.err, Result: j.result,
		Started: j.started, Ended: j.ended, Actor: j.Actor, Workspace: j.Workspace}
	return v
}

// Wait blocks until the job ended (or ctx is done) and returns its view.
func (j *Job) Wait(ctx context.Context) JobView {
	select {
	case <-j.done:
	case <-ctx.Done():
	}
	return j.View(0)
}

// Done reports whether the job ended.
func (j *Job) Done() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// Text is the whole log as text.
func (j *Job) Text() string {
	v := j.View(0)
	return strings.Join(v.Lines, "\n")
}

// Start begins an issuance in the background (one per certificate at a
// time) and returns the job to follow.
func (m *Manager) Start(ws *workspace.Workspace, r *workspace.Resource, actor string) (*Job, error) {
	if _, err := ReadSpec(r); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if j := m.runningLocked(ws.Name(), r.Name); j != nil {
		m.mu.Unlock()
		return j, nil
	}
	b := make([]byte, 6)
	rand.Read(b)
	j := &Job{ID: hex.EncodeToString(b), Workspace: ws.Name(), Cert: r.Name, Actor: actor, state: "running", started: time.Now(), done: make(chan struct{})}
	m.jobs[j.ID] = j
	m.mu.Unlock()
	// a copy of the resource: the workspace may be edited meanwhile
	res := *r
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		issued, err := m.Issue(ctx, ws, &res, j.log)
		j.mu.Lock()
		j.ended = time.Now()
		if err != nil {
			j.state, j.err = "error", err.Error()
			j.lines = append(j.lines, time.Now().Format("15:04:05")+"  error: "+err.Error())
		} else {
			j.state, j.result = "done", issued
		}
		j.mu.Unlock()
		close(j.done)
		if m.Record != nil {
			detail := map[string]any{"job": j.ID}
			if issued != nil {
				detail["item"], detail["domains"], detail["notAfter"] = issued.Item, issued.Info.Domains, issued.Info.NotAfter
			}
			m.Record(ws.Name(), actor, r.Name, detail, err)
		}
		// forget finished jobs after a while
		time.AfterFunc(2*time.Hour, func() {
			m.mu.Lock()
			delete(m.jobs, j.ID)
			m.mu.Unlock()
		})
	}()
	return j, nil
}

// Job finds a job by id.
func (m *Manager) Job(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// Jobs lists the jobs of a workspace, newest first.
func (m *Manager) Jobs(ws string) []JobView {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []JobView
	for _, j := range m.jobs {
		if j.Workspace == ws {
			out = append(out, j.View(0))
		}
	}
	for i := 1; i < len(out); i++ {
		for k := i; k > 0 && out[k].Started.After(out[k-1].Started); k-- {
			out[k], out[k-1] = out[k-1], out[k]
		}
	}
	return out
}

func (m *Manager) running(ws, cert string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runningLocked(ws, cert)
}

func (m *Manager) runningLocked(ws, cert string) *Job {
	for _, j := range m.jobs {
		if j.Workspace == ws && j.Cert == cert && !j.Done() {
			return j
		}
	}
	return nil
}

// String describes a job in one line.
func (v JobView) String() string {
	return fmt.Sprintf("%s %s (%s)", v.Cert, v.State, v.ID)
}
