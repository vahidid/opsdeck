package scripts

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// Command is what runs on the host. It is the same for every script: the
// script, its parameters and secrets travel on stdin (never on the command
// line, where other users could see them in ps) into a private temporary
// folder that is removed afterwards.
const Command = `D=$(mktemp -d "${TMPDIR:-/tmp}/opsdeck-script.XXXXXX") || exit 1; trap 'rm -rf "$D"' EXIT HUP INT TERM; cat > "$D/run" && sh "$D/run" "$D"; rc=$?; rm -rf "$D"; exit $rc`

// Markers and exit codes reported by the payload.
const (
	markerApplied   = "OPSDECK_ALREADY_APPLIED"
	ExitUnsupported = 97
)

// normalize checks a parameter value against its type.
func normalize(p *Param, v string) (string, error) {
	switch p.Type {
	case "int":
		if _, err := strconv.Atoi(strings.TrimSpace(v)); err != nil {
			return "", fmt.Errorf("parameter %s must be an integer, got %q", p.Name, v)
		}
		return strings.TrimSpace(v), nil
	case "bool":
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return "true", nil
		case "false", "0", "no", "off", "":
			return "false", nil
		}
		return "", fmt.Errorf("parameter %s must be true or false, got %q", p.Name, v)
	}
	return v, nil
}

// Resolve checks the given parameter values against the script and fills in
// defaults. Secret parameters come back as vault references in secrets.
func Resolve(s *Script, given map[string]string) (values, secrets map[string]string, err error) {
	values, secrets = map[string]string{}, map[string]string{}
	known := map[string]bool{}
	for i := range s.Params {
		p := &s.Params[i]
		known[p.Name] = true
		v, ok := given[p.Name]
		if !ok || v == "" {
			v = p.Default
		}
		if v == "" {
			if p.Required {
				return nil, nil, fmt.Errorf("parameter %s is required", p.Name)
			}
			if p.Type != "bool" {
				continue
			}
		}
		if p.Type == "secret" {
			if !strings.HasPrefix(v, "vault://") {
				return nil, nil, fmt.Errorf("parameter %s is a secret: pass a vault:// reference, never the value", p.Name)
			}
			secrets[p.Name] = v
			continue
		}
		if v, err = normalize(p, v); err != nil {
			return nil, nil, err
		}
		values[p.Name] = v
	}
	var unknown []string
	for k := range given {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		var names []string
		for _, p := range s.Params {
			names = append(names, p.Name)
		}
		return nil, nil, fmt.Errorf("unknown parameter(s) %s; %s takes: %s", strings.Join(unknown, ", "), s.Name, firstNonEmpty(strings.Join(names, ", "), "no parameters"))
	}
	return values, secrets, nil
}

