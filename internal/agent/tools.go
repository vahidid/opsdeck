package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"

	"opsdeck/internal/ansible"
	"opsdeck/internal/connect"
	"opsdeck/internal/docker"
	"opsdeck/internal/gitx"
	"opsdeck/internal/kube"
	"opsdeck/internal/llm"
	"opsdeck/internal/mcpx"
	"opsdeck/internal/skills"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// Env is what tools operate on.
type Env struct {
	WS         *workspace.Workspace
	Hub        *connect.Hub
	Docker     *docker.Client
	Kube       *kube.Manager
	Git        *gitx.Manager
	Ansible    *ansible.Manager
	Vault      *vault.Vault
	MCP        *mcpx.Manager
	MemoryPath string
	// UserScripts is the user's own script library ("" = none).
	UserScripts string
	SessionID   string
	Model       string
	Local       *LocalPolicy // nil = no access to this computer outside the workspace
	Policy      Policy
}

// Call is a prepared tool invocation: everything the permission layer needs
// to decide, plus the function that performs it.
type Call struct {
	Risk        string
	Target      string
	Environment string
	Protected   bool
	Match       string // text matched by permission rules (command, action...)
	// Shell marks Match as a shell script; permission rules with a match
	// pattern then apply to each of its simple commands (Commands, nil when
	// the script could not be parsed).
	Shell       bool
	Commands    []CommandPart
	Preview     string // shown in approval cards (command, diff...)
	Reasons     []string
	Credentials []string
	// Consent makes even a read ask (e.g. a local file outside the allowed
	// folders); the text is the reason shown to the user.
	Consent string
	// Sensitive marks a read of credentials or secrets: it asks in Auto mode
	// too (only Full skips it).
	Sensitive bool
	// Guarded marks a change on this computer that asks in Auto mode too
	// (the vault, the topology map).
	Guarded bool
	// Scope is where the call has its effect (ScopeLocal, ScopeRemote); ""
	// = from the environment and target.
	Scope string
	// Grant, when set, is what an "always allow" answer covers instead of the
	// exact tool/target/match; GrantLabel describes it on the approval card.
	Grant        string
	GrantLabel   string
	parentGrants []string // grants that also cover this call (a parent folder)
	// Secret, when set, makes the approval card ask the user for a password
	// the call needs (an Ansible Vault password). It always asks, also in
	// Auto mode and for granted calls.
	Secret *SecretPrompt
	Run    func(ctx context.Context) (string, error)
}

// SecretPrompt is a password the user types into the approval card. It is
// checked before the card closes and handed to Use only: it never enters
// the transcript, the audit journal or the model's context.
type SecretPrompt struct {
	Label         string // e.g. "Ansible Vault password of infra (vault id prod)"
	Reason        string // why the card asks, shown as its reason
	RememberLabel string // what "always" also does with the password
	Check         func(secret string) error
	// Use receives the password before Run; remember is true when the user
	// chose to keep it for the session ("always").
	Use func(secret string, remember bool)
}

// Tool is an agent tool.
type Tool struct {
	Def     llm.Tool
	Prepare func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error)
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func enum(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": vals}
}

func decode(input json.RawMessage, v any) error {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	if err := json.Unmarshal(input, v); err != nil {
		return fmt.Errorf("invalid tool input: %v", err)
	}
	return nil
}

// truncate keeps the head and tail of long output.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head := max * 2 / 3
	tail := max - head
	// cut on character boundaries (Persian and other multi-byte text)
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	for tail > 0 && !utf8.RuneStart(s[len(s)-tail]) {
		tail--
	}
	return s[:head] + fmt.Sprintf("\n\n... [%d bytes omitted] ...\n\n", len(s)-head-tail) + s[len(s)-tail:]
}

func targetInfo(w *workspace.Workspace, rs ...*workspace.Resource) (env string, protected bool) {
	for _, r := range rs {
		e := w.EnvironmentOf(r)
		if e.Protected {
			return e.Name, true
		}
		if env == "" {
			env = e.Name
		}
	}
	return env, false
}

func (e *Env) host(name string) (*workspace.Resource, error) {
	name = strings.TrimPrefix(strings.TrimPrefix(name, "host/"), "Host/")
	if r := e.WS.Find("Host", name); r != nil {
		return r, nil
	}
	var names []string
	for _, r := range e.WS.Resources {
		if r.Kind == "Host" {
			names = append(names, r.Name)
		}
	}
	return nil, fmt.Errorf("host %q not found; hosts in this workspace: %s", name, strings.Join(names, ", "))
}

