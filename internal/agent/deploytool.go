package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"opsdeck/internal/deployx"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// deployReads are the deploy tool's actions that only read.
var deployReads = map[string]bool{"platforms": true, "targets": true, "status": true, "logs": true, "diff": true, "file": true}

// toolDeploy reads and drives the workspace's deployment platforms
// (DeployPlatform resources: Portainer, Argo CD, Komodo) with the
// platform's vault credential. Reads are free; deploys, restarts and
// rollbacks wait for approval with the target's state (and, where the
// platform can tell, what would change) on the card.
func toolDeploy() Tool {
	return Tool{
		Def: llm.Tool{Name: "deploy", Description: `The workspace's deployment platforms (DeployPlatform resources: Portainer stacks, Argo CD applications, Komodo stacks and deployments), through the platform's API with its vault credential — you never see it. Reads: platforms (each platform with its version and how many targets it has), targets (what a platform deploys, with status, health, sync and revision), status (one target: services/resources with their health, the running or last operation, the deployment history), logs (recent log lines of its containers or pods; service and tail), diff (what a deploy would change: Argo CD's live vs Git, Komodo's deployed vs latest file), file (the stack's compose file). Changes, which wait for the user's approval: deploy (Portainer: redeploy, pulling the repository of a Git stack, pull=true pulls images; Argo CD: sync, revision for another branch/tag/commit, prune=true also deletes what is gone from Git; Komodo: deploy), update (Portainer: replace a stack's compose file with file and redeploy), rollback (Argo CD: to a history id), restart, start, stop (service: only one service or resource), pull (Komodo: pull images), terminate (Argo CD: stop the running sync). refresh (Argo CD: compare with Git again, hard=true) is a read.`,
			Schema: obj(map[string]any{
				"platform": str("DeployPlatform resource name (default: the workspace's only one)"),
				"action":   enum("What to do", "platforms", "targets", "status", "logs", "diff", "file", "deploy", "update", "rollback", "refresh", "restart", "start", "stop", "pull", "terminate"),
				"target":   str("The stack or application: its name, or its id from targets when names repeat"),
				"service":  str("logs/restart/deploy: one compose service, container, or Kubernetes resource name"),
				"revision": str("deploy (Argo CD): sync to this branch, tag or commit instead of the target revision"),
				"prune":    map[string]any{"type": "boolean", "description": "deploy/rollback (Argo CD): also delete resources no longer in Git; update (Portainer): remove services gone from the file"},
				"history":  str("rollback: the history id to go back to (from status)"),
				"file":     str("update: the complete new compose file"),
				"pull":     map[string]any{"type": "boolean", "description": "deploy (Portainer): pull the images first"},
				"hard":     map[string]any{"type": "boolean", "description": "refresh (Argo CD): a hard refresh (clears the manifest cache)"},
				"tail":     num("logs: lines per container or pod (default 200, max 2000)"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Platform, Action, Target, Service, Revision, History, File string
				Prune, Pull, Hard                                          bool
				Tail                                                       int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			if env.Deploy == nil {
				return nil, errors.New("deployments are not available")
			}
			if in.Action == "platforms" {
				return &Call{Risk: RiskRead, Target: "deploy/" + env.WS.Name(), Match: "platforms", Credentials: env.deployCredentials(nil), Run: func(ctx context.Context) (string, error) {
					return deployPlatforms(ctx, env), nil
				}}, nil
			}
			r, err := env.deployPlatform(in.Platform)
			if err != nil {
				return nil, err
			}
			info, ok := deployx.PlatformByID(deployx.PlatformOf(r))
			if !ok {
				return nil, fmt.Errorf("%s: unknown platform %q (portainer, argocd, komodo)", r.ID(), r.Str("platform"))
			}
			envName, protected := targetInfo(env.WS, r)
			target := "deploy/" + r.Name
			creds := env.deployCredentials(r)
			read := func(match string, run func(ctx context.Context, o *deployx.Opened) (string, error)) *Call {
				return &Call{Risk: RiskRead, Target: target, Environment: envName, Protected: protected, Match: strings.TrimSpace(match), Credentials: creds,
					Run: func(ctx context.Context) (string, error) {
						ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
						defer cancel()
						o, err := env.Deploy.Open(ctx, env.WS, r)
						if err != nil {
							return "", err
						}
						return run(ctx, o)
					}}
			}
			switch in.Action {
			case "targets":
				return read("targets", func(ctx context.Context, o *deployx.Opened) (string, error) {
					ts, err := o.Targets(ctx)
					if err != nil {
						return "", err
					}
					setView(ctx, map[string]any{"kind": "deploy", "platform": r.Name, "targets": ts})
					return formatTargets(r, info, ts), nil
				}), nil
			case "status", "logs", "diff", "file":
				if strings.TrimSpace(in.Target) == "" {
					return nil, fmt.Errorf("%s needs the target (list them with targets)", in.Action)
				}
				return read(in.Action+" "+in.Target, func(ctx context.Context, o *deployx.Opened) (string, error) {
					d, err := o.Detail(ctx, in.Target)
					if err != nil {
						return "", err
					}
					switch in.Action {
					case "logs":
						tail := in.Tail
						if tail <= 0 {
							tail = 200
						}
						return o.Logs(ctx, d, deployx.LogOptions{Service: in.Service, Tail: min(tail, 2000)})
					case "diff":
						diff, err := o.Diff(ctx, d)
						if errors.Is(err, deployx.ErrNoDiff) {
							return fmt.Sprintf("%s cannot show what a deploy of %s would change. Its current state:\n%s", info.Label, d.Name, deployx.Format(d.Target)), nil
						}
						if err != nil {
							return "", err
						}
						if diff == "" {
							return fmt.Sprintf("%s: nothing to deploy — what runs matches what is configured (%s).", d.Name, firstNonEmpty(d.Sync, d.Status)), nil
						}
						return truncate(diff, 60000), nil
					case "file":
						if d.File == "" {
							if d.Git {
								return fmt.Sprintf("%s deploys from Git (%s): read the file in the repository.", d.Name, d.Source), nil
							}
							return fmt.Sprintf("%s has no stack file on %s.", d.Name, info.Label), nil
						}
						return d.File, nil
					}
					setView(ctx, map[string]any{"kind": "deploy", "platform": r.Name, "detail": d})
					return formatDetail(d), nil
				}), nil
			case "deploy", "update", "rollback", "refresh", "restart", "start", "stop", "pull", "terminate":
			default:
				return nil, fmt.Errorf("unknown action %q (platforms, targets, status, logs, diff, file, deploy, update, rollback, refresh, restart, start, stop, pull, terminate)", in.Action)
			}
			if strings.TrimSpace(in.Target) == "" {
				return nil, fmt.Errorf("%s needs the target (list them with targets)", in.Action)
			}
			a := deployx.Action{ID: in.Action, Revision: strings.TrimSpace(in.Revision), Prune: in.Prune, History: strings.TrimSpace(in.History), Service: strings.TrimSpace(in.Service), File: in.File, Pull: in.Pull, Hard: in.Hard}
			// Look the target up now, so the approval card shows its state
			// and what a deploy would change.
			pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			o, err := env.Deploy.Open(pctx, env.WS, r)
			if err != nil {
				return nil, err
			}
			d, err := o.Detail(pctx, in.Target)
			if err != nil {
				return nil, err
			}
			if err := o.Check(d, a); err != nil {
				return nil, err
			}
			risk := a.Risk(info)
			label := in.Action
			if ai, ok := info.Action(in.Action); ok {
				label = strings.ToLower(ai.Label)
			}
			var preview strings.Builder
			fmt.Fprintf(&preview, "%s %s on %s (%s)\nnow: %s\n", label, d.Name, r.Name, info.Label, deployx.Format(d.Target))
			if a.Service != "" {
				fmt.Fprintf(&preview, "only: %s\n", a.Service)
			}
			if a.Revision != "" {
				fmt.Fprintf(&preview, "revision: %s\n", a.Revision)
			}
			if a.Prune {
				preview.WriteString("prune: resources no longer in the source are DELETED\n")
			}
			if a.Pull {
				preview.WriteString("pull: images are pulled first\n")
			}
			switch a.ID {
			case "update":
				preview.WriteString("\n" + unifiedDiff(d.Name+" stack file", d.File, a.File))
			case "rollback":
				for _, h := range d.History {
					if h.ID == a.History {
						fmt.Fprintf(&preview, "back to: #%s rev %s deployed %s\n", h.ID, h.Revision, h.Time.Format(time.RFC3339))
					}
				}
			case "deploy":
				if a.Revision == "" {
					diff, err := o.Diff(pctx, d)
					switch {
					case err == nil && diff == "":
						preview.WriteString("\nNo difference between what runs and what is configured: the deploy reapplies the same state.\n")
					case err == nil:
						preview.WriteString("\n" + truncate(diff, 20000))
					case !errors.Is(err, deployx.ErrNoDiff):
						fmt.Fprintf(&preview, "\n(could not compute what changes: %v)\n", err)
					}
				}
			}
			match := in.Action + " " + d.Name
			if a.Service != "" {
				match += " " + a.Service
			}
			if a.Prune {
				match += " prune"
			}
			return &Call{Risk: risk, Target: target, Environment: envName, Protected: protected, Match: match, Preview: strings.TrimRight(preview.String(), "\n"), Credentials: creds,
				Run: func(ctx context.Context) (string, error) {
					ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
					defer cancel()
					o, err := env.Deploy.Open(ctx, env.WS, r)
					if err != nil {
						return "", err
					}
					res, err := o.Run(ctx, d, a)
					if err != nil {
						return "", err
					}
					var b strings.Builder
					b.WriteString(res.Message + "\n")
					if res.Output != "" {
						b.WriteString("\n" + truncate(res.Output, 20000) + "\n")
					}
					// the state right after (operations run on asynchronously)
					if after, err := o.Detail(ctx, d.ID); err == nil {
						setView(ctx, map[string]any{"kind": "deploy", "platform": r.Name, "detail": after})
						fmt.Fprintf(&b, "\nnow: %s\n", deployx.Format(after.Target))
						if after.Operation != nil {
							fmt.Fprintf(&b, "operation: %s %s", after.Operation.Name, after.Operation.Phase)
							if after.Operation.Message != "" {
								b.WriteString(" — " + after.Operation.Message)
							}
							b.WriteString("\n")
						}
						if in.Action != "refresh" {
							b.WriteString("Check status again (or wait_for) until it is healthy.\n")
						}
					}
					if !res.OK {
						return b.String(), errors.New(res.Message)
					}
					return b.String(), nil
				}}, nil
		},
	}
}

// deployPlatform finds a DeployPlatform resource by name (or the only one).
func (e *Env) deployPlatform(name string) (*workspace.Resource, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "deploy/")
	name = strings.TrimPrefix(name, "DeployPlatform/")
	var all []*workspace.Resource
	for _, r := range e.WS.Resources {
		if r.Kind == "DeployPlatform" {
			all = append(all, r)
		}
	}
	if len(all) == 0 {
		return nil, errors.New("this workspace has no deployment platforms: add one (kind DeployPlatform: platform, url, token) with workspace_resource or under Resources")
	}
	if name == "" && len(all) == 1 {
		return all[0], nil
	}
	var names []string
	for _, r := range all {
		if r.Name == name {
			return r, nil
		}
		names = append(names, r.Name+" ("+deployx.PlatformOf(r)+")")
	}
	if name == "" {
		return nil, fmt.Errorf("name the platform: %s", strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("platform %q not found; platforms: %s", name, strings.Join(names, ", "))
}

// deployCredentials are the vault references a call uses (all platforms'
// when r is nil).
func (e *Env) deployCredentials(r *workspace.Resource) []string {
	var refs []string
	for _, x := range e.WS.Resources {
		if x.Kind == "DeployPlatform" && (r == nil || x == r) && x.Str("token") != "" {
			refs = append(refs, x.Str("token"))
		}
	}
	return refs
}

// deployPlatforms lists the workspace's platforms with what they say.
func deployPlatforms(ctx context.Context, env *Env) string {
	var b strings.Builder
	for _, r := range env.WS.Resources {
		if r.Kind != "DeployPlatform" {
			continue
		}
		info, _ := deployx.PlatformByID(deployx.PlatformOf(r))
		fmt.Fprintf(&b, "- %s: %s at %s", r.Name, firstNonEmpty(info.Label, r.Str("platform")), r.Str("url"))
		if r.Environment != "" {
			fmt.Fprintf(&b, " env=%s", r.Environment)
			if env.WS.EnvironmentOf(r).Protected {
				b.WriteString(" (PROTECTED)")
			}
		}
		if r.Via != "" {
			fmt.Fprintf(&b, " via %s", r.Via)
		}
		if ts := r.List("targets"); len(ts) > 0 {
			fmt.Fprintf(&b, " targets %s", strings.Join(ts, ","))
		}
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		o, err := env.Deploy.Open(cctx, env.WS, r)
		if err == nil {
			var s deployx.Server
			if s, err = o.Verify(cctx); err == nil {
				fmt.Fprintf(&b, ": version %s, signed in as %s", firstNonEmpty(s.Version, "?"), firstNonEmpty(s.User, "?"))
				var ts []deployx.Target
				if ts, err = o.Targets(cctx); err == nil {
					n, bad := len(ts), 0
					for _, t := range ts {
						if t.Health == deployx.HealthErr || t.Health == deployx.HealthWarn || t.OutOfSync {
							bad++
						}
					}
					fmt.Fprintf(&b, ", %d targets", n)
					if bad > 0 {
						fmt.Fprintf(&b, " (%d need attention)", bad)
					}
				}
			}
		}
		cancel()
		if err != nil {
			fmt.Fprintf(&b, " — not reachable: %v", err)
		}
		if n := strings.TrimSpace(r.Str("notes")); n != "" {
			fmt.Fprintf(&b, "\n  notes: %s", strings.ReplaceAll(n, "\n", "\n  "))
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "No deployment platforms in this workspace (add a DeployPlatform resource)."
	}
	return b.String()
}

func formatTargets(r *workspace.Resource, info deployx.PlatformInfo, ts []deployx.Target) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s): %d targets\n", r.Name, info.Label, len(ts))
	if len(ts) == 0 {
		if len(r.List("targets")) > 0 {
			fmt.Fprintf(&b, "(none match spec.targets %s)\n", strings.Join(r.List("targets"), ", "))
		}
		return b.String()
	}
	b.WriteString("id | name [type] status, sync … on where rev … from source\n")
	for _, t := range ts {
		fmt.Fprintf(&b, "%s | %s\n", t.ID, deployx.Format(t))
	}
	return b.String()
}

func formatDetail(d *deployx.Detail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (id %s)\n%s\n", d.Name, d.ID, deployx.Format(d.Target))
	if d.Git {
		b.WriteString("deploys from Git")
		if d.AutoSync {
			b.WriteString(", automatically")
		}
		b.WriteString("\n")
	} else if d.AutoSync {
		b.WriteString("updates automatically\n")
	}
	if d.URL != "" {
		fmt.Fprintf(&b, "open: %s\n", d.URL)
	}
	fmt.Fprintf(&b, "actions: %s\n", strings.Join(d.Actions, ", "))
	if op := d.Operation; op != nil {
		fmt.Fprintf(&b, "\noperation: %s %s", op.Name, op.Phase)
		if !op.Started.IsZero() {
			fmt.Fprintf(&b, " started %s", op.Started.Format(time.RFC3339))
		}
		if !op.Finished.IsZero() {
			fmt.Fprintf(&b, " finished %s", op.Finished.Format(time.RFC3339))
		}
		if op.Message != "" {
			fmt.Fprintf(&b, "\n  %s", oneLineMsg(op.Message, 600))
		}
		b.WriteString("\n")
	}
	if len(d.Services) > 0 {
		fmt.Fprintf(&b, "\n%d services / resources:\n", len(d.Services))
		for _, s := range d.Services {
			b.WriteString("- ")
			if s.Kind != "" {
				b.WriteString(s.Kind + " ")
			}
			if s.Namespace != "" {
				b.WriteString(s.Namespace + "/")
			}
			fmt.Fprintf(&b, "%s: %s", s.Name, s.Status)
			if s.Health != "" && s.Health != deployx.HealthOK && s.Health != deployx.HealthUnknown {
				fmt.Fprintf(&b, " (%s)", s.Health)
			}
			if s.OutOfSync {
				b.WriteString(", out of sync")
			}
			if s.Image != "" {
				fmt.Fprintf(&b, " image %s", s.Image)
			}
			if s.UpdateAvailable {
				b.WriteString(" (update available)")
			}
			if s.Message != "" {
				fmt.Fprintf(&b, " — %s", oneLineMsg(s.Message, 200))
			}
			b.WriteString("\n")
		}
	}
	if len(d.History) > 0 {
		b.WriteString("\nhistory (newest first):\n")
		for i, h := range d.History {
			if i == 15 {
				fmt.Fprintf(&b, "… %d more\n", len(d.History)-i)
				break
			}
			fmt.Fprintf(&b, "- #%s", h.ID)
			if h.Operation != "" {
				b.WriteString(" " + h.Operation)
			}
			if h.Revision != "" {
				b.WriteString(" rev " + h.Revision)
			}
			if !h.Time.IsZero() {
				b.WriteString(" at " + h.Time.Format(time.RFC3339))
			}
			if h.By != "" {
				b.WriteString(" by " + h.By)
			}
			if h.Result != "" {
				b.WriteString(": " + h.Result)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func oneLineMsg(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