// Payload is the program sent on stdin to Command. env holds every variable
// the script gets (parameters, secret values, OPSDECK_*).
func Payload(s *Script, env map[string]string, force bool) (string, error) {
	interp, err := Interpreter(s.Body)
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(env))
	all := s.Body + s.Check
	for k, v := range env {
		keys = append(keys, k)
		all += v
	}
	delim := "OPSDECK_EOF_" + randHex()
	for strings.Contains(all, delim) {
		delim = "OPSDECK_EOF_" + randHex()
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("set -u\nD=\"$1\"\numask 077\n")
	fmt.Fprintf(&b, "cat > \"$D/env\" <<'%s'\n", delim)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, quote(env[k]))
	}
	fmt.Fprintf(&b, "%s\n", delim)
	fmt.Fprintf(&b, "cat > \"$D/script\" <<'%s'\n%s\n%s\n", delim, strings.TrimSuffix(s.Body, "\n"), delim)
	if len(s.OS) > 0 {
		var pats []string
		for _, o := range s.OS {
			pats = append(pats, o, o+"-*", o+".*")
		}
		fmt.Fprintf(&b, `os=$( if [ -r /etc/os-release ]; then . /etc/os-release; printf '%%s-%%s' "${ID:-unknown}" "${VERSION_ID:-}"; else uname -s | tr 'A-Z' 'a-z'; fi )
case "$os" in
  %s) ;;
  *) echo "opsdeck: %s supports %s; this host is $os" >&2; exit %d ;;
esac
`, strings.Join(pats, "|"), s.Name, strings.Join(s.OS, ", "), ExitUnsupported)
	}
	sudo := ""
	if s.Sudo {
		sudo = "sudo -n "
	}
	fmt.Fprintf(&b, "run() { %ssh -c 'set -a; . \"$1\"; set +a; shift; exec \"$@\"' opsdeck \"$D/env\" \"$@\"; }\n", sudo)
	if strings.TrimSpace(s.Check) != "" && !force {
		fmt.Fprintf(&b, "cat > \"$D/check\" <<'%s'\n%s\n%s\n", delim, strings.TrimSuffix(s.Check, "\n"), delim)
		fmt.Fprintf(&b, "if run sh \"$D/check\" >/dev/null 2>&1; then echo %s; exit 0; fi\n", markerApplied)
	}
	var args []string
	for _, a := range interp {
		args = append(args, quote(a))
	}
	fmt.Fprintf(&b, "run %s \"$D/script\"\n", strings.Join(args, " "))
	return b.String(), nil
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func randHex() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// Result is the outcome of a script on one host.
type Result struct {
	Host       string `json:"host"`
	ExitCode   int    `json:"exitCode"`
	Skipped    bool   `json:"skipped,omitempty"` // the check said it was already applied
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
	Log        string `json:"log,omitempty"` // artifact with the full output
}

// OK reports whether the script succeeded (or was already applied).
func (r *Result) OK() bool { return r.Error == "" && r.ExitCode == 0 }

// Runner runs scripts on a workspace's hosts.
type Runner struct {
	WS      *workspace.Workspace
	Hub     *connect.Hub
	Vault   *vault.Vault
	Redact  func(string) string
	Actor   string // user | agent
	Session string // agent session, if any
}

// SecretValue picks the value of a vault item for a script parameter: the
// referenced field, else the first secret field (a public key for ssh keys
// when the field is public_key).
func SecretValue(it vault.Item, field string) (string, error) {
	if field != "" {
		return it.Get(field), nil
	}
	for _, f := range it.Fields {
		if f.Secret && f.Value != "" {
			return f.Value, nil
		}
	}
	return "", fmt.Errorf("vault item %s has no secret value; name a field with #field", it.Ref())
}

// Run runs s on hosts (in parallel, at most 8 at a time), saves each host's
// output as an artifact and records the run.
func (r *Runner) Run(ctx context.Context, s *Script, hosts []*workspace.Resource, given map[string]string, force bool) ([]Result, error) {
	values, secrets, err := Resolve(s, given)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for k, v := range values {
		env[k] = v
	}
	for k, ref := range secrets {
		it, field, err := r.Vault.ResolveFor(r.WS.Name(), ref)
		if err != nil {
			return nil, fmt.Errorf("parameter %s: %v", k, err)
		}
		if r.Actor == "agent" && !it.Policy.AgentUse {
			return nil, fmt.Errorf("parameter %s: credential %s is not allowed for agent use", k, ref)
		}
		if env[k], err = SecretValue(it, field); err != nil {
			return nil, err
		}
	}
	env["OPSDECK_SCRIPT"] = s.Name
	env["OPSDECK_SCRIPT_VERSION"] = strconv.Itoa(s.Version)
	env["OPSDECK_WORKSPACE"] = r.WS.Name()
	timeout := time.Duration(s.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := make([]Result, len(hosts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h *workspace.Resource) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = r.runOne(ctx, s, h, env, values, force)
		}(i, h)
	}
	wg.Wait()
	return results, nil
}