// hosts expands a target (host, group/x, or comma-separated list) into hosts.
func (e *Env) hosts(target string) ([]*workspace.Resource, error) {
	var out []*workspace.Resource
	for _, t := range strings.Split(target, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		r := e.WS.FindTarget(t)
		if r == nil {
			if h, err := e.host(t); err == nil {
				r = h
			} else {
				return nil, fmt.Errorf("target %q not found (use a host name, host/<name> or group/<name>)", t)
			}
		}
		switch r.Kind {
		case "Host":
			out = append(out, r)
		case "HostGroup":
			members := e.WS.GroupMembers(r)
			if len(members) == 0 {
				return nil, fmt.Errorf("group %s has no hosts", r.Name)
			}
			out = append(out, members...)
		default:
			return nil, fmt.Errorf("%s is a %s, not a host", t, r.Kind)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no target given")
	}
	return out, nil
}

func (e *Env) creds(rs ...*workspace.Resource) []string {
	var out []string
	for _, r := range rs {
		out = append(out, e.Hub.Credentials(e.WS, r)...)
	}
	return out
}

func formatExec(res connect.ExecResult, err error) string {
	if err != nil && res.DurationMs == 0 {
		return "error: " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "exit code: %d (%.1fs)\n", res.ExitCode, float64(res.DurationMs)/1000)
	if err != nil {
		fmt.Fprintf(&b, "error: %v\n", err)
	}
	if res.Stdout != "" {
		b.WriteString("stdout:\n" + truncate(res.Stdout, 24000) + "\n")
	}
	if res.Stderr != "" {
		b.WriteString("stderr:\n" + truncate(res.Stderr, 8000) + "\n")
	}
	if res.Truncated {
		b.WriteString("(output was truncated at the source)\n")
	}
	if res.Stdout == "" && res.Stderr == "" {
		b.WriteString("(no output)\n")
	}
	return b.String()
}

func unifiedDiff(path, before, after string) string {
	d := difflib.UnifiedDiff{
		A: difflib.SplitLines(before), B: difflib.SplitLines(after),
		FromFile: path + " (current)", ToFile: path + " (new)", Context: 3,
	}
	s, err := difflib.GetUnifiedDiffString(d)
	if err != nil {
		return ""
	}
	return truncate(s, 20000)
}

// Tools returns the built-in tool set.
func Tools() []Tool {
	return []Tool{
		toolInventory(), toolReadDoc(), toolKnowledge(), toolLoadSkill(), toolMemory(), toolWriteDoc(), toolWriteReport(), toolSaveArtifact(),
		toolExec(), toolReadFile(), toolListDir(), toolWriteFile(), toolEditFile(),
		toolNetCheck(), toolDocker(), toolKubernetes(), toolGitSync(),
		toolRepo(), toolRepoFiles(), toolRepoWrite(), toolMergeRequest(), toolTopology(),
		toolWorkspaceResource(), toolVaultList(), toolVaultAddItem(), toolVPN(),
		toolScript(), toolRunScript(),
		toolAnsible(), toolAnsibleRun(), toolAnsibleVault(),
	}
}

// ---------------------------------------------------------------------------

func toolInventory() Tool {
	return Tool{
		Def: llm.Tool{Name: "inventory", Description: "List the workspace's resources (hosts, groups, networks, panels, clusters, databases, git remotes) with addresses, environments, roles and how each is reached. Never contains secrets. Filter by kind, environment or a text query.",
			Schema: obj(map[string]any{
				"kind":        str("Optional kind filter: Host, HostGroup, WireGuard, Tailscale, NetBird, OpenVPN, OpenConnect, OpenFortiVPN, SocksProxy, Panel, GitRemote, Database, KubernetesCluster"),
				"environment": str("Optional environment filter"),
				"query":       str("Optional case-insensitive text to match in name, address, tags, roles or description"),
			})},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Kind, Environment, Query string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			return &Call{Risk: RiskRead, Target: "workspace/" + env.WS.Name(), Run: func(ctx context.Context) (string, error) {
				return renderInventory(env, in.Kind, in.Environment, in.Query), nil
			}}, nil
		},
	}
}

func renderInventory(env *Env, kind, envName, query string) string {
	var b strings.Builder
	q := strings.ToLower(query)
	netStatus := map[string]string{}
	for _, n := range env.Hub.Networks(env.WS) {
		st := n.Status
		if n.Mode != "" {
			st += " mode=" + n.Mode
		}
		if n.Status == "up" && n.Address != "" {
			st += " vpn-address=" + n.Address
		}
		netStatus[n.Name] = st
	}
	count := 0
	for _, r := range env.WS.Resources {
		if kind != "" && !strings.EqualFold(r.Kind, kind) {
			continue
		}
		if envName != "" && r.Environment != envName {
			continue
		}
		line := fmt.Sprintf("- %s/%s", r.Kind, r.Name)
		var attrs []string
		if r.Environment != "" {
			e := env.WS.EnvironmentOf(r)
			if e.Protected {
				attrs = append(attrs, "env="+r.Environment+" (PROTECTED)")
			} else {
				attrs = append(attrs, "env="+r.Environment)
			}
		}
		keys := make([]string, 0, len(r.Spec))
		for k := range r.Spec {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := r.Spec[k]
			if s, ok := v.(string); ok && strings.HasPrefix(s, "vault://") {
				attrs = append(attrs, k+"=<credential "+s+">")
				continue
			}
			if k == "caCert" {
				attrs = append(attrs, k+"=<set>")
				continue
			}
			attrs = append(attrs, fmt.Sprintf("%s=%v", k, v))
		}
		if len(r.Tags) > 0 {
			attrs = append(attrs, "tags="+strings.Join(r.Tags, ","))
		}
		if r.Via != "" {
			path, _ := connect.DescribePath(env.WS, r)
			attrs = append(attrs, "reached via "+strings.Join(path, " → "))
		}
		if st, ok := netStatus[r.Name]; ok && workspace.IsNetworkKind(r.Kind) {
			attrs = append(attrs, "status="+st)
		}
		if r.Kind == "GitRemote" {
			if _, err := os.Stat(filepath.Join(env.WS.RepoDir(r.Name), ".git")); err == nil {
				attrs = append(attrs, "working-copy=repos/"+r.Name)
			} else {
				attrs = append(attrs, "working-copy=not cloned")
			}
		}
		if r.Kind == "HostGroup" {
			var names []string
			for _, m := range env.WS.GroupMembers(r) {
				names = append(names, m.Name)
			}
			attrs = append(attrs, "members="+strings.Join(names, ","))
		}
		if r.Description != "" {
			attrs = append(attrs, "description="+strconv.Quote(r.Description))
		}
		if r.Docs != "" {
			attrs = append(attrs, "docs="+r.Docs)
		}
		line += " " + strings.Join(attrs, " ")
		if q != "" && !strings.Contains(strings.ToLower(line), q) {
			continue
		}
		b.WriteString(line + "\n")
		count++
	}
	if count == 0 {
		return "No matching resources."
	}
	return b.String()
}

