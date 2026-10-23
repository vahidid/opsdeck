package ansible

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"opsdeck/internal/sysproc"
	"opsdeck/internal/workspace"
)

// Run kinds.
const (
	KindPlaybook = "playbook" // ansible-playbook
	KindAdhoc    = "adhoc"    // ansible <pattern> -m <module>
	KindGalaxy   = "galaxy"   // ansible-galaxy install -r <requirements>
	KindLint     = "lint"     // ansible-lint
)

// Run statuses.
const (
	StatusRunning     = "running"
	StatusOK          = "ok"
	StatusFailed      = "failed"      // a task failed on some host
	StatusUnreachable = "unreachable" // some host could not be reached
	StatusCanceled    = "canceled"
	StatusError       = "error" // ansible itself failed (syntax, missing vault password…)
)

// RunOptions describe what to run.
type RunOptions struct {
	Kind         string `json:"kind"`
	Playbook     string `json:"playbook,omitempty"`
	Inventory    string `json:"inventory,omitempty"` // a path in the project, "workspace", or "" for the default
	Limit        string `json:"limit,omitempty"`
	Tags         string `json:"tags,omitempty"`
	SkipTags     string `json:"skipTags,omitempty"`
	ExtraVars    string `json:"extraVars,omitempty"` // a YAML or JSON mapping
	Check        bool   `json:"check,omitempty"`
	Diff         bool   `json:"diff,omitempty"`
	Verbosity    int    `json:"verbosity,omitempty"`
	Forks        int    `json:"forks,omitempty"`
	StartAt      string `json:"startAt,omitempty"`
	Become       bool   `json:"become,omitempty"`
	Pattern      string `json:"pattern,omitempty"` // adhoc
	Module       string `json:"module,omitempty"`  // adhoc
	Args         string `json:"args,omitempty"`    // adhoc
	Requirements string `json:"requirements,omitempty"`
	Force        bool   `json:"force,omitempty"` // galaxy: reinstall
}

// HostRecap is one line of PLAY RECAP.
type HostRecap struct {
	Host        string `json:"host"`
	OK          int    `json:"ok"`
	Changed     int    `json:"changed"`
	Unreachable int    `json:"unreachable"`
	Failed      int    `json:"failed"`
	Skipped     int    `json:"skipped"`
	Rescued     int    `json:"rescued"`
	Ignored     int    `json:"ignored"`
}

// RunInfo is the record of one execution.
type RunInfo struct {
	ID         string      `json:"id"`
	Workspace  string      `json:"workspace"`
	Project    string      `json:"project"`
	Options    RunOptions  `json:"options"`
	Command    string      `json:"command"`
	Actor      string      `json:"actor"`
	Started    time.Time   `json:"started"`
	Ended      *time.Time  `json:"ended,omitempty"`
	Status     string      `json:"status"`
	ExitCode   int         `json:"exitCode"`
	Recap      []HostRecap `json:"recap,omitempty"`
	Error      string      `json:"error,omitempty"`
	LogLines   int         `json:"logLines"`
	Hosts      []string    `json:"hosts,omitempty"` // workspace hosts of a generated inventory
	DurationMs int64       `json:"durationMs,omitempty"`
}

// Run is one execution with its log, live or loaded from the history.
type Run struct {
	mu      sync.Mutex
	info    RunInfo
	lines   []LogLine
	size    int
	changed chan struct{} // closed when lines are added or the run ends
	cancel  context.CancelFunc
	done    chan struct{}
	logPath string
}

// LogLine is one line of output.
type LogLine struct {
	N    int       `json:"n"`
	TS   time.Time `json:"ts"`
	Text string    `json:"line"`
}

const maxLogMemory = 64 << 20

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)[:6]
}

func runsDir(w *workspace.Workspace) string { return filepath.Join(w.StateDir(), "ansible") }

// Info is a copy of the run's record.
func (r *Run) Info() RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.info
	i.Recap = append([]HostRecap(nil), r.info.Recap...)
	return i
}

// ID is the run's id.
func (r *Run) ID() string { return r.info.ID }

// Done is closed when the run has ended.
func (r *Run) Done() <-chan struct{} { return r.done }

// Cancel stops the run.
func (r *Run) Cancel() {
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *Run) add(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts := time.Now().UTC()
	if n := len(r.lines); n > 0 && !ts.After(r.lines[n-1].TS) {
		ts = r.lines[n-1].TS.Add(time.Microsecond) // strictly increasing, for resuming a stream
	}
	l := LogLine{N: r.info.LogLines, TS: ts, Text: text}
	r.info.LogLines++
	if r.size < maxLogMemory {
		r.lines = append(r.lines, l)
		r.size += len(text)
	}
	close(r.changed)
	r.changed = make(chan struct{})
}

