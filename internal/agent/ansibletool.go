package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"opsdeck/internal/ansible"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// ansibleProject resolves a project (optional when the workspace has one).
// A folder outside the workspace needs access to this computer and is
// treated like any local folder: reads ask, writes always ask.
func (e *Env) ansibleProject(name string) (*ansible.Project, error) {
	if e.Ansible == nil {
		return nil, errors.New("Ansible is not available")
	}
	if name = strings.TrimSpace(name); name == "" {
		if list := ansible.Resources(e.WS); len(list) == 1 {
			name = list[0].Name
		} else if len(list) > 1 {
			var names []string
			for _, r := range list {
				names = append(names, r.Name)
			}
			return nil, fmt.Errorf("say which project: %s", strings.Join(names, ", "))
		}
	}
	p, err := ansible.Open(e.WS, name)
	if err != nil {
		return nil, err
	}
	if p.External() {
		if e.Local == nil {
			return nil, fmt.Errorf("Ansible project %s is a folder outside the workspace (%s) and the agent's access to this computer is turned off (Settings → This computer)", p.Name(), p.Root)
		}
		root, err := e.Local.Resolve(p.Root)
		if err != nil {
			return nil, err
		}
		if root == filepath.Clean(e.Local.Home) || root == string(filepath.Separator) || e.Local.Sensitive(root) {
			return nil, fmt.Errorf("Ansible project %s points at %s, which the agent may not use as a project folder", p.Name(), root)
		}
	}
	return p, nil
}

// ansibleRead marks a read of project files: a folder outside the
// workspace asks once per project (like other local folders).
func ansibleRead(env *Env, p *ansible.Project, c *Call) *Call {
	c.Target = "ansible/" + p.Name()
	if p.External() && env.Local != nil {
		env.Local.readAccess(c, env, p.Root, true)
	}
	return c
}

// ansibleWrite marks a change to project files. In a repository project
// it shares the repository's approval with repo_write (nothing leaves this
// computer before a push); a folder outside the workspace always asks.
func ansibleWrite(env *Env, p *ansible.Project, c *Call, path string) *Call {
	c.Target = "ansible/" + p.Name()
	c.Risk = RiskChange
	switch {
	case p.External() && env.Local != nil:
		abs, _ := p.Path(path)
		env.Local.writeAccess(c, abs)
	case p.Source == "repo":
		c.Environment = "working copy"
		c.Grant = "git-edit:" + p.Repo
		c.GrantLabel = "Always allow editing files in the working copy of " + p.Repo + " in this session (nothing leaves this computer before a push)"
	default:
		c.Environment = "workspace files"
		c.Grant = "ansible-edit:" + p.Name()
		c.GrantLabel = "Always allow editing the files of the Ansible project " + p.Name() + " in this session"
	}
	return c
}

// vaultPrompt asks the user for a vault password on the approval card when
// none is at hand for id. A password typed in is used for this call only,
// or kept until opsdeck locks when the user picks "always".
func vaultPrompt(ctx context.Context, env *Env, p *ansible.Project, c *Call, id, reason string) *Call {
	if env.Ansible.HasPassword(ctx, p, id) {
		return c
	}
	var given string
	c.Secret = &SecretPrompt{
		Label:         fmt.Sprintf("Ansible Vault password of %s (vault id %s)", p.Name(), ansible.VaultIDName(id)),
		Reason:        reason,
		RememberLabel: "and keep the password in memory until opsdeck locks",
		Check: func(pw string) error {
			_, err := ansible.CheckPassword(p, id, pw)
			return err
		},
		Use: func(pw string, remember bool) {
			if remember {
				env.Ansible.Remember(p, id, pw)
			}
			given = pw
		},
	}
	run := c.Run
	c.Run = func(ctx context.Context) (string, error) {
		if given != "" {
			ctx = ansible.WithPassword(ctx, p, id, given)
		}
		return run(ctx)
	}
	return c
}

// missingVaultID is a vault id of the project's content without a password
// at hand ("" with ok=false when every one has, or there is none). For runs
// it only reports when no password at all is at hand.
func missingVaultID(ctx context.Context, env *Env, p *ansible.Project) (string, bool) {
	pw := env.Ansible.Passwords(ctx, p)
	if len(pw) > 0 {
		return "", false
	}
	s := p.Summarize()
	if len(s.VaultIDs) == 0 {
		return "", false
	}
	return s.VaultIDs[0], true
}

