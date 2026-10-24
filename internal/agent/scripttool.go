package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"opsdeck/internal/connect"
	"opsdeck/internal/llm"
	"opsdeck/internal/scripts"
)

func (e *Env) scriptStores() (user, ws scripts.Store) {
	return scripts.Store{Dir: e.UserScripts, Scope: scripts.ScopeUser}, scripts.WorkspaceStore(e.WS)
}

// AllScripts is the user's scripts plus the workspace's (a workspace script
// with the same name wins).
func AllScripts(e *Env) []scripts.Script {
	user, ws := e.scriptStores()
	var u []scripts.Script
	if e.UserScripts != "" {
		u = user.List()
	}
	return scripts.Merge(u, ws.List())
}

// findScript returns the script that applies in this workspace, with its body.
func (e *Env) findScript(name string) (scripts.Script, error) {
	user, ws := e.scriptStores()
	if s, err := ws.Get(name); err == nil || !errors.Is(err, scripts.ErrNotFound) {
		return s, err
	}
	if e.UserScripts != "" {
		if s, err := user.Get(name); err == nil || !errors.Is(err, scripts.ErrNotFound) {
			return s, err
		}
	}
	var names []string
	for _, s := range AllScripts(e) {
		names = append(names, s.Name)
	}
	return scripts.Script{}, fmt.Errorf("script %q not found; available: %s", name, firstNonEmpty(strings.Join(names, ", "), "none yet"))
}

func describeScript(s *scripts.Script, withBody bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s script, %s, hash %s)\n%s\n", s.Ref(), s.Scope, s.Status, s.Hash, s.Description)
	if len(s.OS) > 0 {
		b.WriteString("os: " + strings.Join(s.OS, ", ") + "\n")
	}
	if len(s.When) > 0 {
		b.WriteString("when: " + strings.Join(s.When, ", ") + "\n")
	}
	if s.Sudo {
		b.WriteString("runs as root (sudo -n)\n")
	}
	if s.Risk == "destructive" {
		b.WriteString("risk: destructive\n")
	}
	if s.Check != "" {
		b.WriteString("check (skips the run when it exits 0): " + s.Check + "\n")
	}
	if len(s.Params) > 0 {
		b.WriteString("params:\n")
		for _, p := range s.Params {
			fmt.Fprintf(&b, "  - %s (%s", p.Name, firstNonEmpty(p.Type, "string"))
			if p.Required {
				b.WriteString(", required")
			}
			if p.Default != "" {
				b.WriteString(", default " + p.Default)
			}
			b.WriteString(")")
			if p.Description != "" {
				b.WriteString(": " + p.Description)
			}
			b.WriteString("\n")
		}
	}
	if withBody {
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", s.File, s.Body)
	}
	return b.String()
}