func toolReadDoc() Tool {
	return Tool{
		Def: llm.Tool{Name: "read_doc", Description: "Read workspace documentation. Without a path, lists available docs (OPSDECK.md, docs/**, skills/*). With a path, returns that file.",
			Schema: obj(map[string]any{"path": str("Doc path, e.g. docs/runbooks/deploy.md")})},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Path string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			return &Call{Risk: RiskRead, Target: "docs/" + in.Path, Run: func(ctx context.Context) (string, error) {
				if in.Path == "" {
					var b strings.Builder
					for _, d := range env.WS.Docs() {
						fmt.Fprintf(&b, "- %s — %s\n", d.Path, d.Title)
					}
					return b.String(), nil
				}
				s, err := env.WS.ReadDoc(in.Path)
				return truncate(s, 40000), err
			}}, nil
		},
	}
}

// AllSkills is the workspace's skills plus the built-in library (a
// workspace skill with the same name wins).
func AllSkills(w *workspace.Workspace) []skills.Skill {
	out := w.Skills()
	if w.Settings.NoBuiltinSkills {
		return out
	}
	own := map[string]bool{}
	for _, s := range out {
		own[s.Name] = true
	}
	for _, s := range skills.Library() {
		if !own[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

func toolLoadSkill() Tool {
	return Tool{
		Def: llm.Tool{Name: "load_skill", Description: "Load a skill (a packaged procedure listed in the system prompt: the workspace's own or opsdeck's built-in library) before following it. Skills may reference supporting files (references/…, assets/… templates, scripts/…): load them with file.",
			Schema: obj(map[string]any{"name": str("Skill name"), "file": str("Optional supporting file of the skill, e.g. references/details.md")}, "name")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Name, File string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			return &Call{Risk: RiskRead, Target: "skill/" + in.Name, Run: func(ctx context.Context) (string, error) {
				var names []string
				for _, s := range AllSkills(env.WS) {
					names = append(names, s.Name)
					if s.Name != in.Name {
						continue
					}
					var body string
					var err error
					switch {
					case s.Builtin:
						body, err = skills.Read(s.Name, in.File)
					case in.File != "" && in.File != "SKILL.md":
						body, err = env.WS.SkillFile(s, in.File)
					default:
						body, err = env.WS.ReadDoc(s.Path)
					}
					if err != nil {
						return "", err
					}
					if in.File == "" && len(s.Files) > 0 {
						body += "\n\n---\nSupporting files of this skill (load with load_skill name=" + s.Name + " file=<path>): " + strings.Join(s.Files, ", ")
					}
					if in.File == "" && s.Builtin {
						body += "\n\n(opsdeck note: built-in skill from " + s.Source + ", " + s.License + ". Adapt it to this workspace: run commands with exec on its hosts or local_exec on this computer, change Git repositories with repo/repo_write, and keep the workspace's approval and environment rules.)"
					}
					return truncate(body, 60000), nil
				}
				return "", fmt.Errorf("skill %q not found; available: %s", in.Name, strings.Join(names, ", "))
			}}, nil
		},
	}
}

var memoryMu sync.Mutex

func toolMemory() Tool {
	return Tool{
		Def: llm.Tool{Name: "memory", Description: "Workspace memory: durable notes you keep across sessions (facts learned about this project's infrastructure, conventions, gotchas). 'append' adds a note, 'replace' rewrites the whole memory, 'read' returns it. Never store secrets.",
			Schema: obj(map[string]any{"action": enum("Action", "read", "append", "replace"), "content": str("Text to append or the full replacement")}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Action, Content string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			risk := RiskRead
			if in.Action != "read" {
				// Memory is injected into every future prompt: writing it is a change.
				risk = RiskChange
			}
			return &Call{Risk: risk, Target: "memory", Match: in.Action, Preview: "memory " + in.Action + ":\n" + in.Content, Run: func(ctx context.Context) (string, error) {
				memoryMu.Lock()
				defer memoryMu.Unlock()
				cur, _ := os.ReadFile(env.MemoryPath)
				switch in.Action {
				case "read":
					if len(cur) == 0 {
						return "(memory is empty)", nil
					}
					return string(cur), nil
				case "append":
					note := strings.TrimSpace(in.Content)
					if note == "" {
						return "", errors.New("content is empty")
					}
					next := strings.TrimRight(string(cur), "\n")
					if next != "" {
						next += "\n"
					}
					next += "- " + strings.ReplaceAll(note, "\n", "\n  ") + "\n"
					return "saved", writeFile(env.MemoryPath, next)
				case "replace":
					return "saved", writeFile(env.MemoryPath, in.Content)
				}
				return "", fmt.Errorf("unknown action %q", in.Action)
			}}, nil
		},
	}
}

func writeFile(path, content string) error {
	os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func toolWriteDoc() Tool {
	return Tool{
		Def: llm.Tool{Name: "write_doc", Description: "Create or update a workspace documentation file (docs/**.md, skills/*.md or OPSDECK.md), e.g. to record a runbook or a topology note after finishing a task.",
			Schema: obj(map[string]any{"path": str("e.g. docs/runbooks/deploy-shop.md"), "content": str("Full Markdown content")}, "path", "content")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Path, Content string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			before, _ := env.WS.ReadDoc(in.Path)
			return &Call{Risk: RiskChange, Target: "docs/" + in.Path, Match: in.Path, Preview: unifiedDiff(in.Path, before, in.Content),
				Run: func(ctx context.Context) (string, error) {
					if err := env.WS.WriteDoc(in.Path, in.Content); err != nil {
						return "", err
					}
					return "written " + in.Path, nil
				}}, nil
		},
	}
}

func toolExec() Tool {
	return Tool{
		Def: llm.Tool{Name: "exec", Description: "Run a non-interactive shell command on one or more workspace hosts over SSH (VPN/jump hosts are handled automatically). target is a host name, group/<name>, or a comma-separated list. Commands run as the host's configured user; use 'sudo -n' when root is needed. Do not run interactive programs (vim, less, top without -b). Output is limited; filter with grep/head/tail.",
			Schema: obj(map[string]any{
				"target":      str("Host name, group/<name>, or comma-separated host names"),
				"command":     str("Shell command"),
				"timeout_sec": num("Timeout in seconds (default 120, max 1800)"),
			}, "target", "command")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Target     string
				Command    string
				TimeoutSec int `json:"timeout_sec"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if strings.TrimSpace(in.Command) == "" {
				return nil, errors.New("command is empty")
			}
			hosts, err := env.hosts(in.Target)
			if err != nil {
				return nil, err
			}
			timeout := time.Duration(in.TimeoutSec) * time.Second
			if timeout <= 0 {
				timeout = 120 * time.Second
			}
			if timeout > 30*time.Minute {
				timeout = 30 * time.Minute
			}
			an := AnalyzeCommand(in.Command)
			risk, reasons := an.Risk, an.Reasons
			envName, protected := targetInfo(env.WS, hosts...)
			var names []string
			for _, h := range hosts {
				names = append(names, h.Name)
			}
			target := "host/" + strings.Join(names, ",")
			preview := "$ " + in.Command + "\n\non: " + strings.Join(names, ", ")
			if len(hosts) == 1 {
				if path, _ := connect.DescribePath(env.WS, hosts[0]); len(path) > 0 {
					preview += "\nvia: " + strings.Join(path, " → ")
				}
			}
			return &Call{Risk: risk, Target: target, Environment: envName, Protected: protected, Match: in.Command,
				Shell: true, Commands: an.Parts, Preview: preview, Reasons: reasons, Credentials: env.creds(hosts...),
				Run: func(ctx context.Context) (string, error) {
					ctx, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()
					if len(hosts) == 1 {
						res, err := env.Hub.Exec(ctx, env.WS, hosts[0], in.Command, nil, 2<<20)
						return formatExec(res, err), nil
					}
					results := make([]string, len(hosts))
					var wg sync.WaitGroup
					sem := make(chan struct{}, 8)
					for i, h := range hosts {
						wg.Add(1)
						go func(i int, h *workspace.Resource) {
							defer wg.Done()
							sem <- struct{}{}
							defer func() { <-sem }()
							res, err := env.Hub.Exec(ctx, env.WS, h, in.Command, nil, 512<<10)
							results[i] = "=== " + h.Name + " ===\n" + truncate(formatExec(res, err), 60000/len(hosts)+2000)
						}(i, h)
					}
					wg.Wait()
					return strings.Join(results, "\n"), nil
				}}, nil
		},
	}
}

func toolReadFile() Tool {
	return Tool{
		Def: llm.Tool{Name: "read_file", Description: "Read a text file on a host (via SFTP). Large files are truncated; for logs prefer exec with tail/grep.",
			Schema: obj(map[string]any{"host": str("Host name"), "path": str("Absolute path"), "max_bytes": num("Max bytes (default 100000)")}, "host", "path")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Host, Path string
				MaxBytes   int64 `json:"max_bytes"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			h, err := env.host(in.Host)
			if err != nil {
				return nil, err
			}
			if in.MaxBytes <= 0 || in.MaxBytes > 1<<20 {
				in.MaxBytes = 100000
			}
			envName, protected := targetInfo(env.WS, h)
			return &Call{Risk: RiskRead, Target: "host/" + h.Name, Environment: envName, Protected: protected, Match: in.Path, Credentials: env.creds(h),
				Run: func(ctx context.Context) (string, error) {
					s, trunc, err := env.Hub.ReadFile(ctx, env.WS, h, in.Path, in.MaxBytes)
					if err != nil {
						return "", fmt.Errorf("%v (if permission denied, try exec with 'sudo -n cat')", err)
					}
					if trunc {
						s += fmt.Sprintf("\n... [truncated at %d bytes]", in.MaxBytes)
					}
					return s, nil
				}}, nil
		},
	}
}

func toolListDir() Tool {
	return Tool{
		Def: llm.Tool{Name: "list_dir", Description: "List a directory on a host.",
			Schema: obj(map[string]any{"host": str("Host name"), "path": str("Directory path (default: home)")}, "host")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Host, Path string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			h, err := env.host(in.Host)
			if err != nil {
				return nil, err
			}
			envName, protected := targetInfo(env.WS, h)
			return &Call{Risk: RiskRead, Target: "host/" + h.Name, Environment: envName, Protected: protected, Match: in.Path, Credentials: env.creds(h),
				Run: func(ctx context.Context) (string, error) {
					path, entries, err := env.Hub.ListDir(ctx, env.WS, h, in.Path)
					if err != nil {
						return "", err
					}
					var b strings.Builder
					fmt.Fprintf(&b, "%s:\n", path)
					for _, e := range entries {
						kind := "f"
						if e.Dir {
							kind = "d"
						}
						fmt.Fprintf(&b, "%s %s %10d %s %s\n", kind, e.Mode, e.Size, e.Modified.Format("2006-01-02 15:04"), e.Name)
					}
					return truncate(b.String(), 30000), nil
				}}, nil
		},
	}
}

// writeRemote backs up the old file (if any) next to it and writes the new content.
func writeRemote(ctx context.Context, env *Env, h *workspace.Resource, path, before, content string, existed bool) (string, error) {
	backup := ""
	if existed {
		backup = fmt.Sprintf("%s.opsdeck-bak-%s", path, time.Now().Format("20060102-150405"))
		res, err := env.Hub.Exec(ctx, env.WS, h, "cp -p -- "+shellQuote(path)+" "+shellQuote(backup), nil, 4096)
		if err != nil || res.ExitCode != 0 {
			backup = ""
		}
	}
	if err := env.Hub.WriteFile(ctx, env.WS, h, path, content); err != nil {
		return "", fmt.Errorf("%v (if permission denied, write via exec: printf '%%s' ... | sudo -n tee %s)", err, path)
	}
	msg := "written " + path
	if backup != "" {
		msg += " (backup: " + backup + ")"
	}
	return msg, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func toolWriteFile() Tool {
	return Tool{
		Def: llm.Tool{Name: "write_file", Description: "Write a whole text file on a host (atomic replace; the previous version is backed up next to it as <file>.opsdeck-bak-<time>). Prefer edit_file for small changes.",
			Schema: obj(map[string]any{"host": str("Host name"), "path": str("Absolute path"), "content": str("Full new content")}, "host", "path", "content")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Host, Path, Content string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			h, err := env.host(in.Host)
			if err != nil {
				return nil, err
			}
			before, _, rerr := env.Hub.ReadFile(ctx, env.WS, h, in.Path, 1<<20)
			existed := rerr == nil
			envName, protected := targetInfo(env.WS, h)
			return &Call{Risk: RiskChange, Target: "host/" + h.Name, Environment: envName, Protected: protected, Match: in.Path,
				Preview: "file " + in.Path + " on " + h.Name + "\n\n" + unifiedDiff(in.Path, before, in.Content), Credentials: env.creds(h),
				Run: func(ctx context.Context) (string, error) {
					return writeRemote(ctx, env, h, in.Path, before, in.Content, existed)
				}}, nil
		},
	}
}

func toolEditFile() Tool {
	return Tool{
		Def: llm.Tool{Name: "edit_file", Description: "Replace one exact occurrence of old_string with new_string in a text file on a host (backup kept). old_string must match exactly once; include surrounding lines to make it unique.",
			Schema: obj(map[string]any{"host": str("Host name"), "path": str("Absolute path"), "old_string": str("Exact text to replace"), "new_string": str("Replacement text")}, "host", "path", "old_string", "new_string")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Host, Path string
				Old        string `json:"old_string"`
				New        string `json:"new_string"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			h, err := env.host(in.Host)
			if err != nil {
				return nil, err
			}
			before, trunc, err := env.Hub.ReadFile(ctx, env.WS, h, in.Path, 2<<20)
			if err != nil {
				return nil, fmt.Errorf("read %s: %v", in.Path, err)
			}
			if trunc {
				return nil, errors.New("file too large to edit (>2MB)")
			}
			if n := strings.Count(before, in.Old); n != 1 {
				return nil, fmt.Errorf("old_string matches %d times (must match exactly once)", n)
			}
			after := strings.Replace(before, in.Old, in.New, 1)
			envName, protected := targetInfo(env.WS, h)
			return &Call{Risk: RiskChange, Target: "host/" + h.Name, Environment: envName, Protected: protected, Match: in.Path,
				Preview: "file " + in.Path + " on " + h.Name + "\n\n" + unifiedDiff(in.Path, before, after), Credentials: env.creds(h),
				Run: func(ctx context.Context) (string, error) {
					return writeRemote(ctx, env, h, in.Path, before, after, true)
				}}, nil
		},
	}
}

func toolNetCheck() Tool {
	return Tool{
		Def: llm.Tool{Name: "net_check", Description: "Network diagnostics. type=tcp: can <target host:port> be reached; type=http: GET a URL (status, latency, TLS expiry); type=dns: resolve a name; type=ping: ICMP ping (only from a host). 'from' is the vantage point: empty = this machine (through the target's VPN if target is a workspace resource), or a host name to test from that host.",
			Schema: obj(map[string]any{
				"type":   enum("Check type", "tcp", "http", "dns", "ping"),
				"target": str("host:port, URL, DNS name, or a workspace resource like panel/grafana or host/app-1"),
				"from":   str("Optional host name to run the check from"),
			}, "type", "target")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Type, Target, From string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			var from *workspace.Resource
			if in.From != "" {
				h, err := env.host(in.From)
				if err != nil {
					return nil, err
				}
				from = h
			}
			res := env.WS.FindTarget(in.Target)
			if res != nil && !strings.Contains(in.Target, "/") && res.Kind != "Host" {
				res = nil
			}
			var rs []*workspace.Resource
			if from != nil {
				rs = append(rs, from)
			}
			if res != nil {
				rs = append(rs, res)
			}
			envName, protected := targetInfo(env.WS, rs...)
			return &Call{Risk: RiskRead, Target: "net/" + in.Target, Environment: envName, Protected: protected, Match: in.Target, Credentials: env.creds(rs...),
				Run: func(ctx context.Context) (string, error) {
					ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					return netCheck(ctx, env, in.Type, in.Target, from, res)
				}}, nil
		},
	}
}

func netCheck(ctx context.Context, env *Env, typ, target string, from, res *workspace.Resource) (string, error) {
	// Where to dial from: a host (its SSH connection), the resource's path, or locally.
	dialVia := func() (connect.DialFunc, error) {
		if from != nil {
			c, err := env.Hub.SSH(ctx, env.WS, from)
			if err != nil {
				return nil, err
			}
			return c.DialContext, nil
		}
		if res != nil {
			return env.Hub.Dialer(ctx, env.WS, res)
		}
		var d net.Dialer
		return d.DialContext, nil
	}
	switch typ {
	case "tcp":
		addr := target
		if res != nil {
			switch res.Kind {
			case "Host":
				addr = net.JoinHostPort(res.Str("address"), strconv.Itoa(res.Int("port", 22)))
			case "Database":
				addr = res.Str("address")
			}
		}
		dial, err := dialVia()
		if err != nil {
			return "", err
		}
		start := time.Now()
		c, err := dial(ctx, "tcp", addr)
		if err != nil {
			return fmt.Sprintf("tcp %s: UNREACHABLE (%v)", addr, err), nil
		}
		c.Close()
		return fmt.Sprintf("tcp %s: open (%dms)", addr, time.Since(start).Milliseconds()), nil
	case "http":
		url := target
		if res != nil && res.Str("url") != "" {
			url = res.Str("url")
		}
		if !strings.Contains(url, "://") {
			url = "http://" + url
		}
		dial, err := dialVia()
		if err != nil {
			return "", err
		}
		client := &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { return dial(ctx, n, a) },
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Sprintf("GET %s: FAILED (%v)", url, err), nil
		}
		defer resp.Body.Close()
		var b strings.Builder
		fmt.Fprintf(&b, "GET %s → %s in %dms\n", url, resp.Status, time.Since(start).Milliseconds())
		for _, h := range []string{"Server", "Location", "Content-Type", "X-Powered-By"} {
			if v := resp.Header.Get(h); v != "" {
				fmt.Fprintf(&b, "%s: %s\n", h, v)
			}
		}
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			cert := resp.TLS.PeerCertificates[0]
			days := int(time.Until(cert.NotAfter).Hours() / 24)
			fmt.Fprintf(&b, "TLS: subject=%s issuer=%s expires=%s (%d days)\n", cert.Subject.CommonName, cert.Issuer.CommonName, cert.NotAfter.Format("2006-01-02"), days)
		}
		return b.String(), nil
	case "dns":
		if from != nil {
			r, err := env.Hub.Exec(ctx, env.WS, from, "getent ahosts "+shellQuote(target)+" || dig +short "+shellQuote(target), nil, 16384)
			return formatExec(r, err), nil
		}
		addrs, err := net.DefaultResolver.LookupHost(ctx, target)
		if err != nil {
			return fmt.Sprintf("dns %s: %v", target, err), nil
		}
		return fmt.Sprintf("dns %s (local resolver): %s", target, strings.Join(addrs, ", ")), nil
	case "ping":
		if from == nil {
			return "", errors.New("ping needs 'from' (a host to ping from); use type=tcp to test reachability from here")
		}
		r, err := env.Hub.Exec(ctx, env.WS, from, "ping -c 4 -W 2 "+shellQuote(target), nil, 16384)
		return formatExec(r, err), nil
	}
	return "", fmt.Errorf("unknown check type %q", typ)
}

// gitSyncScript clones or updates a repository. The auth header (if any) is
// read from stdin into git's environment, so it never appears in the command
// line, in .git/config, or in the model's context.
const gitSyncScript = `set -e
IFS= read -r OPSDECK_GIT_HEADER || true
if [ -n "$OPSDECK_GIT_HEADER" ]; then
  export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=http.extraHeader GIT_CONFIG_VALUE_0="$OPSDECK_GIT_HEADER"
fi
export GIT_TERMINAL_PROMPT=0
if [ -d %[2]s/.git ]; then
  cd %[2]s
  git fetch --prune origin
  if [ -n %[3]s ]; then git checkout -q %[3]s; fi
  git pull --ff-only
else
  mkdir -p "$(dirname %[2]s)"
  if [ -n %[3]s ]; then git clone --branch %[3]s %[1]s %[2]s; else git clone %[1]s %[2]s; fi
  cd %[2]s
fi
echo "---"
git log -1 --format='HEAD %%h %%s (%%an, %%cr)'
git status -sb | head -1`

func toolGitSync() Tool {
	return Tool{
		Def: llm.Tool{Name: "git_sync", Description: "Clone a workspace Git repository (a GitRemote resource) onto a host, or update it if already cloned (fetch + fast-forward pull). Private repos authenticate with the GitRemote's vault token, which you never see. Use this instead of running git clone with credentials yourself.",
			Schema: obj(map[string]any{
				"repo":   str("GitRemote resource name"),
				"host":   str("Host to clone onto"),
				"path":   str("Absolute destination directory, e.g. /opt/apps/shop"),
				"branch": str("Branch or tag (default: the GitRemote's branch, else the remote default)"),
			}, "repo", "host", "path")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Repo, Host, Path, Branch string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			repo := env.WS.Find("GitRemote", strings.TrimPrefix(in.Repo, "git/"))
			if repo == nil {
				return nil, fmt.Errorf("git repository %q not found in the workspace", in.Repo)
			}
			h, err := env.host(in.Host)
			if err != nil {
				return nil, err
			}
			if !strings.HasPrefix(in.Path, "/") || strings.Contains(in.Path, "..") {
				return nil, errors.New("path must be an absolute directory without '..'")
			}
			branch := in.Branch
			if branch == "" {
				branch = repo.Str("branch")
			}
			url := repo.Str("url")
			if strings.HasPrefix(url, "http") && !strings.HasSuffix(url, ".git") {
				url += ".git"
			}
			refs := env.creds(h)
			if r := repo.Str("token"); r != "" {
				refs = append(refs, r)
			}
			envName, protected := targetInfo(env.WS, h)
			script := fmt.Sprintf(gitSyncScript, shellQuote(url), shellQuote(in.Path), shellQuote(branch))
			return &Call{Risk: RiskChange, Target: "host/" + h.Name, Environment: envName, Protected: protected,
				Match: "git_sync " + repo.Name + " " + in.Path, Credentials: refs,
				Preview: fmt.Sprintf("git clone/pull %s (%s)\ninto %s:%s", repo.Name, firstNonEmpty(branch, "default branch"), h.Name, in.Path),
				Run: func(ctx context.Context) (string, error) {
					header := ""
					if ref := repo.Str("token"); ref != "" {
						it, field, err := env.Vault.ResolveFor(env.WS.Name(), ref)
						if err != nil {
							return "", err
						}
						if !it.Policy.AgentUse {
							return "", fmt.Errorf("credential %s is not allowed for agent use", ref)
						}
						header = gitx.AuthHeader(it, field)
					}
					ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
					defer cancel()
					res, err := env.Hub.Exec(ctx, env.WS, h, script, strings.NewReader(header+"\n"), 1<<20)
					return formatExec(res, err), nil
				}}, nil
		},
	}
}

func toolWriteReport() Tool {
	return Tool{
		Def: llm.Tool{Name: "write_report", Description: "Save the result of an investigation, analysis, audit, incident or plan as a new Markdown report in the workspace (reports/<year>/<month>/<date>-<title>.md). Use it whenever the work produced findings worth keeping; include evidence (commands and key output), conclusions and next steps. Reports are never overwritten.",
			Schema: obj(map[string]any{
				"title":   str("Short descriptive title, e.g. \"nginx 502 on app-1 — root cause\""),
				"content": str("Full Markdown body"),
				"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional tags, e.g. incident, audit, plan"},
			}, "title", "content")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Title, Content string
				Tags           []string
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			// A new local file that is never fed back into prompts: allowed in
			// every mode, including plan mode (to save the plan itself).
			return &Call{Risk: RiskRead, Target: "reports", Match: in.Title, Run: func(ctx context.Context) (string, error) {
				path, err := env.WS.WriteReport(workspace.ReportMeta{Title: in.Title, Session: env.SessionID, Model: env.Model, Tags: in.Tags}, in.Content, time.Now())
				if err != nil {
					return "", err
				}
				return "saved " + path, nil
			}}, nil
		},
	}
}

func toolSaveArtifact() Tool {
	return Tool{
		Def: llm.Tool{Name: "save_artifact", Description: "Keep a file as evidence in the workspace (artifacts/<date>/...): source=file copies a text file from a host (e.g. a config snapshot before changing it); source=command saves the FULL output of a command run on a host (it is not sent back to you, only a short preview); source=text saves content you produced (a generated config, a diff, a CSV). Secrets are redacted before saving. Text only, up to 5 MB. Reference saved artifacts from your reports.",
			Schema: obj(map[string]any{
				"source":  enum("What to save", "file", "command", "text"),
				"host":    str("Host name (for file and command)"),
				"path":    str("Absolute remote path (for file)"),
				"command": str("Shell command whose output to save (for command)"),
				"content": str("Content to save (for text)"),
				"name":    str("File name for command/text artifacts, e.g. nginx-T.txt or upstreams.csv"),
				"note":    str("Short note for the artifacts index: why this was saved"),
			}, "source")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct{ Source, Host, Path, Command, Content, Name, Note string }
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			redact := NewRedactor(env.Vault.SecretValues).Redact
			now := time.Now()
			save := func(rel, content, source string) (string, error) {
				content = redact(content)
				path, err := env.WS.SaveArtifact(rel, []byte(content), workspace.ArtifactMeta{Source: source, Session: env.SessionID, Note: in.Note}, now)
				if err != nil {
					return "", err
				}
				preview := content
				if len(preview) > 1500 {
					preview = preview[:1500] + "\n…"
				}
				return fmt.Sprintf("saved %s (%d bytes)\npreview:\n%s", path, len(content), preview), nil
			}
			name := in.Name
			switch in.Source {
			case "text":
				if name == "" {
					return nil, errors.New("name is required for text artifacts")
				}
				return &Call{Risk: RiskRead, Target: "artifacts", Match: name, Run: func(ctx context.Context) (string, error) {
					return save(workspace.ArtifactPath(now, "", name), in.Content, "text")
				}}, nil
			case "file", "command":
				h, err := env.host(in.Host)
				if err != nil {
					return nil, err
				}
				envName, protected := targetInfo(env.WS, h)
				if in.Source == "file" {
					if !strings.HasPrefix(in.Path, "/") {
						return nil, errors.New("path must be absolute")
					}
					return &Call{Risk: RiskRead, Target: "host/" + h.Name, Environment: envName, Protected: protected, Match: in.Path, Credentials: env.creds(h),
						Run: func(ctx context.Context) (string, error) {
							content, trunc, err := env.Hub.ReadFile(ctx, env.WS, h, in.Path, workspace.MaxArtifactSize)
							if err != nil {
								return "", fmt.Errorf("%v (if permission denied, use source=command with 'sudo -n cat %s')", err, in.Path)
							}
							if trunc {
								return "", fmt.Errorf("%s is larger than 5 MB", in.Path)
							}
							return save(workspace.ArtifactPath(now, h.Name, in.Path), content, "file "+h.Name+":"+in.Path)
						}}, nil
				}
				if strings.TrimSpace(in.Command) == "" {
					return nil, errors.New("command is required")
				}
				if name == "" {
					name = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(in.Command), "-"), "-")
					if len(name) > 50 {
						name = name[:50]
					}
					name += ".txt"
				}
				an := AnalyzeCommand(in.Command)
				return &Call{Risk: an.Risk, Reasons: an.Reasons, Target: "host/" + h.Name, Environment: envName, Protected: protected, Match: in.Command,
					Shell: true, Commands: an.Parts,
					Preview: "$ " + in.Command + "\n\non: " + h.Name + " → artifacts/…/" + name, Credentials: env.creds(h),
					Run: func(ctx context.Context) (string, error) {
						ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
						defer cancel()
						res, err := env.Hub.Exec(ctx, env.WS, h, in.Command, nil, workspace.MaxArtifactSize)
						if err != nil && res.DurationMs == 0 {
							return "", err
						}
						out := res.Stdout
						if res.Stderr != "" {
							out += "\n--- stderr ---\n" + res.Stderr
						}
						if res.Truncated {
							out += "\n--- output truncated at 5 MB ---\n"
						}
						msg, err := save(workspace.ArtifactPath(now, h.Name, name), out, fmt.Sprintf("command %s: %s (exit %d)", h.Name, in.Command, res.ExitCode))
						if err != nil {
							return "", err
						}
						return fmt.Sprintf("exit code: %d\n%s", res.ExitCode, msg), nil
					}}, nil
			}
			return nil, fmt.Errorf("unknown source %q (file, command or text)", in.Source)
		},
	}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)