// Follow calls fn for every log line from line from on, waiting for new
// lines while the run is live. It returns when the run has ended and every
// line was delivered, or when ctx ends.
func (r *Run) Follow(ctx context.Context, from int, fn func(LogLine) error) error {
	for {
		r.mu.Lock()
		var batch []LogLine
		if len(r.lines) > 0 && from < r.lines[0].N+len(r.lines) {
			start := from - r.lines[0].N
			if start < 0 {
				start = 0
			}
			batch = append(batch, r.lines[start:]...)
		}
		ch := r.changed
		live := r.info.Ended == nil
		r.mu.Unlock()
		for _, l := range batch {
			if err := fn(l); err != nil {
				return err
			}
			from = l.N + 1
		}
		if !live && len(batch) == 0 {
			return nil
		}
		if len(batch) > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// Tail returns the last n lines (all lines when n <= 0).
func (r *Run) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := 0
	if n > 0 && len(r.lines) > n {
		start = len(r.lines) - n
	}
	out := make([]string, 0, len(r.lines)-start)
	for _, l := range r.lines[start:] {
		out = append(out, l.Text)
	}
	return out
}

// ---------------------------------------------------------------------------
// Starting a run

var (
	moduleRe = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)
	ansiRe   = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	recapRe  = regexp.MustCompile(`^(\S+)\s*:\s*ok=(\d+)\s+changed=(\d+)\s+unreachable=(\d+)\s+failed=(\d+)(?:\s+skipped=(\d+))?(?:\s+rescued=(\d+))?(?:\s+ignored=(\d+))?`)
)

// StripANSI removes colour codes.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// noFlag checks a value passed to ansible. Values go as --flag=value and the
// pattern after "--", so none can become an option of its own.
func noFlag(name, v string) error {
	if strings.ContainsAny(v, "\n\r\x00") {
		return fmt.Errorf("%s cannot contain line breaks", name)
	}
	return nil
}

// Validate checks options against the project and fills defaults.
func (o *RunOptions) Validate(p *Project) error {
	if o.Kind == "" {
		o.Kind = KindPlaybook
	}
	for name, v := range map[string]string{"limit": o.Limit, "tags": o.Tags, "skip tags": o.SkipTags, "start at task": o.StartAt, "pattern": o.Pattern, "args": o.Args} {
		if err := noFlag(name, v); err != nil {
			return err
		}
	}
	if o.Verbosity < 0 || o.Verbosity > 4 {
		return errors.New("verbosity is 0 to 4")
	}
	if o.Forks < 0 || o.Forks > 200 {
		return errors.New("forks is 1 to 200")
	}
	if o.Inventory != "" && o.Inventory != WorkspaceInventory {
		if _, err := p.Path(o.Inventory); err != nil {
			return fmt.Errorf("inventory: %w", err)
		}
	}
	if strings.TrimSpace(o.ExtraVars) != "" {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(o.ExtraVars), &m); err != nil || m == nil {
			return errors.New("extra variables must be a YAML or JSON mapping (name: value)")
		}
	}
	switch o.Kind {
	case KindPlaybook:
		if o.Playbook == "" {
			return errors.New("choose a playbook")
		}
		abs, err := p.Path(o.Playbook)
		if err != nil {
			return err
		}
		if _, err := os.Stat(abs); err != nil {
			return fmt.Errorf("playbook %s not found", o.Playbook)
		}
	case KindAdhoc:
		if strings.TrimSpace(o.Pattern) == "" {
			o.Pattern = "all"
		}
		if o.Module == "" {
			o.Module = "ansible.builtin.ping"
		}
		if !moduleRe.MatchString(o.Module) {
			return fmt.Errorf("invalid module name %q", o.Module)
		}
	case KindGalaxy:
		if o.Requirements == "" {
			o.Requirements = "requirements.yml"
		}
		abs, err := p.Path(o.Requirements)
		if err != nil {
			return err
		}
		if _, err := os.Stat(abs); err != nil {
			return fmt.Errorf("%s not found", o.Requirements)
		}
	case KindLint:
		if o.Playbook != "" {
			if _, err := p.Path(o.Playbook); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown run kind %q (playbook, adhoc, galaxy, lint)", o.Kind)
	}
	return nil
}