func (r *Runner) runOne(ctx context.Context, s *Script, h *workspace.Resource, base map[string]string, values map[string]string, force bool) Result {
	res := Result{Host: h.Name}
	env := map[string]string{"OPSDECK_HOST": h.Name}
	for k, v := range base {
		env[k] = v
	}
	payload, err := Payload(s, env, force)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	start := time.Now()
	out, err := r.Hub.Exec(ctx, r.WS, h, Command, strings.NewReader(payload), 4<<20)
	res.ExitCode, res.DurationMs = out.ExitCode, out.DurationMs
	res.Stdout, res.Stderr = r.Redact(out.Stdout), r.Redact(out.Stderr)
	if err != nil {
		res.Error = r.Redact(err.Error())
		if out.DurationMs == 0 {
			res.DurationMs = time.Since(start).Milliseconds()
		}
	}
	if strings.HasPrefix(res.Stdout, markerApplied) {
		res.Skipped = true
		res.Stdout = strings.TrimPrefix(strings.TrimPrefix(res.Stdout, markerApplied), "\n")
	}
	if s.Sudo && res.ExitCode != 0 && strings.Contains(res.Stderr, "sudo: a password is required") {
		res.Stderr += "\n(opsdeck: this script needs sudo without a password (NOPASSWD) for the host's user)"
	}
	now := time.Now()
	if !res.Skipped {
		var log strings.Builder
		fmt.Fprintf(&log, "# %s on %s (exit %d, %.1fs)\n# hash %s, scope %s, by %s\n", s.Ref(), h.Name, res.ExitCode, float64(res.DurationMs)/1000, s.Hash, s.Scope, r.Actor)
		if res.Error != "" {
			fmt.Fprintf(&log, "# error: %s\n", res.Error)
		}
		log.WriteString("\n--- stdout ---\n" + res.Stdout)
		if res.Stderr != "" {
			log.WriteString("\n--- stderr ---\n" + res.Stderr)
		}
		if out.Truncated {
			log.WriteString("\n--- output truncated ---\n")
		}
		rel := workspace.ArtifactPath(now, h.Name, "script-"+s.Name+"-v"+strconv.Itoa(s.Version)+".log")
		if p, err := r.WS.SaveArtifact(rel, []byte(r.Redact(log.String())), workspace.ArtifactMeta{
			Source: fmt.Sprintf("script %s on %s (exit %d)", s.Ref(), h.Name, res.ExitCode), Session: r.Session}, now); err == nil {
			res.Log = p
		}
	}
	AppendRun(r.WS, RunRecord{Time: now.UTC().Format(time.RFC3339), Script: s.Name, Version: s.Version, Hash: s.Hash, Scope: s.Scope,
		Host: h.Name, Params: values, ExitCode: res.ExitCode, Skipped: res.Skipped, DurationMs: res.DurationMs, Error: res.Error,
		Actor: r.Actor, Session: r.Session, Log: res.Log})
	return res
}

// RunRecord is one line of the run history (.opsdeck/script-runs.jsonl).
type RunRecord struct {
	Time       string            `json:"time"`
	Script     string            `json:"script"`
	Version    int               `json:"version"`
	Hash       string            `json:"hash"`
	Scope      string            `json:"scope"`
	Host       string            `json:"host"`
	Params     map[string]string `json:"params,omitempty"` // without secrets
	ExitCode   int               `json:"exitCode"`
	Skipped    bool              `json:"skipped,omitempty"`
	DurationMs int64             `json:"durationMs"`
	Error      string            `json:"error,omitempty"`
	Actor      string            `json:"actor"`
	Session    string            `json:"session,omitempty"`
	Log        string            `json:"log,omitempty"`
}

var runsMu sync.Mutex

func runsPath(w *workspace.Workspace) string { return filepath.Join(w.StateDir(), "script-runs.jsonl") }

// AppendRun adds a record to the workspace's run history.
func AppendRun(w *workspace.Workspace, rec RunRecord) {
	runsMu.Lock()
	defer runsMu.Unlock()
	os.MkdirAll(w.StateDir(), 0o700)
	f, err := os.OpenFile(runsPath(w), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(rec)
	f.Write(append(b, '\n'))
}

// Runs returns the latest runs, newest first, optionally of one script
// and/or on one host.
func Runs(w *workspace.Workspace, script, host string, limit int) []RunRecord {
	runsMu.Lock()
	b, err := os.ReadFile(runsPath(w))
	runsMu.Unlock()
	out := []RunRecord{}
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r RunRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		if (script == "" || r.Script == script) && (host == "" || r.Host == host) {
			out = append(out, r)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
