package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/gitx"
	"opsdeck/internal/llm"
)

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func strArray(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// repo resolves a GitRemote by name; with one repository the name may be
// left out.
func (e *Env) repo(name string) (*gitx.Repo, error) {
	if e.Git == nil {
		return nil, errors.New("Git is not available")
	}
	name = strings.TrimPrefix(strings.TrimPrefix(name, "git/"), "GitRemote/")
	var names []string
	for _, r := range e.WS.Resources {
		if r.Kind == "GitRemote" {
			names = append(names, r.Name)
		}
	}
	if name == "" && len(names) == 1 {
		name = names[0]
	}
	if e.WS.Find("GitRemote", name) == nil {
		if len(names) == 0 {
			return nil, errors.New("this workspace has no Git repositories: add one (kind GitRemote) with workspace_resource or on the Repositories page")
		}
		return nil, fmt.Errorf("git repository %q not found; repositories in this workspace: %s", name, strings.Join(names, ", "))
	}
	return e.Git.Open(e.WS, name)
}

// editCall is a change to the local working copy: it never leaves this
// computer, so one approval covers every edit of the repository in the
// session (pushing is approved separately).
func editCall(env *Env, repo *gitx.Repo, match, preview string, run func(ctx context.Context) (string, error)) *Call {
	return &Call{Risk: RiskChange, Target: "git/" + repo.R.Name, Environment: "working copy", Match: match, Preview: preview,
		Grant:      "git-edit:" + repo.R.Name,
		GrantLabel: "Always allow editing, branching and committing in the working copy of " + repo.R.Name + " in this session (nothing leaves this computer before a push)",
		Run:        run}
}

func formatStatus(name string, st *gitx.Status) string {
	if !st.Cloned {
		if st.Busy == "clone" {
			return name + ": being cloned right now"
		}
		return name + ": not cloned yet (repo action=clone)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: on %s", name, firstNonEmpty(st.Branch, "detached HEAD "+short(st.Head)))
	if st.Upstream != "" {
		fmt.Fprintf(&b, ", tracking %s", st.Upstream)
		if st.Ahead > 0 || st.Behind > 0 {
			fmt.Fprintf(&b, " (ahead %d, behind %d)", st.Ahead, st.Behind)
		} else {
			b.WriteString(" (up to date at last fetch)")
		}
	} else if st.Branch != "" {
		b.WriteString(", not pushed yet")
	}
	if st.Default != "" {
		fmt.Fprintf(&b, "; default branch %s", st.Default)
	}
	if st.Busy != "" {
		fmt.Fprintf(&b, "; %s in progress", st.Busy)
	}
	if len(st.Changes) == 0 {
		b.WriteString("\nno uncommitted changes")
	} else {
		fmt.Fprintf(&b, "\n%d uncommitted changes:\n", len(st.Changes))
		for i, c := range st.Changes {
			if i == 200 {
				fmt.Fprintf(&b, "… %d more\n", len(st.Changes)-200)
				break
			}
			if c.Orig != "" {
				fmt.Fprintf(&b, "  %s %s → %s\n", c.Status, c.Orig, c.Path)
			} else {
				fmt.Fprintf(&b, "  %s %s\n", c.Status, c.Path)
			}
		}
	}
	return b.String()
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func toolRepo() Tool {
	return Tool{
		Def: llm.Tool{Name: "repo", Description: "The workspace's Git repositories (GitLab, GitHub; e.g. deployment manifests, compose files, Helm values, Terraform). Each has a working copy on this computer that you change with repo_write and review here before pushing. Credentials (token, deploy key) and VPNs are handled by opsdeck. Actions: list (all repositories with branch/changes), status, clone, fetch, pull (fast-forward; rebase=true to put local commits on top), branches, checkout (branch; create=true for a new branch from start, default the current HEAD), log (ref, path, n), diff (uncommitted changes, optional paths), show (rev: a commit with its patch), commit (message, optional paths; stages and commits), push (branch, default current; force uses --force-with-lease), discard (throw away uncommitted changes of paths, all when empty). Pushing to the default branch or a protected branch always needs the user's approval: work on a branch and open a merge request instead.",
			Schema: obj(map[string]any{
				"action":  enum("Action", "list", "status", "clone", "fetch", "pull", "branches", "checkout", "log", "diff", "show", "commit", "push", "discard"),
				"name":    str("Repository (GitRemote name); optional when the workspace has one"),
				"branch":  str("checkout/push/clone: branch name"),
				"create":  boolean("checkout: create the branch"),
				"start":   str("checkout with create: start point, e.g. origin/main"),
				"rebase":  boolean("pull: rebase local commits onto the remote branch"),
				"message": str("commit: commit message (first line ≤ 72 chars, then a blank line and details)"),
				"paths":   strArray("commit/diff/discard: limit to these paths"),
				"ref":     str("log: branch, tag or commit"),
				"path":    str("log: only commits touching this path"),
				"rev":     str("show: commit"),
				"n":       num("log: number of commits (default 20)"),
				"force":   boolean("push: --force-with-lease (rewrites remote history)"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Name, Branch, Start, Message, Ref, Path, Rev string
				Create, Rebase, Force                                bool
				Paths                                                []string
				N                                                    int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if in.Action == "list" {
				return &Call{Risk: RiskRead, Target: "workspace/" + env.WS.Name(), Run: func(ctx context.Context) (string, error) {
					return listRepos(ctx, env), nil
				}}, nil
			}
			repo, err := env.repo(in.Name)
			if err != nil {
				return nil, err
			}
			target := "git/" + repo.R.Name
			read := func(run func(ctx context.Context) (string, error)) *Call {
				return &Call{Risk: RiskRead, Target: target, Match: in.Action, Run: run}
			}
			network := func(c *Call) *Call {
				c.Credentials = env.Git.Credentials(env.WS, repo.R)
				return c
			}
			switch in.Action {
			case "status":
				return read(func(ctx context.Context) (string, error) {
					st, err := repo.Status(ctx)
					if err != nil {
						return "", err
					}
					return formatStatus(repo.R.Name, st), nil
				}), nil
			case "clone":
				// Downloading into the workspace changes nothing elsewhere.
				return network(read(func(ctx context.Context) (string, error) {
					out, err := repo.Clone(ctx, in.Branch)
					if err != nil {
						return "", err
					}
					return "cloned into repos/" + repo.R.Name + " — " + out, nil
				})), nil
			case "fetch":
				return network(read(func(ctx context.Context) (string, error) {
					if _, err := repo.Fetch(ctx); err != nil {
						return "", err
					}
					st, err := repo.Status(ctx)
					if err != nil {
						return "", err
					}
					return "fetched\n" + formatStatus(repo.R.Name, st), nil
				})), nil
			case "pull":
				run := func(ctx context.Context) (string, error) { return repo.Pull(ctx, in.Rebase) }
				if in.Rebase {
					c := editCall(env, repo, "pull --rebase", "pull with rebase in "+repo.R.Name+" (local commits are replayed on the remote branch)", run)
					return network(c), nil
				}
				return network(read(run)), nil
			case "branches":
				return read(func(ctx context.Context) (string, error) {
					list, err := repo.Branches(ctx)
					if err != nil {
						return "", err
					}
					var b strings.Builder
					for _, br := range list {
						mark := " "
						if br.Current {
							mark = "*"
						}
						name := br.Name
						if br.Remote {
							name = "origin/" + name
						}
						fmt.Fprintf(&b, "%s %s %s %s", mark, name, br.Commit, br.Subject)
						if br.Track != "" {
							fmt.Fprintf(&b, " [%s]", br.Track)
						}
						b.WriteString("\n")
					}
					return b.String(), nil
				}), nil
			case "log":
				return read(func(ctx context.Context) (string, error) {
					n := in.N
					if n <= 0 {
						n = 20
					}
					list, err := repo.Log(ctx, in.Ref, in.Path, n)
					if err != nil {
						return "", err
					}
					if len(list) == 0 {
						return "no commits", nil
					}
					var b strings.Builder
					for _, c := range list {
						fmt.Fprintf(&b, "%s %s %s — %s", c.Short, c.Date.Format("2006-01-02 15:04"), c.Author, c.Subject)
						if c.Refs != "" {
							fmt.Fprintf(&b, " (%s)", c.Refs)
						}
						b.WriteString("\n")
					}
					return b.String(), nil
				}), nil
			case "diff":
				return read(func(ctx context.Context) (string, error) {
					d, trunc, err := repo.Diff(ctx, in.Paths, 0)
					if err != nil {
						return "", err
					}
					if d == "" {
						return "no uncommitted changes", nil
					}
					if trunc {
						d += "\n(diff truncated)"
					}
					return truncate(d, 40000), nil
				}), nil
			case "show":
				return read(func(ctx context.Context) (string, error) {
					d, _, err := repo.Show(ctx, firstNonEmpty(in.Rev, "HEAD"), 0)
					return truncate(d, 40000), err
				}), nil
			case "checkout":
				what := "switch to " + in.Branch
				if in.Create {
					what = "create branch " + in.Branch + " from " + firstNonEmpty(in.Start, "the current HEAD")
				}
				return editCall(env, repo, "checkout "+in.Branch, what+" in "+repo.R.Name, func(ctx context.Context) (string, error) {
					return repo.Checkout(ctx, in.Branch, in.Create, in.Start)
				}), nil
			case "commit":
				if strings.TrimSpace(in.Message) == "" {
					return nil, errors.New("message is required")
				}
				d, _, err := repo.Diff(ctx, in.Paths, 30000)
				if err != nil {
					return nil, err
				}
				if strings.TrimSpace(d) == "" {
					return nil, errors.New("nothing to commit: no changes" + map[bool]string{true: " at these paths", false: ""}[len(in.Paths) > 0])
				}
				preview := "commit to " + firstNonEmpty(repo.CurrentBranch(ctx), "detached HEAD") + " in " + repo.R.Name + ":\n" + in.Message + "\n\n" + truncate(d, 20000)
				return editCall(env, repo, "commit", preview, func(ctx context.Context) (string, error) {
					return repo.Commit(ctx, gitx.CommitOptions{Message: in.Message, Paths: in.Paths, Session: env.SessionID})
				}), nil
			case "push":
				branch := firstNonEmpty(in.Branch, repo.CurrentBranch(ctx))
				if branch == "" {
					return nil, errors.New("HEAD is detached: check out a branch to push")
				}
				out, err := repo.Outgoing(ctx, branch)
				if err != nil {
					return nil, err
				}
				envName, protected := targetInfo(env.WS, repo.R)
				c := &Call{Risk: RiskChange, Target: target, Environment: envName, Protected: protected, Match: "push " + branch,
					Credentials: env.Git.Credentials(env.WS, repo.R),
					Preview:     "push " + branch + " to " + repo.Remote.Web() + "\n" + truncate(out, 20000)}
				if prot, why := repo.ProtectedBranch(ctx, branch); prot {
					c.Protected = true
					c.Environment = firstNonEmpty(envName, "repository")
					c.Reasons = append(c.Reasons, "pushes straight to "+branch+" ("+why+"): changes land without review and may deploy")
				}
				if in.Force {
					c.Risk = RiskDestructive
					c.Reasons = append(c.Reasons, "force push rewrites the remote branch's history")
				}
				c.Run = func(ctx context.Context) (string, error) {
					res, err := repo.Push(ctx, gitx.PushOptions{Branch: branch, Force: in.Force})
					if err != nil {
						return "", err
					}
					return res + "\nOpen a merge request with merge_request action=create when the change should be reviewed.", nil
				}
				return c, nil
			case "discard":
				st, err := repo.Status(ctx)
				if err != nil {
					return nil, err
				}
				what := "all uncommitted changes"
				if len(in.Paths) > 0 {
					what = strings.Join(in.Paths, ", ")
				}
				return &Call{Risk: RiskDestructive, Target: target, Environment: "working copy", Match: "discard",
					Preview: "throw away " + what + " in " + repo.R.Name + "\n" + formatStatus(repo.R.Name, st),
					Run: func(ctx context.Context) (string, error) {
						return repo.Discard(ctx, in.Paths)
					}}, nil
			}
			return nil, fmt.Errorf("unknown action %q", in.Action)
		},
	}
}

func listRepos(ctx context.Context, env *Env) string {
	var b strings.Builder
	for _, r := range env.WS.Resources {
		if r.Kind != "GitRemote" {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s", r.Name, r.Str("url"))
		if r.Environment != "" {
			fmt.Fprintf(&b, " [%s]", r.Environment)
		}
		if r.Via != "" {
			fmt.Fprintf(&b, " via %s", r.Via)
		}
		if r.Bool("docs", false) {
			b.WriteString(" (documentation)")
		}
		if r.Description != "" {
			fmt.Fprintf(&b, " — %s", r.Description)
		}
		b.WriteString("\n  ")
		repo, err := env.Git.Open(env.WS, r.Name)
		if err != nil {
			b.WriteString("error: " + err.Error() + "\n")
			continue
		}
		st, err := repo.Status(ctx)
		if err != nil {
			b.WriteString("error: " + err.Error() + "\n")
			continue
		}
		first, _, _ := strings.Cut(formatStatus(r.Name, st), "\n")
		b.WriteString(strings.TrimPrefix(first, r.Name+": "))
		if st.Cloned {
			fmt.Fprintf(&b, ", %d uncommitted changes", len(st.Changes))
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "No Git repositories in this workspace."
	}
	return b.String()
}

func toolRepoFiles() Tool {
	return Tool{
		Def: llm.Tool{Name: "repo_files", Description: "Read a Git repository's working copy (or any branch/commit with ref). action=list lists files (under path, a folder prefix); action=read returns a file with line numbers; action=search finds text in all files (pattern; regex=true for an extended regular expression).",
			Schema: obj(map[string]any{
				"action":      enum("Action", "list", "read", "search"),
				"name":        str("Repository (GitRemote name); optional when the workspace has one"),
				"path":        str("list: folder prefix; read: file path relative to the repository root"),
				"ref":         str("Optional branch, tag or commit instead of the working copy (list, read)"),
				"pattern":     str("search: text to find"),
				"regex":       boolean("search: pattern is a regular expression"),
				"ignore_case": boolean("search: case-insensitive"),
				"offset":      num("read: first line (1-based)"),
				"limit":       num("read: number of lines (default 2000)"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Name, Path, Ref, Pattern string
				Regex                            bool
				IgnoreCase                       bool `json:"ignore_case"`
				Offset, Limit                    int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			repo, err := env.repo(in.Name)
			if err != nil {
				return nil, err
			}
			c := &Call{Risk: RiskRead, Target: "git/" + repo.R.Name, Match: in.Action}
			switch in.Action {
			case "list":
				c.Run = func(ctx context.Context) (string, error) {
					t, err := repo.Files(ctx, in.Ref, 0)
					if err != nil {
						return "", err
					}
					prefix := strings.Trim(in.Path, "/")
					var b strings.Builder
					n := 0
					for _, f := range t.Files {
						if prefix != "" && prefix != "." && f != prefix && !strings.HasPrefix(f, prefix+"/") {
							continue
						}
						if n++; n > 3000 {
							b.WriteString("… (more files: narrow with path)\n")
							break
						}
						b.WriteString(f + "\n")
					}
					if n == 0 {
						return "no files", nil
					}
					return b.String(), nil
				}
			case "read":
				c.Run = func(ctx context.Context) (string, error) {
					f, err := repo.Read(ctx, in.Path, in.Ref, 2<<20)
					if err != nil {
						return "", err
					}
					if f.Binary {
						return fmt.Sprintf("%s is a binary file (%d bytes)", f.Path, f.Size), nil
					}
					return numberLines(f.Path, f.Text, f.Truncated, in.Offset, in.Limit), nil
				}
			case "search":
				c.Run = func(ctx context.Context) (string, error) {
					out, trunc, err := repo.Grep(ctx, in.Pattern, in.Regex, in.IgnoreCase, 0)
					if err != nil {
						return "", err
					}
					if out == "" {
						return "no matches", nil
					}
					if trunc {
						out += "\n(more matches: narrow the pattern)"
					}
					return truncate(out, 30000), nil
				}
			default:
				return nil, fmt.Errorf("unknown action %q", in.Action)
			}
			return c, nil
		},
	}
}

func toolRepoWrite() Tool {
	return Tool{
		Def: llm.Tool{Name: "repo_write", Description: "Change files in a Git repository's working copy (nothing leaves this computer until you commit and push with the repo tool). action=write creates or replaces a file with content; action=edit replaces one exact occurrence of old_string with new_string (include surrounding lines to make it unique; replace_all for every occurrence); action=delete removes a file. Paths are relative to the repository root; .git is off limits.",
			Schema: obj(map[string]any{
				"action":      enum("Action", "write", "edit", "delete"),
				"name":        str("Repository (GitRemote name); optional when the workspace has one"),
				"path":        str("File path relative to the repository root"),
				"content":     str("write: full file content"),
				"old_string":  str("edit: exact text to replace"),
				"new_string":  str("edit: replacement"),
				"replace_all": boolean("edit: replace every occurrence"),
			}, "action", "path")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Name, Path, Content string
				Old                         string `json:"old_string"`
				New                         string `json:"new_string"`
				All                         bool   `json:"replace_all"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			repo, err := env.repo(in.Name)
			if err != nil {
				return nil, err
			}
			before := ""
			exists := false
			if f, err := repo.Read(ctx, in.Path, "", 4<<20); err == nil {
				if f.Binary || f.Truncated {
					if in.Action != "delete" {
						return nil, fmt.Errorf("%s is binary or larger than 4 MB: it cannot be edited here", in.Path)
					}
				}
				before, exists = f.Text, true
			} else if !strings.Contains(err.Error(), "does not exist") {
				return nil, err
			}
			label := repo.R.Name + ":" + in.Path
			switch in.Action {
			case "write":
				return editCall(env, repo, "write "+in.Path, "file "+label+"\n\n"+unifiedDiff(in.Path, before, in.Content), func(ctx context.Context) (string, error) {
					if err := repo.Write(in.Path, in.Content); err != nil {
						return "", err
					}
					if exists {
						return "updated " + label, nil
					}
					return "created " + label, nil
				}), nil
			case "edit":
				if !exists {
					return nil, fmt.Errorf("%s does not exist (use action=write to create it)", in.Path)
				}
				if in.Old == "" {
					return nil, errors.New("old_string is required")
				}
				n := strings.Count(before, in.Old)
				if n == 0 || (n > 1 && !in.All) {
					return nil, fmt.Errorf("old_string matches %d times (must match exactly once, or set replace_all)", n)
				}
				after := strings.Replace(before, in.Old, in.New, map[bool]int{true: -1, false: 1}[in.All])
				return editCall(env, repo, "edit "+in.Path, "file "+label+"\n\n"+unifiedDiff(in.Path, before, after), func(ctx context.Context) (string, error) {
					cur, err := repo.Read(ctx, in.Path, "", 4<<20)
					if err != nil {
						return "", err
					}
					if cur.Text != before {
						return "", errors.New(in.Path + " changed since it was read: read it again and redo the edit")
					}
					if err := repo.Write(in.Path, after); err != nil {
						return "", err
					}
					return "edited " + label + " (" + strconv.Itoa(n) + " replacement" + map[bool]string{true: "s", false: ""}[n > 1 && in.All] + ")", nil
				}), nil
			case "delete":
				if !exists {
					return nil, fmt.Errorf("%s does not exist", in.Path)
				}
				return editCall(env, repo, "delete "+in.Path, "delete "+label, func(ctx context.Context) (string, error) {
					if err := repo.Delete(in.Path); err != nil {
						return "", err
					}
					return "deleted " + label + " (restore it with repo action=discard paths=[" + in.Path + "] before committing)", nil
				}), nil
			}
			return nil, fmt.Errorf("unknown action %q", in.Action)
		},
	}
}

func formatMR(m gitx.MR) string {
	var b strings.Builder
	fmt.Fprintf(&b, "!%d %s [%s", m.ID, m.Title, m.State)
	if m.Draft {
		b.WriteString(", draft")
	}
	b.WriteString("]")
	fmt.Fprintf(&b, " %s → %s by %s, updated %s", m.Source, m.Target, m.Author, m.Updated.Format("2006-01-02 15:04"))
	if m.Checks != "" {
		fmt.Fprintf(&b, ", CI %s", m.Checks)
	}
	if m.Mergeable != "" {
		fmt.Fprintf(&b, ", merge status %s", m.Mergeable)
	}
	if m.URL != "" {
		b.WriteString("\n  " + m.URL)
	}
	return b.String()
}

func toolMergeRequest() Tool {
	return Tool{
		Def: llm.Tool{Name: "merge_request", Description: "Merge requests (GitLab) / pull requests (GitHub) of a workspace repository, through its hosting API with the repository's token. action=list (state: open|merged|closed|all), get (id), create (title, description, source = branch to merge, default the current branch, target default the default branch; push the branch first), comment (id, body), merge (id; method merge|squash|rebase; remove_source deletes the branch). Merging always needs the user's approval: it changes the target branch, which often deploys.",
			Schema: obj(map[string]any{
				"action":        enum("Action", "list", "get", "create", "comment", "merge"),
				"name":          str("Repository (GitRemote name); optional when the workspace has one"),
				"id":            num("Merge request number (GitLab iid, GitHub PR number)"),
				"state":         enum("list: state (default open)", "open", "merged", "closed", "all"),
				"title":         str("create: title"),
				"description":   str("create: description (Markdown): what changes, why, how it was verified, rollback"),
				"source":        str("create: source branch (default: current branch)"),
				"target":        str("create: target branch (default: the default branch)"),
				"draft":         boolean("create: open as draft"),
				"body":          str("comment: text (Markdown)"),
				"method":        enum("merge: method", "merge", "squash", "rebase"),
				"remove_source": boolean("create/merge: delete the source branch after merging"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action, Name, State, Title, Description, Source, Target, Body, Method string
				ID                                                                    int
				Draft                                                                 bool
				RemoveSource                                                          bool `json:"remove_source"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			repo, err := env.repo(in.Name)
			if err != nil {
				return nil, err
			}
			target := "git/" + repo.R.Name
			c := &Call{Target: target, Match: in.Action, Credentials: env.Git.Credentials(env.WS, repo.R)}
			api := func(ctx context.Context) (gitx.Provider, context.Context, context.CancelFunc, error) {
				ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
				p, err := repo.Provider(ctx)
				if err != nil {
					cancel()
					return nil, nil, nil, err
				}
				return p, ctx, cancel, nil
			}
			needID := func() error {
				if in.ID <= 0 {
					return errors.New("id is required")
				}
				return nil
			}
			switch in.Action {
			case "list":
				c.Risk = RiskRead
				c.Run = func(ctx context.Context) (string, error) {
					p, ctx, cancel, err := api(ctx)
					if err != nil {
						return "", err
					}
					defer cancel()
					list, err := p.MRs(ctx, in.State)
					if err != nil {
						return "", err
					}
					if len(list) == 0 {
						return "no " + firstNonEmpty(in.State, "open") + " merge requests", nil
					}
					var b strings.Builder
					for _, m := range list {
						b.WriteString(formatMR(m) + "\n")
					}
					return b.String(), nil
				}
			case "get":
				if err := needID(); err != nil {
					return nil, err
				}
				c.Risk = RiskRead
				c.Run = func(ctx context.Context) (string, error) {
					p, ctx, cancel, err := api(ctx)
					if err != nil {
						return "", err
					}
					defer cancel()
					m, err := p.MR(ctx, in.ID)
					if err != nil {
						return "", err
					}
					return formatMR(*m) + "\n\n" + truncate(m.Description, 10000), nil
				}
			case "create":
				if strings.TrimSpace(in.Title) == "" {
					return nil, errors.New("title is required")
				}
				src := firstNonEmpty(in.Source, repo.CurrentBranch(ctx))
				dst := firstNonEmpty(in.Target, repo.DefaultBranch(ctx))
				if src == "" {
					return nil, errors.New("source branch is required (HEAD is detached)")
				}
				if src == dst {
					return nil, fmt.Errorf("source and target are both %s: create a branch, commit and push it first", src)
				}
				c.Risk, c.Match = RiskChange, "create "+src+" → "+dst
				c.Preview = fmt.Sprintf("open a merge request in %s\n%s → %s\n\n%s\n\n%s", repo.Remote.Web(), src, dst, in.Title, in.Description)
				c.Run = func(ctx context.Context) (string, error) {
					p, ctx, cancel, err := api(ctx)
					if err != nil {
						return "", err
					}
					defer cancel()
					m, err := p.CreateMR(ctx, gitx.NewMR{Title: in.Title, Description: in.Description, Source: src, Target: dst, Draft: in.Draft, RemoveSource: in.RemoveSource})
					if err != nil {
						return "", err
					}
					return "opened " + formatMR(*m), nil
				}
			case "comment":
				if err := needID(); err != nil {
					return nil, err
				}
				c.Risk, c.Match = RiskChange, "comment "+strconv.Itoa(in.ID)
				c.Preview = fmt.Sprintf("comment on !%d in %s:\n\n%s", in.ID, repo.R.Name, in.Body)
				c.Run = func(ctx context.Context) (string, error) {
					p, ctx, cancel, err := api(ctx)
					if err != nil {
						return "", err
					}
					defer cancel()
					if err := p.Comment(ctx, in.ID, in.Body); err != nil {
						return "", err
					}
					return fmt.Sprintf("commented on !%d", in.ID), nil
				}
			case "merge":
				if err := needID(); err != nil {
					return nil, err
				}
				envName, _ := targetInfo(env.WS, repo.R)
				c.Risk, c.Match = RiskChange, "merge "+strconv.Itoa(in.ID)
				c.Protected, c.Environment = true, firstNonEmpty(envName, "repository")
				c.Reasons = []string{"merging changes the target branch, which often deploys"}
				c.Preview = fmt.Sprintf("merge !%d in %s (%s)", in.ID, repo.R.Name, firstNonEmpty(in.Method, "merge"))
				pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				if p, err := repo.Provider(pctx); err == nil {
					if m, err := p.MR(pctx, in.ID); err == nil {
						c.Preview = "merge " + formatMR(*m) + "\nmethod: " + firstNonEmpty(in.Method, "merge")
						if in.RemoveSource {
							c.Preview += ", then delete " + m.Source
						}
					}
				}
				cancel()
				c.Run = func(ctx context.Context) (string, error) {
					p, ctx, cancel, err := api(ctx)
					if err != nil {
						return "", err
					}
					defer cancel()
					m, err := p.Merge(ctx, in.ID, in.Method, in.RemoveSource)
					if err != nil {
						return "", err
					}
					return "merged: " + formatMR(*m) + "\nPull the target branch (repo action=pull) to update the working copy.", nil
				}
			default:
				return nil, fmt.Errorf("unknown action %q", in.Action)
			}
			return c, nil
		},
	}
}