// command returns the program and its arguments; secrets and generated
// files are in pe.
func (o *RunOptions) command(p *Project, pe *procEnv) (string, []string) {
	var args []string
	common := func() {
		if pe.inventory != "" {
			args = append(args, "--inventory="+pe.inventory)
		}
		if o.Limit != "" {
			args = append(args, "--limit="+o.Limit)
		}
		if pe.extraVars != "" {
			args = append(args, "--extra-vars=@"+pe.extraVars)
		}
		if o.Check {
			args = append(args, "--check")
		}
		if o.Diff {
			args = append(args, "--diff")
		}
		if o.Verbosity > 0 {
			args = append(args, "-"+strings.Repeat("v", o.Verbosity))
		}
		if o.Forks > 0 {
			args = append(args, "--forks="+strconv.Itoa(o.Forks))
		}
		if o.Become {
			args = append(args, "--become")
		}
		args = append(args, pe.vaultArgs...)
	}
	switch o.Kind {
	case KindAdhoc:
		args = append(args, "--module-name="+o.Module)
		if o.Args != "" {
			args = append(args, "--args="+o.Args)
		}
		common()
		return "ansible", append(args, "--", o.Pattern)
	case KindGalaxy:
		args = append(args, "install", "--role-file="+o.Requirements)
		if o.Force {
			args = append(args, "--force")
		}
		return "ansible-galaxy", args
	case KindLint:
		args = append(args, "--force-color")
		if o.Playbook != "" {
			args = append(args, "--", o.Playbook)
		}
		return "ansible-lint", args
	}
	if o.Tags != "" {
		args = append(args, "--tags="+o.Tags)
	}
	if o.SkipTags != "" {
		args = append(args, "--skip-tags="+o.SkipTags)
	}
	if o.StartAt != "" {
		args = append(args, "--start-at-task="+o.StartAt)
	}
	common()
	return "ansible-playbook", append(args, "--", o.Playbook)
}

// display is the command as shown to people: generated files and password
// files by name only.
func display(prog string, args []string, pe *procEnv) string {
	parts := []string{prog}
	for _, a := range args {
		for real, shown := range pe.shown {
			a = strings.ReplaceAll(a, real, shown)
		}
		if strings.ContainsAny(a, " \t'\"$`\\") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Start runs ansible for a project in the background. actor is "user" or
// "agent:<session>"; ctx carries the actor for credential policy and ends
// the run when it is canceled.
func (m *Manager) Start(ctx context.Context, p *Project, o RunOptions, actor string) (*Run, error) {
	if err := o.Validate(p); err != nil {
		return nil, err
	}
	if !p.Exists() {
		return nil, fmt.Errorf("the project folder %s does not exist", p.Root)
	}
	if o.Kind != KindGalaxy && o.Kind != KindLint {
		if err := m.needPasswords(ctx, p); err != nil {
			return nil, err
		}
	}
	rctx, cancel := context.WithCancel(ctx)
	pe, err := m.prepare(rctx, p, o, o.Kind != KindGalaxy && o.Kind != KindLint)
	if err != nil {
		cancel()
		return nil, err
	}
	progName, args := o.command(p, pe)
	prog, err := lookPath(p, progName)
	if err != nil {
		pe.close()
		cancel()
		return nil, err
	}
	r := &Run{info: RunInfo{ID: newID(), Workspace: p.W.Name(), Project: p.R.Name, Options: o, Actor: actor, Started: time.Now().UTC(),
		Status: StatusRunning, Hosts: pe.hosts, Command: display(progName, args, pe)},
		changed: make(chan struct{}), cancel: cancel, done: make(chan struct{})}
	if m.Redact != nil && o.ExtraVars != "" {
		r.info.Options.ExtraVars = m.Redact(o.ExtraVars)
	}
	dir := runsDir(p.W)
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
		pe.close()
		cancel()
		return nil, err
	}
	r.logPath = filepath.Join(dir, "logs", r.info.ID+".log")
	logf, err := os.OpenFile(r.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		pe.close()
		cancel()
		return nil, err
	}

	cmd := sysproc.Command(prog, args...)
	cmd.Dir = p.Root
	cmd.Env = pe.env
	setProcessGroup(cmd)
	pr, pw, err := os.Pipe()
	if err != nil {
		logf.Close()
		pe.close()
		cancel()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		logf.Close()
		pe.close()
		cancel()
		return nil, err
	}
	pw.Close()
	m.mu.Lock()
	m.runs[r.info.ID] = r
	m.mu.Unlock()
	appendRecord(dir, r.Info())

	for _, l := range pe.notes {
		r.add(l)
		logf.WriteString(l + "\n")
	}
	exited := make(chan struct{})
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				if m.Touch != nil {
					m.Touch()
				}
				continue
			case <-rctx.Done():
				interrupt(cmd)
				select {
				case <-exited:
				case <-time.After(5 * time.Second):
					kill(cmd)
				}
			case <-exited:
			}
			return
		}
	}()
	go func() {
		defer close(r.done)
		var recap []HostRecap
		inRecap := false
		read := make(chan struct{})
		go func() {
			defer close(read)
			br := bufio.NewReaderSize(pr, 64*1024)
			for {
				line, err := br.ReadString('\n')
				if line != "" {
					line = strings.TrimRight(line, "\r\n")
					if m.Redact != nil {
						line = m.Redact(line)
					}
					logf.WriteString(line + "\n")
					plain := StripANSI(line)
					if strings.HasPrefix(plain, "PLAY RECAP") {
						inRecap = true
					} else if inRecap {
						if mm := recapRe.FindStringSubmatch(strings.TrimSpace(plain)); mm != nil {
							recap = append(recap, parseRecap(mm))
						}
					}
					r.add(line)
				}
				if err != nil {
					return
				}
			}
		}()
		werr := cmd.Wait()
		close(exited)
		// ssh ControlPersist masters may keep the pipe open: give the reader
		// a moment to drain, then stop.
		pr.SetReadDeadline(time.Now().Add(3 * time.Second))
		<-read
		pr.Close()
		logf.Close()
		pe.close()

		now := time.Now().UTC()
		r.mu.Lock()
		in := &r.info
		in.Ended = &now
		in.DurationMs = now.Sub(in.Started).Milliseconds()
		in.Recap = recap
		in.ExitCode = cmd.ProcessState.ExitCode()
		switch {
		case rctx.Err() != nil:
			in.Status = StatusCanceled
		case werr == nil:
			in.Status = StatusOK
		case in.ExitCode == 2:
			in.Status = StatusFailed // a task failed; for ansible-lint: violations
		case in.ExitCode == 4:
			in.Status = StatusUnreachable
		default:
			in.Status = StatusError
			if in.ExitCode < 0 {
				in.Error = werr.Error()
			}
		}
		close(r.changed)
		r.changed = make(chan struct{})
		r.mu.Unlock()
		cancel()
		appendRecord(dir, r.Info())
		// keep finished runs in memory for a while (followers, the agent)
		time.AfterFunc(30*time.Minute, func() {
			m.mu.Lock()
			if m.runs[r.info.ID] == r {
				delete(m.runs, r.info.ID)
			}
			m.mu.Unlock()
		})
	}()
	return r, nil
}