func toolScript() Tool {
	return Tool{
		Def: llm.Tool{Name: "script", Description: "Saved scripts: reusable shell scripts the user approved (server bootstrap, hardening, agent installs…) that run_script runs on hosts as one unit. action=list shows them; read shows one (version for an earlier one); save creates or updates one. Save a procedure as a script when it is worth repeating on other hosts. Scripts you save are drafts until the user reviews and approves them on the Scripts page; a change to an approved script makes it a draft again. Never put secrets in a script: declare a parameter of type secret and pass a vault:// reference when running it. Parameters reach the script as environment variables of the same name; write idempotent scripts (safe to run twice) and give a check command when 'already applied' can be detected.",
			Schema: obj(map[string]any{
				"action":      enum("Action", "list", "read", "save"),
				"name":        str("Script name (lowercase, digits, . _ -), e.g. server-bootstrap"),
				"version":     num("read: an earlier version"),
				"scope":       enum("save: user = the user's own library, available in every workspace (default for new, general-purpose scripts); workspace = only this project, shared with its team. An existing script is updated where it is.", "user", "workspace"),
				"description": str("save: what it does and when to use it"),
				"body":        str("save: the full script; start with a #! line (default bash). Use set -euo pipefail for bash."),
				"params": map[string]any{"type": "array", "description": "save: parameters", "items": obj(map[string]any{
					"name": str("Environment variable name, e.g. DEPLOY_USER"), "description": str("What it is"),
					"type":    enum("Type", "string", "int", "bool", "secret"),
					"default": str("Default value (a vault:// reference for secrets)"), "required": map[string]any{"type": "boolean"},
				}, "name")},
				"os":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "save: supported systems as <ID>[-<VERSION_ID>] from /etc/os-release, e.g. ubuntu, ubuntu-24.04, debian-12, rhel-9; the run stops on other systems"},
				"when":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "save: when to suggest it, e.g. new-host, hardening, monitoring"},
				"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"sudo":        map[string]any{"type": "boolean", "description": "save: run as root with sudo -n"},
				"check":       str("save: optional shell command that exits 0 when the script was already applied (the run is then skipped)"),
				"risk":        enum("save: destructive if it deletes data or can break access", "change", "destructive"),
				"timeout_sec": num("save: timeout in seconds (default 1800, max 7200)"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Name, Scope, Description, Body, Check, Risk string
				Version                                             int
				Params                                              []scripts.Param
				OS, When, Tags                                      []string
				Sudo                                                bool
				TimeoutSec                                          int `json:"timeout_sec"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			switch in.Action {
			case "list", "":
				return &Call{Risk: RiskRead, Target: "scripts", Run: func(ctx context.Context) (string, error) {
					list := AllScripts(env)
					if len(list) == 0 {
						return "No scripts yet. Save one with action=save.", nil
					}
					var b strings.Builder
					for i := range list {
						b.WriteString(describeScript(&list[i], false) + "\n")
					}
					return b.String(), nil
				}}, nil
			case "read":
				return &Call{Risk: RiskRead, Target: "script/" + in.Name, Run: func(ctx context.Context) (string, error) {
					s, err := env.findScript(in.Name)
					if err != nil {
						return "", err
					}
					if in.Version > 0 && in.Version != s.Version {
						user, ws := env.scriptStores()
						st := ws
						if s.Scope == scripts.ScopeUser {
							st = user
						}
						if s, err = st.Version(in.Name, in.Version); err != nil {
							return "", err
						}
					}
					out := describeScript(&s, true)
					if len(s.Versions) > 0 {
						out += fmt.Sprintf("\nearlier versions: %v\n", s.Versions)
					}
					return truncate(out, 60000), nil
				}}, nil
			case "save":
			default:
				return nil, fmt.Errorf("unknown action %q (list, read, save)", in.Action)
			}
			user, ws := env.scriptStores()
			st := user
			old, err := env.findScript(in.Name)
			switch {
			case err == nil:
				if old.Scope == scripts.ScopeWorkspace {
					st = ws
				}
				if in.Scope != "" && in.Scope != old.Scope {
					return nil, fmt.Errorf("%s is a %s script; it is updated where it is (the user can copy it on the Scripts page)", in.Name, old.Scope)
				}
			case in.Scope == scripts.ScopeWorkspace:
				st = ws
			}
			if st.Scope == scripts.ScopeUser && env.UserScripts == "" {
				return nil, errors.New("the user's script library is not available; save with scope=workspace")
			}
			s := scripts.Script{Meta: scripts.Meta{Name: in.Name, Description: in.Description, OS: in.OS, When: in.When, Tags: in.Tags,
				Sudo: in.Sudo, Check: in.Check, Risk: in.Risk, TimeoutSec: in.TimeoutSec, Params: in.Params, File: old.File}, Body: in.Body}
			if err := scripts.Validate(&s); err != nil {
				return nil, err
			}
			preview := fmt.Sprintf("save %s script %s (as a draft for your review)\n\n", st.Scope, in.Name)
			if old.Name != "" {
				preview += unifiedDiff(old.File, describeScript(&old, true), describeScript(&s, true))
			} else {
				preview += describeScript(&s, true)
			}
			return &Call{Risk: RiskChange, Target: "script/" + in.Name, Match: "save " + in.Name, Preview: truncate(preview, 30000),
				Run: func(ctx context.Context) (string, error) {
					saved, err := st.Save(s, "agent")
					if err != nil {
						return "", err
					}
					msg := fmt.Sprintf("saved %s in the %s library (%s, hash %s).", saved.Ref(), saved.Scope, saved.Status, saved.Hash)
					if saved.Status == scripts.StatusDraft {
						msg += " It is a draft: ask the user to review and approve it on the Scripts page. Until then each run needs approval and it cannot run on protected environments."
					}
					return msg, nil
				}}, nil
		},
	}
}

func toolRunScript() Tool {
	return Tool{
		Def: llm.Tool{Name: "run_script", Description: "Run a saved script (see the Scripts list in the system prompt, or script action=list) on one or more workspace hosts as one unit: the exact approved content, uploaded to a private temporary folder and removed afterwards. Parameters go in params (secrets as vault:// references; opsdeck injects the values, you never see them). If the script has a check and it passes, the host is skipped as already applied unless force is true. Each host's full output is saved as an artifact and the run is recorded in the script's history. Prefer this over repeating a script's steps by hand.",
			Schema: obj(map[string]any{
				"script": str("Script name"),
				"target": str("Host name, group/<name>, or comma-separated host names"),
				"params": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Parameter values by name (strings; secrets as vault:// references)"},
				"force":  map[string]any{"type": "boolean", "description": "Run even when the script's check says it is already applied"},
				"canary": map[string]any{"type": "boolean", "description": "Several hosts: run on the first one, then ask before the rest"},
			}, "script", "target")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Script, Target string
				Params         map[string]any
				Force, Canary  bool
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			s, err := env.findScript(in.Script)
			if err != nil {
				return nil, err
			}
			given := map[string]string{}
			for k, v := range in.Params {
				switch x := v.(type) {
				case string:
					given[k] = x
				case nil:
				default:
					b, _ := json.Marshal(x)
					given[k] = string(b)
				}
			}
			values, secrets, err := scripts.Resolve(&s, given)
			if err != nil {
				return nil, err
			}
			hosts, err := env.hosts(in.Target)
			if err != nil {
				return nil, err
			}
			envName, protected := targetInfo(env.WS, hosts...)
			if s.Status != scripts.StatusApproved && protected {
				return nil, fmt.Errorf("%s is a draft (not reviewed by the user) and %s is a protected environment: ask the user to review and approve it on the Scripts page first", s.Ref(), envName)
			}
			risk := RiskChange
			if s.Risk == "destructive" {
				risk = RiskDestructive
			}
			var reasons []string
			if s.Status != scripts.StatusApproved {
				risk = RiskDestructive
				reasons = append(reasons, "draft script: not reviewed by the user yet")
			}
			if _, why := ClassifyCommand(s.Body); len(why) > 0 {
				if len(why) > 8 {
					why = append(why[:8], fmt.Sprintf("… and %d more", len(why)-8))
				}
				reasons = append(reasons, why...)
			}
			var names []string
			for _, h := range hosts {
				names = append(names, h.Name)
			}
			var keys []string
			for k := range values {
				keys = append(keys, k)
			}
			for k := range secrets {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var pv []string
			for _, k := range keys {
				v, ok := values[k]
				if !ok {
					v = secrets[k]
				}
				pv = append(pv, k+"="+v)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "run script %s (%s, %s, hash %s)", s.Ref(), s.Scope, s.Status, s.Hash)
			if s.Sudo {
				b.WriteString(" as root")
			}
			fmt.Fprintf(&b, "\non: %s\n", strings.Join(names, ", "))
			if len(hosts) == 1 {
				if path, _ := connect.DescribePath(env.WS, hosts[0]); len(path) > 0 {
					b.WriteString("via: " + strings.Join(path, " → ") + "\n")
				}
			}
			if len(pv) > 0 {
				b.WriteString("params: " + strings.Join(pv, " ") + "\n")
			}
			if s.Check != "" {
				if in.Force {
					b.WriteString("check skipped (force)\n")
				} else {
					b.WriteString("skipped where already applied: " + s.Check + "\n")
				}
			}
			fmt.Fprintf(&b, "\n--- %s ---\n%s", s.File, s.Body)
			creds := env.creds(hosts...)
			for _, k := range keys {
				if ref, ok := secrets[k]; ok {
					creds = append(creds, ref)
				}
			}
			match := s.Name + "@" + s.Hash
			if len(pv) > 0 {
				match += " " + strings.Join(pv, " ")
			}
			c := &Call{Risk: risk, Target: "host/" + strings.Join(names, ","), Environment: envName, Protected: protected,
				Match: match, Preview: truncate(b.String(), 30000), Reasons: reasons, Credentials: creds, Hosts: names, Canary: in.Canary && len(hosts) > 1,
				GrantLabel: s.Ref() + " with these parameters on " + strings.Join(names, ", ") + " (until the script changes)"}
			c.Run = func(ctx context.Context) (string, error) {
				r := scripts.Runner{WS: env.WS, Hub: env.Hub, Vault: env.Vault, Redact: NewRedactor(env.Vault.SecretValues).Redact,
					Actor: "agent", Session: env.SessionID}
				if (in.Canary || c.canary) && len(hosts) > 1 {
					first, err := r.Run(ctx, &s, hosts[:1], given, in.Force)
					if err != nil {
						return "", err
					}
					head := formatScriptResults(&s, first)
					reason := "the canary on " + names[0] + " succeeded"
					if len(first) == 0 || !first[0].OK() {
						reason = "the canary on " + names[0] + " FAILED"
					}
					approved, note, cerr := confirmStep(ctx, Step{Title: strings.Replace(continueTitle(names[1:]), "Continue on", "Run "+s.Ref()+" on", 1), Reason: reason, Risk: risk,
						Preview: truncate(head, 8000) + "\n\nnext: " + strings.Join(names[1:], ", ")})
					if cerr != nil || !approved {
						msg := head + "\nThe rollout stopped after the canary host: the user did not continue on " + strings.Join(names[1:], ", ") + "."
						if note != "" {
							msg += " Their note: " + note
						}
						return msg, nil
					}
					rest, err := r.Run(ctx, &s, hosts[1:], given, in.Force)
					if err != nil {
						return head, err
					}
					return formatScriptResults(&s, append(first, rest...)), nil
				}
				results, err := r.Run(ctx, &s, hosts, given, in.Force)
				if err != nil {
					return "", err
				}
				return formatScriptResults(&s, results), nil
			}
			return c, nil
		},
	}
}

func formatScriptResults(s *scripts.Script, results []scripts.Result) string {
	var b strings.Builder
	ok := 0
	for _, r := range results {
		if r.OK() {
			ok++
		}
	}
	fmt.Fprintf(&b, "%s: %d of %d host(s) succeeded\n", s.Ref(), ok, len(results))
	per := 60000 / (len(results) + 1)
	for _, r := range results {
		fmt.Fprintf(&b, "\n=== %s ===\n", r.Host)
		switch {
		case r.Skipped:
			b.WriteString("already applied (check passed); skipped. Use force to run anyway.\n")
			continue
		case r.ExitCode == scripts.ExitUnsupported:
			b.WriteString("unsupported system; not run.\n")
		}
		fmt.Fprintf(&b, "exit code: %d (%.1fs)\n", r.ExitCode, float64(r.DurationMs)/1000)
		if r.Error != "" {
			b.WriteString("error: " + r.Error + "\n")
		}
		if r.Stdout != "" {
			b.WriteString("stdout:\n" + truncate(r.Stdout, per*3/4) + "\n")
		}
		if r.Stderr != "" {
			b.WriteString("stderr:\n" + truncate(r.Stderr, per/4) + "\n")
		}
		if r.Log != "" {
			b.WriteString("full output: " + r.Log + "\n")
		}
	}
	return b.String()
}