func fileVaultIDs(fc ansible.FileContent) []string {
	if fc.Encrypted {
		return []string{ansible.VaultIDName(fc.VaultID)}
	}
	seen := map[string]bool{}
	var out []string
	for _, in := range fc.Inline {
		if id := ansible.VaultIDName(in.VaultID); !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func describeProject(s ansible.Summary, v ansible.Version) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", s.Name)
	if s.Environment != "" {
		fmt.Fprintf(&b, " [%s]", s.Environment)
	}
	switch s.Source {
	case "repo":
		fmt.Fprintf(&b, " — repository %s, folder %s", s.Repo, firstNonEmpty(s.Path, "."))
	default:
		fmt.Fprintf(&b, " — folder %s", s.Root)
	}
	if s.Via != "" {
		fmt.Fprintf(&b, ", hosts reached via %s", s.Via)
	}
	b.WriteString("\n")
	if s.Description != "" {
		b.WriteString("  " + s.Description + "\n")
	}
	if s.Error != "" {
		b.WriteString("  problem: " + s.Error + "\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  files: %d; ansible.cfg: %v\n", s.Files, s.Config)
	if len(s.Playbooks) > 0 {
		b.WriteString("  playbooks: " + strings.Join(s.Playbooks, ", ") + "\n")
	}
	invs := append([]string{}, s.Inventories...)
	invs = append(invs, ansible.WorkspaceInventory+" (generated from the workspace's hosts)")
	b.WriteString("  inventories: " + strings.Join(invs, ", "))
	if s.Inventory != "" {
		b.WriteString("; default " + s.Inventory)
	}
	b.WriteString("\n")
	if len(s.VaultIDs) > 0 {
		fmt.Fprintf(&b, "  vault: %d encrypted files, %d inline values; vault ids %s; password at hand for: %s\n",
			s.VaultFiles, s.InlineVars, strings.Join(s.VaultIDs, ", "), firstNonEmpty(strings.Join(s.Unlocked, ", "), "none (the user is asked when needed)"))
	}
	if len(s.Requirements) > 0 {
		b.WriteString("  requirements: " + strings.Join(s.Requirements, ", ") + "\n")
	}
	if v.Installed {
		fmt.Fprintf(&b, "  ansible: %s", v.Version)
		if v.Lint {
			b.WriteString(", ansible-lint installed")
		}
		b.WriteString("\n")
	} else if v.Error != "" {
		b.WriteString("  ansible: " + v.Error + "\n")
	}
	return b.String()
}

func toolAnsible() Tool {
	return Tool{
		Def: llm.Tool{Name: "ansible", Description: "The workspace's Ansible projects (AnsibleProject: a folder of the workspace, a folder on this computer, or a folder in one of its Git repositories). action=projects lists them with playbooks, inventories and vault ids; files lists a project's files with their type (playbook, inventory, vars, role, template, config, requirements; vault-encrypted ones are marked); read shows a file (encrypted files only say so — use ansible_vault view); search finds text; write (whole file), edit (exact replacement) and delete change files; inventory shows the groups and hosts of an inventory (ansible-inventory --graph; inventory=workspace is generated from the workspace's hosts); syntax, list_hosts, list_tasks and list_tags check a playbook without touching hosts; runs lists past runs and log shows a run's log. Run playbooks with ansible_run. In a repository project, commit and push your changes with the repo tool and open a merge request.",
			Schema: obj(map[string]any{
				"action":     enum("Action", "projects", "files", "read", "search", "write", "edit", "delete", "inventory", "syntax", "list_hosts", "list_tasks", "list_tags", "runs", "log"),
				"project":    str("Project name; optional when the workspace has one"),
				"path":       str("read/write/edit/delete: file path relative to the project folder"),
				"content":    str("write: full file content"),
				"old_string": str("edit: exact text to replace (must match once)"),
				"new_string": str("edit: replacement"),
				"query":      str("search: text (or a regular expression with regex=true); files: filter by path"),
				"regex":      boolean("search: query is a regular expression"),
				"playbook":   str("syntax/list_*: playbook path"),
				"inventory":  str("inventory/syntax/list_*: inventory path in the project, or 'workspace'; default: the project's"),
				"limit":      str("list_hosts/list_tasks: host pattern (--limit)"),
				"tags":       str("list_tasks: only these tags"),
				"run_id":     str("log: the run"),
				"lines":      num("log: how many lines from the end (default 200)"),
				"grep":       str("log: only lines containing this text"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Project, Path, Content, Query, Playbook, Inventory, Limit, Tags, Grep string
				Old                                                                           string `json:"old_string"`
				New                                                                           string `json:"new_string"`
				Regex                                                                         bool
				RunID                                                                         string `json:"run_id"`
				Lines                                                                         int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if env.Ansible == nil {
				return nil, errors.New("Ansible is not available")
			}
			if in.Action == "projects" || in.Action == "" {
				return &Call{Risk: RiskRead, Target: "ansible", Run: func(ctx context.Context) (string, error) {
					list := env.Ansible.Projects(ctx, env.WS)
					if len(list) == 0 {
						return "No Ansible projects in this workspace. The user adds them on the Ansible page (a folder or a folder of a Git repository); you can add one with workspace_resource (kind AnsibleProject).", nil
					}
					var b strings.Builder
					for _, s := range list {
						v := ansible.Version{}
						if p, err := ansible.Open(env.WS, s.Name); err == nil {
							v = env.Ansible.Detect(ctx, p)
						}
						b.WriteString(describeProject(s, v) + "\n")
					}
					return b.String(), nil
				}}, nil
			}
			p, err := env.ansibleProject(in.Project)
			if err != nil {
				return nil, err
			}
			read := func(match string, run func(ctx context.Context) (string, error)) (*Call, error) {
				return ansibleRead(env, p, &Call{Risk: RiskRead, Match: match, Run: run}), nil
			}
			inv := firstNonEmpty(in.Inventory, p.DefaultInventory())
			switch in.Action {
			case "files":
				return read("files", func(ctx context.Context) (string, error) {
					files, truncated, err := p.Files()
					if err != nil {
						return "", err
					}
					var b strings.Builder
					n := 0
					for _, f := range files {
						if in.Query != "" && !strings.Contains(strings.ToLower(f.Path), strings.ToLower(in.Query)) {
							continue
						}
						n++
						fmt.Fprintf(&b, "%s  [%s", f.Path, f.Type)
						if f.Encrypted {
							fmt.Fprintf(&b, ", vault-encrypted, id %s", ansible.VaultIDName(f.VaultID))
						}
						if f.Inline > 0 {
							fmt.Fprintf(&b, ", %d inline !vault values", f.Inline)
						}
						fmt.Fprintf(&b, ", %d bytes]\n", f.Size)
					}
					if truncated {
						b.WriteString("(only the first 5000 files are listed)\n")
					}
					return fmt.Sprintf("%s (%s): %d files\n%s", p.Name(), p.Root, n, b.String()), nil
				})
			case "read":
				return read("read "+in.Path, func(ctx context.Context) (string, error) {
					fc, err := p.Read(in.Path)
					if err != nil {
						return "", err
					}
					switch {
					case fc.Binary:
						return fmt.Sprintf("%s is a binary file (%d bytes)", in.Path, fc.Size), nil
					case fc.Encrypted:
						return fmt.Sprintf("%s is encrypted with Ansible Vault (vault id %s). Use ansible_vault action=view to see its content (the user is asked for the password if needed), and ansible_vault edit/set to change it.", in.Path, ansible.VaultIDName(fc.VaultID)), nil
					}
					out := fc.Text
					if fc.Truncated {
						out += "\n… (truncated at 2 MB)"
					}
					if len(fc.Inline) > 0 {
						var keys []string
						for _, i := range fc.Inline {
							keys = append(keys, firstNonEmpty(i.Key, "line "+strconv.Itoa(i.Line)))
						}
						out += fmt.Sprintf("\n\n[%d inline !vault values: %s — ansible_vault view shows them decrypted, ansible_vault set changes one]", len(fc.Inline), strings.Join(keys, ", "))
					}
					return out, nil
				})
			case "search":
				return read("search "+in.Query, func(ctx context.Context) (string, error) {
					hits, err := p.Search(in.Query, in.Regex, 200)
					if err != nil {
						return "", err
					}
					if len(hits) == 0 {
						return "no matches (vault-encrypted files are not searched)", nil
					}
					var b strings.Builder
					for _, h := range hits {
						fmt.Fprintf(&b, "%s:%d: %s\n", h.Path, h.Line, h.Text)
					}
					return b.String(), nil
				})
			case "write", "edit", "delete":
				return ansibleEditCall(env, p, in.Action, in.Path, in.Content, in.Old, in.New)
			case "inventory":
				return read("inventory "+inv, func(ctx context.Context) (string, error) {
					if inv == ansible.WorkspaceInventory {
						y, err := ansible.WorkspaceInventoryYAML(env.WS, workspaceHostList(env.WS), "")
						if err != nil {
							return "", err
						}
						out, err := env.Ansible.Exec(ctx, p, ansible.RunOptions{Inventory: inv}, false, "ansible-inventory", "--graph")
						if err != nil {
							return string(y) + "\n" + out, nil
						}
						return out + "\nGenerated inventory:\n" + string(y), nil
					}
					// without vault passwords: decrypted variables are not shown here
					return env.Ansible.Exec(ctx, p, ansible.RunOptions{Inventory: inv}, false, "ansible-inventory", "--graph")
				})
			case "syntax", "list_hosts", "list_tasks", "list_tags":
				if in.Playbook == "" {
					return nil, errors.New("playbook is required")
				}
				flag := map[string]string{"syntax": "--syntax-check", "list_hosts": "--list-hosts", "list_tasks": "--list-tasks", "list_tags": "--list-tags"}[in.Action]
				o := ansible.RunOptions{Kind: ansible.KindPlaybook, Playbook: in.Playbook, Inventory: inv, Limit: in.Limit, Tags: in.Tags}
				if err := o.Validate(p); err != nil {
					return nil, err
				}
				args := []string{flag}
				if in.Limit != "" {
					args = append(args, "--limit="+in.Limit)
				}
				if in.Tags != "" {
					args = append(args, "--tags="+in.Tags)
				}
				args = append(args, "--", in.Playbook)
				return read(in.Action+" "+in.Playbook, func(ctx context.Context) (string, error) {
					return env.Ansible.Exec(ctx, p, o, true, "ansible-playbook", args...)
				})
			case "runs":
				return read("runs", func(ctx context.Context) (string, error) {
					runs := env.Ansible.Runs(env.WS, p.Name(), 30)
					if len(runs) == 0 {
						return "no runs yet", nil
					}
					var b strings.Builder
					for _, r := range runs {
						fmt.Fprintf(&b, "%s  %s  %s  by %s  %s\n", r.ID, r.Started.Local().Format("2006-01-02 15:04"), r.Status, r.Actor, r.Command)
						for _, h := range r.Recap {
							fmt.Fprintf(&b, "    %s ok=%d changed=%d unreachable=%d failed=%d\n", h.Host, h.OK, h.Changed, h.Unreachable, h.Failed)
						}
					}
					return b.String(), nil
				})
			case "log":
				if in.RunID == "" {
					return nil, errors.New("run_id is required")
				}
				return read("log "+in.RunID, func(ctx context.Context) (string, error) {
					r, err := env.Ansible.Get(env.WS, in.RunID)
					if err != nil {
						return "", err
					}
					if in.Grep != "" {
						var b strings.Builder
						for i, l := range r.Tail(0) {
							if pl := ansible.StripANSI(l); strings.Contains(strings.ToLower(pl), strings.ToLower(in.Grep)) {
								fmt.Fprintf(&b, "%d: %s\n", i+1, pl)
							}
						}
						return firstNonEmpty(b.String(), "no matching lines"), nil
					}
					n := in.Lines
					if n <= 0 {
						n = 200
					}
					return r.Summary(n), nil
				})
			}
			return nil, fmt.Errorf("unknown action %q", in.Action)
		},
	}
}

func workspaceHostList(w *workspace.Workspace) []*workspace.Resource {
	var out []*workspace.Resource
	for _, r := range w.Resources {
		if r.Kind == "Host" {
			out = append(out, r)
		}
	}
	return out
}

// ansibleEditCall writes, edits or deletes a plain project file.
func ansibleEditCall(env *Env, p *ansible.Project, action, path, content, old, new string) (*Call, error) {
	if path == "" {
		return nil, errors.New("path is required")
	}
	if _, err := p.Path(path); err != nil {
		return nil, err
	}
	fc, err := p.Read(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if exists && (fc.Binary || fc.Truncated) {
		return nil, fmt.Errorf("%s is binary or larger than 2 MB", path)
	}
	if exists && fc.Encrypted && action != "delete" {
		return nil, fmt.Errorf("%s is encrypted with Ansible Vault: change it with ansible_vault edit or set", path)
	}
	if action != "delete" && ansible.IsEncrypted([]byte(content)) && action == "write" {
		return nil, errors.New("write plain content; use ansible_vault to create encrypted files")
	}
	label := p.Name() + ":" + path
	switch action {
	case "write":
		return ansibleWrite(env, p, &Call{Match: "write " + path, Preview: "file " + label + "\n\n" + unifiedDiff(path, fc.Text, content),
			Run: func(ctx context.Context) (string, error) {
				if err := p.Write(path, content); err != nil {
					return "", err
				}
				if exists {
					return "updated " + label, nil
				}
				return "created " + label, nil
			}}, path), nil
	case "edit":
		if !exists {
			return nil, fmt.Errorf("%s does not exist (use action=write to create it)", path)
		}
		if old == "" {
			return nil, errors.New("old_string is required")
		}
		if n := strings.Count(fc.Text, old); n != 1 {
			return nil, fmt.Errorf("old_string matches %d times (must match exactly once; include surrounding lines)", n)
		}
		after := strings.Replace(fc.Text, old, new, 1)
		before := fc.Text
		return ansibleWrite(env, p, &Call{Match: "edit " + path, Preview: "file " + label + "\n\n" + unifiedDiff(path, before, after),
			Run: func(ctx context.Context) (string, error) {
				cur, err := p.ReadText(path)
				if err != nil {
					return "", err
				}
				if cur != before {
					return "", errors.New(path + " changed since it was read: read it again and redo the edit")
				}
				if err := p.Write(path, after); err != nil {
					return "", err
				}
				return "edited " + label, nil
			}}, path), nil
	case "delete":
		if !exists {
			return nil, fmt.Errorf("%s does not exist", path)
		}
		c := ansibleWrite(env, p, &Call{Match: "delete " + path, Preview: "delete " + label, Run: func(ctx context.Context) (string, error) {
			if err := p.Delete(path); err != nil {
				return "", err
			}
			return "deleted " + label, nil
		}}, path)
		if p.Source != "repo" {
			c.Risk = RiskDestructive // no Git history to bring it back
		}
		return c, nil
	}
	return nil, fmt.Errorf("unknown action %q", action)
}

// ---------------------------------------------------------------------------
// ansible_run

var readModules = map[string]bool{"ping": true, "setup": true, "gather_facts": true, "stat": true, "find": true, "service_facts": true,
	"package_facts": true, "slurp": true, "debug": true, "getent": true, "wait_for_connection": true, "command_info": true}

// adhocRisk classifies an ad-hoc module call.
func adhocRisk(module, args string) (string, []string) {
	m := module
	for _, pre := range []string{"ansible.builtin.", "ansible.legacy."} {
		m = strings.TrimPrefix(m, pre)
	}
	switch {
	case readModules[m]:
		return RiskRead, nil
	case m == "command" || m == "shell" || m == "raw":
		return ClassifyCommand(args)
	}
	return RiskChange, []string{"module " + module + " can change hosts"}
}

func toolAnsibleRun() Tool {
	return Tool{
		Def: llm.Tool{Name: "ansible_run", Description: "Run Ansible from a workspace project on this computer, with its output streamed to the user's Ansible page: kind=playbook runs ansible-playbook (check=true for a dry run that reports what would change, diff=true to show file changes, limit to a host pattern, tags/skip_tags, extra_vars); kind=adhoc runs one module on a host pattern (e.g. module=ping, or module=shell args='df -h'); kind=galaxy installs requirements.yml; kind=lint runs ansible-lint. inventory is a path in the project or 'workspace' (generated from the workspace's hosts, reached through their VPNs and jump hosts with keys from the vault). Vault passwords are supplied by opsdeck — when none is at hand the user is asked on the approval card; never ask for it in chat. Returns the status, PLAY RECAP, failures and the end of the log; the full log is in the run's history (ansible action=log).",
			Schema: obj(map[string]any{
				"project":       str("Project name; optional when the workspace has one"),
				"kind":          enum("What to run (default playbook)", "playbook", "adhoc", "galaxy", "lint"),
				"playbook":      str("playbook/lint: playbook path in the project"),
				"inventory":     str("Inventory path in the project, or 'workspace'; default: the project's"),
				"limit":         str("Host pattern to limit to (--limit), e.g. web-1 or 'web:&prod'"),
				"tags":          str("Only tasks with these tags (comma-separated)"),
				"skip_tags":     str("Skip tasks with these tags"),
				"extra_vars":    map[string]any{"type": "object", "description": "Extra variables (-e), as an object"},
				"check":         boolean("Dry run (--check): report what would change"),
				"diff":          boolean("Show the differences of changed files and templates (--diff)"),
				"verbosity":     num("0-4 (-v … -vvvv)"),
				"forks":         num("Parallel hosts (--forks)"),
				"start_at_task": str("Start at the task with this name"),
				"become":        boolean("adhoc: run with become (sudo)"),
				"pattern":       str("adhoc: host pattern (default all)"),
				"module":        str("adhoc: module name (default ping)"),
				"args":          str("adhoc: module arguments"),
				"requirements":  str("galaxy: requirements file (default requirements.yml)"),
				"vault_id":      str("A vault id whose password the run needs: the user is asked for it when it is not at hand (use it when a run failed with 'no vault secrets' for that id)"),
			})},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Project, Kind, Playbook, Inventory, Limit, Tags, Pattern, Module, Args, Requirements string
				SkipTags                                                                             string         `json:"skip_tags"`
				ExtraVars                                                                            map[string]any `json:"extra_vars"`
				Check, Diff, Become                                                                  bool
				Verbosity, Forks                                                                     int
				StartAt                                                                              string `json:"start_at_task"`
				VaultID                                                                              string `json:"vault_id"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			p, err := env.ansibleProject(in.Project)
			if err != nil {
				return nil, err
			}
			o := ansible.RunOptions{Kind: in.Kind, Playbook: in.Playbook, Inventory: in.Inventory, Limit: in.Limit, Tags: in.Tags, SkipTags: in.SkipTags,
				Check: in.Check, Diff: in.Diff, Verbosity: in.Verbosity, Forks: in.Forks, StartAt: in.StartAt, Become: in.Become,
				Pattern: in.Pattern, Module: in.Module, Args: in.Args, Requirements: in.Requirements}
			if len(in.ExtraVars) > 0 {
				b, _ := json.Marshal(in.ExtraVars)
				o.ExtraVars = string(b)
			}
			if err := o.Validate(p); err != nil {
				return nil, err
			}
			c := &Call{Target: "ansible/" + p.Name(), Environment: p.R.Environment}
			if e := env.WS.EnvironmentOf(p.R); e.Protected {
				c.Environment, c.Protected = e.Name, true
			}
			inv := firstNonEmpty(o.Inventory, p.DefaultInventory(), "(ansible.cfg)")
			var what string
			switch o.Kind {
			case ansible.KindPlaybook:
				what = "playbook " + o.Playbook
				c.Risk = RiskChange
				if o.Check {
					c.Risk = RiskRead
					c.Consent = "a check-mode run connects to the hosts (tasks marked check_mode: false still make changes)"
				}
			case ansible.KindAdhoc:
				what = fmt.Sprintf("%s on %s", o.Module, o.Pattern)
				if o.Args != "" {
					what += ": " + o.Args
				}
				c.Risk, c.Reasons = adhocRisk(o.Module, o.Args)
				if o.Become && c.Risk == RiskRead {
					c.Reasons = append(c.Reasons, "runs with become")
				}
				if c.Risk == RiskRead {
					c.Consent = "connects to the hosts"
				}
				if o.Check && c.Risk == RiskChange {
					c.Risk, c.Consent = RiskRead, "a check-mode run connects to the hosts"
				}
			case ansible.KindGalaxy:
				what = "ansible-galaxy install -r " + o.Requirements
				c.Risk, c.Environment, c.Protected = RiskChange, "this computer", false
			case ansible.KindLint:
				what = "ansible-lint " + o.Playbook
				c.Risk, c.Environment, c.Protected = RiskRead, "this computer", false
			}
			c.Match = strings.TrimSpace(fmt.Sprintf("%s %s inventory=%s limit=%s tags=%s check=%v", o.Kind, firstNonEmpty(o.Playbook, o.Module+" "+o.Args, o.Requirements), inv, o.Limit, o.Tags, o.Check))

			var preview strings.Builder
			fmt.Fprintf(&preview, "%s · project %s · inventory %s\n", what, p.Name(), inv)
			if o.Limit != "" {
				preview.WriteString("limit: " + o.Limit + "\n")
			}
			if o.Tags != "" || o.SkipTags != "" {
				fmt.Fprintf(&preview, "tags: %s  skip: %s\n", o.Tags, o.SkipTags)
			}
			if o.Check || o.Diff {
				fmt.Fprintf(&preview, "check: %v  diff: %v\n", o.Check, o.Diff)
			}
			if o.ExtraVars != "" {
				preview.WriteString("extra vars: " + o.ExtraVars + "\n")
			}
			// which hosts, and their environments: production hosts make it protected
			if o.Kind == ansible.KindPlaybook || o.Kind == ansible.KindAdhoc {
				hosts := listHosts(ctx, env, p, o)
				if len(hosts) > 0 {
					shown := hosts
					if len(shown) > 40 {
						shown = append(append([]string{}, shown[:40]...), fmt.Sprintf("… %d more", len(hosts)-40))
					}
					fmt.Fprintf(&preview, "hosts (%d): %s\n", len(hosts), strings.Join(shown, ", "))
					var rs []*workspace.Resource
					for _, h := range hosts {
						for _, r := range workspaceHostList(env.WS) {
							if r.Name == h || r.Str("address") == h {
								rs = append(rs, r)
								c.Credentials = append(c.Credentials, env.creds(r)...)
							}
						}
					}
					if e, prot := targetInfo(env.WS, rs...); prot {
						c.Environment, c.Protected = e, true
					} else if c.Environment == "" {
						c.Environment = e
					}
				}
			}
			if ref := p.R.Str("sshKey"); ref != "" {
				c.Credentials = append(c.Credentials, ref)
			}
			c.Preview = preview.String()
			c.Run = func(ctx context.Context) (string, error) {
				r, err := env.Ansible.Start(ctx, p, o, "agent:"+env.SessionID)
				if err != nil {
					return "", err
				}
				r.Wait(ctx)
				info := r.Info()
				out := r.Summary(80) + "\nThe user can follow the full log on the Ansible page (History, run " + info.ID + ")."
				if info.Status != ansible.StatusOK {
					return out, fmt.Errorf("the run ended with status %s", info.Status)
				}
				return out, nil
			}
			if o.Kind == ansible.KindPlaybook || o.Kind == ansible.KindAdhoc {
				if in.VaultID != "" {
					vaultPrompt(ctx, env, p, c, in.VaultID, "the run needs the password of vault id "+ansible.VaultIDName(in.VaultID))
				} else if id, missing := missingVaultID(ctx, env, p); missing {
					vaultPrompt(ctx, env, p, c, id, "the project uses Ansible Vault and no password is at hand")
				}
			}
			return c, nil
		},
	}
}

// listHosts asks ansible which hosts a run would target (best effort).
func listHosts(ctx context.Context, env *Env, p *ansible.Project, o ansible.RunOptions) []string {
	var args []string
	prog := "ansible-playbook"
	if o.Kind == ansible.KindAdhoc {
		prog = "ansible"
		args = append(args, "--list-hosts")
		if o.Limit != "" {
			args = append(args, "--limit="+o.Limit)
		}
		args = append(args, "--", o.Pattern)
	} else {
		args = append(args, "--list-hosts")
		if o.Limit != "" {
			args = append(args, "--limit="+o.Limit)
		}
		if o.Tags != "" {
			args = append(args, "--tags="+o.Tags)
		}
		args = append(args, "--", o.Playbook)
	}
	out, err := env.Ansible.Exec(ctx, p, ansible.RunOptions{Inventory: o.Inventory}, true, prog, args...)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var hosts []string
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.Contains(t, ":") || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "playbook") {
			continue
		}
		if strings.Contains(l, "    ") && !strings.Contains(t, " ") && !seen[t] {
			seen[t] = true
			hosts = append(hosts, t)
		}
	}
	sort.Strings(hosts)
	return hosts
}

// ---------------------------------------------------------------------------
// ansible_vault

func randomSecret(n int) string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[k.Int64()]
	}
	return string(b)
}

func toolAnsibleVault() Tool {
	return Tool{
		Def: llm.Tool{Name: "ansible_vault", Description: "Ansible Vault content of a project. action=view shows a vault-encrypted file, or a YAML file with its inline !vault values, decrypted; edit replaces one exact text in a vault file's decrypted content and encrypts it again; set sets a variable: inside a vault file, or as an inline !vault value in a plain YAML vars file (generate=true makes opsdeck create a random value you never see — use it for new passwords); encrypt encrypts a plain file in place; decrypt stores a vault file as plain text (rarely right: secrets end up readable on disk and in Git); encrypt_string returns a !vault block for a value. opsdeck supplies the vault password; when none is at hand the user types it into the approval card — never ask for it in chat. Values that look like credentials are masked before you see them.",
			Schema: obj(map[string]any{
				"action":     enum("Action", "view", "edit", "set", "encrypt", "decrypt", "encrypt_string"),
				"project":    str("Project name; optional when the workspace has one"),
				"path":       str("File path relative to the project folder"),
				"old_string": str("edit: exact text of the decrypted content to replace (must match once)"),
				"new_string": str("edit: replacement"),
				"key":        str("set: variable name (top level of the file)"),
				"value":      str("set/encrypt_string: the value"),
				"generate":   boolean("set: generate a random value instead (not shown to you)"),
				"length":     num("set with generate: length (default 24)"),
				"vault_id":   str("encrypt/encrypt_string/set in a plain file: vault id (default: 'default')"),
				"name":       str("encrypt_string: variable name for the block"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Project, Path, Key, Value, Name string
				Old                                     string `json:"old_string"`
				New                                     string `json:"new_string"`
				Generate                                bool
				Length                                  int
				VaultID                                 string `json:"vault_id"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			p, err := env.ansibleProject(in.Project)
			if err != nil {
				return nil, err
			}
			m := env.Ansible
			id := firstNonEmpty(in.VaultID, "default")
			label := p.Name() + ":" + in.Path
			if in.Action == "encrypt_string" {
				if in.Value == "" {
					return nil, errors.New("value is required")
				}
				c := &Call{Risk: RiskRead, Target: "ansible/" + p.Name(), Match: "encrypt_string", Run: func(ctx context.Context) (string, error) {
					pw, ok := m.Passwords(ctx, p)[id]
					if !ok {
						return "", &ansible.NeedPassword{Project: p.Name(), VaultID: id}
					}
					vid := in.VaultID
					if vid == "default" {
						vid = ""
					}
					return ansible.EncryptString(in.Name, in.Value, pw, vid, 0)
				}}
				return vaultPrompt(ctx, env, p, c, id, "encrypting needs the project's vault password"), nil
			}
			if in.Path == "" {
				return nil, errors.New("path is required")
			}
			fc, err := p.Read(in.Path)
			exists := err == nil
			if err != nil && !(in.Action == "set" && errors.Is(err, fs.ErrNotExist)) {
				return nil, err
			}
			if exists && (fc.Binary || fc.Truncated) {
				return nil, fmt.Errorf("%s is not a text file of at most 2 MB", in.Path)
			}
			ids := fileVaultIDs(fc)
			need := func(c *Call, reason string, ids ...string) *Call {
				for _, i := range ids {
					if !m.HasPassword(ctx, p, i) {
						return vaultPrompt(ctx, env, p, c, i, reason)
					}
				}
				return c
			}
			switch in.Action {
			case "view":
				if !fc.Encrypted && len(fc.Inline) == 0 {
					return nil, fmt.Errorf("%s is not encrypted: read it with the ansible tool", in.Path)
				}
				c := &Call{Risk: RiskRead, Target: "ansible/" + p.Name(), Match: "view " + in.Path,
					Consent: "shows the decrypted content of " + label + " to the agent", Sensitive: true,
					Grant: "ansible-view:" + p.Name(), GrantLabel: "Always allow the agent to see decrypted vault content of " + p.Name() + " in this session",
					Run: func(ctx context.Context) (string, error) { return m.View(ctx, p, in.Path) }}
				c.Preview = "decrypt " + label + " and show it to the agent (values that look like credentials are masked)"
				return need(c, "decrypting needs the vault password", ids...), nil
			case "edit":
				if !fc.Encrypted {
					return nil, fmt.Errorf("%s is not a vault file (to change an inline !vault value use set)", in.Path)
				}
				if in.Old == "" {
					return nil, errors.New("old_string is required")
				}
				if strings.Contains(in.Old, "[REDACTED") {
					return nil, errors.New("old_string contains a masked value: to change a variable whose value you cannot see, use action=set")
				}
				preview := "edit the encrypted file " + label + "\n\n"
				if plain, err := m.View(ctx, p, in.Path); err == nil {
					if n := strings.Count(plain, in.Old); n != 1 {
						return nil, fmt.Errorf("old_string matches %d times in the decrypted content (must match once)", n)
					}
					preview += unifiedDiff(in.Path, plain, strings.Replace(plain, in.Old, in.New, 1))
				} else {
					preview += unifiedDiff(in.Path, in.Old+"\n", in.New+"\n")
				}
				c := ansibleWrite(env, p, &Call{Match: "vault edit " + in.Path, Preview: preview, Run: func(ctx context.Context) (string, error) {
					plain, err := m.View(ctx, p, in.Path)
					if err != nil {
						return "", err
					}
					if n := strings.Count(plain, in.Old); n != 1 {
						return "", fmt.Errorf("old_string matches %d times in the decrypted content (must match once)", n)
					}
					after := strings.Replace(plain, in.Old, in.New, 1)
					if err := m.SaveEncrypted(ctx, p, in.Path, after, ""); err != nil {
						return "", err
					}
					return "edited " + label + " (encrypted again)\n\n" + unifiedDiff(in.Path, plain, after), nil
				}}, in.Path)
				return need(c, "editing needs the vault password", ids...), nil
			case "set":
				if in.Key == "" {
					return nil, errors.New("key is required")
				}
				value := in.Value
				shown := strconv.Quote(value)
				if in.Generate {
					n := in.Length
					if n <= 0 {
						n = 24
					}
					if n < 8 || n > 128 {
						return nil, errors.New("length is 8 to 128")
					}
					value, shown = randomSecret(n), fmt.Sprintf("a random %d-character value generated by opsdeck (not shown to the agent)", n)
				} else if value == "" {
					return nil, errors.New("value is required (or generate=true)")
				}
				where := "as an inline !vault value (vault id " + id + ")"
				if fc.Encrypted {
					where = "inside the encrypted file"
					ids = []string{ansible.VaultIDName(fc.VaultID)}
				} else {
					ids = []string{id}
				}
				c := ansibleWrite(env, p, &Call{Match: "vault set " + in.Path + " " + in.Key,
					Preview: fmt.Sprintf("set %s in %s %s to %s", in.Key, label, where, shown),
					Run: func(ctx context.Context) (string, error) {
						created, err := m.SetVar(ctx, p, in.Path, in.Key, value, id)
						if err != nil {
							return "", err
						}
						msg := fmt.Sprintf("set %s in %s %s", in.Key, label, where)
						if created {
							msg += " (new file)"
						}
						if in.Generate {
							msg += fmt.Sprintf("; the value is a random %d-character string (not shown to you; the user can view it on the Ansible page)", len(value))
						}
						return msg, nil
					}}, in.Path)
				return need(c, "setting the value needs the vault password", ids...), nil
			case "encrypt":
				if fc.Encrypted {
					return nil, fmt.Errorf("%s is already encrypted", in.Path)
				}
				c := ansibleWrite(env, p, &Call{Match: "vault encrypt " + in.Path, Preview: fmt.Sprintf("encrypt %s with vault id %s", label, id),
					Run: func(ctx context.Context) (string, error) {
						if err := m.EncryptFile(ctx, p, in.Path, id); err != nil {
							return "", err
						}
						return "encrypted " + label + " (vault id " + id + ")", nil
					}}, in.Path)
				return need(c, "encrypting needs the vault password", id), nil
			case "decrypt":
				if !fc.Encrypted {
					return nil, fmt.Errorf("%s is not encrypted", in.Path)
				}
				c := ansibleWrite(env, p, &Call{Match: "vault decrypt " + in.Path,
					Preview: "store " + label + " DECRYPTED on disk (its secrets become readable in the folder" + map[bool]string{true: " and in Git once committed", false: ""}[p.Source == "repo"] + ")",
					Run: func(ctx context.Context) (string, error) {
						if err := m.DecryptFile(ctx, p, in.Path); err != nil {
							return "", err
						}
						return "decrypted " + label + " (now plain text on disk)", nil
					}}, in.Path)
				c.Risk = RiskDestructive
				return need(c, "decrypting needs the vault password", ids...), nil
			}
			return nil, fmt.Errorf("unknown action %q", in.Action)
		},
	}
}