func parseRecap(mm []string) HostRecap {
	n := func(i int) int {
		if i >= len(mm) {
			return 0
		}
		v, _ := strconv.Atoi(mm[i])
		return v
	}
	return HostRecap{Host: mm[1], OK: n(2), Changed: n(3), Unreachable: n(4), Failed: n(5), Skipped: n(6), Rescued: n(7), Ignored: n(8)}
}

// needPasswords fails when the project holds vault content and no password
// at all is at hand (ansible would stop at the first encrypted value).
func (m *Manager) needPasswords(ctx context.Context, p *Project) error {
	pw := m.Passwords(ctx, p)
	if len(pw) > 0 {
		return nil
	}
	files, _, err := p.Files()
	if err != nil {
		return nil
	}
	for _, f := range files {
		if f.Encrypted {
			return &NeedPassword{Project: p.R.Name, VaultID: f.VaultID}
		}
	}
	for _, f := range files {
		if f.Inline > 0 {
			if fc, err := p.Read(f.Path); err == nil && len(fc.Inline) > 0 {
				return &NeedPassword{Project: p.R.Name, VaultID: fc.Inline[0].VaultID}
			}
		}
	}
	return nil
}

// Wait waits for a run to end (or ctx); on ctx end the run is canceled.
func (r *Run) Wait(ctx context.Context) {
	select {
	case <-r.done:
	case <-ctx.Done():
		r.Cancel()
		<-r.done
	}
}

// ---------------------------------------------------------------------------
// History

func appendRecord(dir string, r RunInfo) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "runs.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// Get returns a run: live from memory, else from the history.
func (m *Manager) Get(w *workspace.Workspace, id string) (*Run, error) {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r != nil && r.info.Workspace == w.Name() {
		return r, nil
	}
	for _, x := range m.history(w) {
		if x.ID == id {
			r := &Run{info: x, changed: make(chan struct{}), done: make(chan struct{}), logPath: filepath.Join(runsDir(w), "logs", id+".log")}
			close(r.done)
			if r.info.Ended == nil {
				t := r.info.Started
				r.info.Ended = &t
			}
			r.loadLog()
			return r, nil
		}
	}
	return nil, fmt.Errorf("run %s not found", id)
}

func (r *Run) loadLog() {
	f, err := os.Open(r.logPath)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	n := 0
	for sc.Scan() {
		if r.size < maxLogMemory {
			r.lines = append(r.lines, LogLine{N: n, Text: sc.Text()})
			r.size += len(sc.Text())
		}
		n++
	}
	r.info.LogLines = n
}

// LogPath is the file of a run's full log.
func (m *Manager) LogPath(w *workspace.Workspace, id string) (string, error) {
	if strings.ContainsAny(id, "/\\.") {
		return "", errors.New("invalid run id")
	}
	p := filepath.Join(runsDir(w), "logs", id+".log")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("run %s not found", id)
	}
	return p, nil
}

// history reads the run records (the last record of a run wins).
func (m *Manager) history(w *workspace.Workspace) []RunInfo {
	f, err := os.Open(filepath.Join(runsDir(w), "runs.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	byID := map[string]RunInfo{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		var r RunInfo
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID == "" {
			continue
		}
		if _, ok := byID[r.ID]; !ok {
			order = append(order, r.ID)
		}
		byID[r.ID] = r
	}
	out := make([]RunInfo, 0, len(order))
	m.mu.Lock()
	for _, id := range order {
		r := byID[id]
		if live := m.runs[id]; live != nil {
			r = live.Info()
		} else if r.Status == StatusRunning {
			r.Status = "interrupted" // opsdeck stopped during the run
		}
		out = append(out, r)
	}
	m.mu.Unlock()
	return out
}

// Runs lists runs of a workspace (of one project when given), newest first.
func (m *Manager) Runs(w *workspace.Workspace, project string, limit int) []RunInfo {
	all := m.history(w)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Started.After(all[j].Started) })
	out := []RunInfo{}
	for _, r := range all {
		if project != "" && r.Project != project {
			continue
		}
		out = append(out, r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Summary is a short text of a finished run for the agent: status, recap,
// failures and the end of the log.
func (r *Run) Summary(tail int) string {
	s := r.Info()
	var b strings.Builder
	fmt.Fprintf(&b, "run %s: %s (exit code %d, %.1fs)\n$ %s\n", s.ID, s.Status, s.ExitCode, float64(s.DurationMs)/1000, s.Command)
	if s.Error != "" {
		b.WriteString("error: " + s.Error + "\n")
	}
	if len(s.Recap) > 0 {
		b.WriteString("\nPLAY RECAP\n")
		for _, h := range s.Recap {
			fmt.Fprintf(&b, "  %s: ok=%d changed=%d unreachable=%d failed=%d skipped=%d rescued=%d ignored=%d\n",
				h.Host, h.OK, h.Changed, h.Unreachable, h.Failed, h.Skipped, h.Rescued, h.Ignored)
		}
	}
	lines := r.Tail(0)
	var problems []string
	task := ""
	for _, l := range lines {
		pl := StripANSI(l)
		t := strings.TrimSpace(pl)
		switch {
		case strings.HasPrefix(t, "TASK ["):
			task = strings.TrimRight(t, " *")
		case strings.HasPrefix(t, "fatal:"), strings.HasPrefix(t, "failed:"), strings.HasPrefix(t, "ERROR!"), strings.HasPrefix(t, "[ERROR]"), strings.Contains(t, "UNREACHABLE!"):
			if task != "" && (len(problems) == 0 || problems[len(problems)-1] != task) {
				problems = append(problems, task)
			}
			if len(t) > 2000 {
				t = t[:2000] + "…"
			}
			problems = append(problems, "  "+t)
		}
	}
	if len(problems) > 0 {
		if len(problems) > 60 {
			problems = append(problems[:60], fmt.Sprintf("  … %d more lines", len(problems)-60))
		}
		b.WriteString("\nFailures:\n" + strings.Join(problems, "\n") + "\n")
	}
	if tail > 0 {
		start := 0
		if len(lines) > tail {
			start = len(lines) - tail
			fmt.Fprintf(&b, "\nLast %d of %d log lines:\n", tail, len(lines))
		} else {
			b.WriteString("\nLog:\n")
		}
		for _, l := range lines[start:] {
			b.WriteString(StripANSI(l) + "\n")
		}
	}
	return b.String()
}
