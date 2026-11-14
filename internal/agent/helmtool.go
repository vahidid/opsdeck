package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"opsdeck/internal/llm"
	"opsdeck/internal/sysproc"
)

var helmRisk = map[string]string{
	"list":      RiskRead,
	"status":    RiskRead,
	"values":    RiskRead,
	"history":   RiskRead,
	"diff":      RiskRead,
	"template":  RiskRead,
	"upgrade":   RiskChange,
	"rollback":  RiskChange,
	"uninstall": RiskDestructive,
}

func helmActions() []string {
	return []string{"list", "status", "values", "history", "diff", "template", "upgrade", "rollback", "uninstall"}
}

func toolHelm() Tool {
	return Tool{
		Def: llm.Tool{Name: "helm", Description: `Work with Helm releases on Kubernetes clusters.
list: list releases across namespaces (or in specific namespace).
status: get the status and resources of a deployed release.
values: get computed or user-supplied values of a release.
history: view release revision history.
diff: preview changes against current release before upgrading.
template: render templates locally to inspect generated manifests.
upgrade: install or upgrade a release with chart, values file or set flags.
rollback: rollback a failed release to a previous revision.
uninstall: delete a Helm release.
Target via cluster name or run directly on a host with helm installed.`,
			Schema: obj(map[string]any{
				"action":    enum("Action to perform", helmActions()...),
				"release":   str("Release name (required for status, values, history, diff, upgrade, rollback, uninstall)"),
				"chart":     str("Chart reference, repo/chart or local path (for upgrade, template, diff)"),
				"namespace": str("Kubernetes namespace (default: default; use '*' or empty for all in list)"),
				"cluster":   str("KubernetesCluster resource name in the workspace"),
				"host":      str("Optional host name to execute on via SSH (if helm is installed on a bastion/node)"),
				"revision":  num("Revision number (for rollback or history)"),
				"values":    str("Optional values file path or YAML content"),
				"set":       str("Optional --set flags (e.g. image.tag=v2.0,replicaCount=3)"),
			}, "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action    string
				Release   string
				Chart     string
				Namespace string
				Cluster   string
				Host      string
				Revision  int
				Values    string
				Set       string
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			in.Action = strings.TrimSpace(in.Action)
			risk, ok := helmRisk[in.Action]
			if !ok {
				return nil, fmt.Errorf("unknown helm action %q", in.Action)
			}

			// Build helm CLI command
			var args []string
			args = append(args, "helm", in.Action)

			ns := in.Namespace
			if ns == "" {
				ns = "default"
			}

			switch in.Action {
			case "list":
				args = []string{"helm", "list"}
				if in.Namespace == "*" || in.Namespace == "all" || in.Namespace == "" {
					args = append(args, "-A")
				} else {
					args = append(args, "-n", ns)
				}
			case "status":
				if in.Release == "" {
					return nil, errors.New("release name is required for status")
				}
				args = append(args, in.Release, "-n", ns)
			case "values":
				if in.Release == "" {
					return nil, errors.New("release name is required for values")
				}
				args = append(args, in.Release, "-n", ns, "-a")
			case "history":
				if in.Release == "" {
					return nil, errors.New("release name is required for history")
				}
				args = append(args, in.Release, "-n", ns)
			case "diff":
				if in.Release == "" || in.Chart == "" {
					return nil, errors.New("release and chart are required for diff")
				}
				args = []string{"helm", "diff", "upgrade", in.Release, in.Chart, "-n", ns}
				if in.Set != "" {
					args = append(args, "--set", in.Set)
				}
			case "template":
				if in.Chart == "" {
					return nil, errors.New("chart is required for template")
				}
				name := in.Release
				if name == "" {
					name = "release-preview"
				}
				args = append(args, name, in.Chart, "-n", ns)
				if in.Set != "" {
					args = append(args, "--set", in.Set)
				}
			case "upgrade":
				if in.Release == "" || in.Chart == "" {
					return nil, errors.New("release and chart are required for upgrade")
				}
				args = append(args, in.Release, in.Chart, "--install", "-n", ns)
				if in.Set != "" {
					args = append(args, "--set", in.Set)
				}
			case "rollback":
				if in.Release == "" {
					return nil, errors.New("release is required for rollback")
				}
				args = append(args, in.Release)
				if in.Revision > 0 {
					args = append(args, fmt.Sprintf("%d", in.Revision))
				}
				args = append(args, "-n", ns)
			case "uninstall":
				if in.Release == "" {
					return nil, errors.New("release is required for uninstall")
				}
				args = append(args, in.Release, "-n", ns)
			}

			fullCmd := strings.Join(args, " ")
			preview := fmt.Sprintf("helm %s %s in namespace %s\ncommand: %s", in.Action, in.Release, ns, fullCmd)
			target := "helm/" + in.Release
			if in.Host != "" {
				target = "host/" + in.Host
			}

			return &Call{
				Risk:        risk,
				Target:      target,
				Environment: "kubernetes",
				Match:       fullCmd,
				Preview:     preview,
				Run: func(ctx context.Context) (string, error) {
					timeout := 5 * time.Minute
					ctx, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()

					// If host is given, run on host via SSH
					if in.Host != "" {
						h, err := env.host(in.Host)
						if err != nil {
							return "", err
						}
						res, err := env.Hub.Exec(ctx, env.WS, h, fullCmd, nil, 2<<20)
						return truncate(formatExec(res, err), 35000), nil
					}

					// Otherwise run locally
					cmd := sysproc.CommandContext(ctx, "sh", "-c", fullCmd)
					b, err := cmd.CombinedOutput()
					if err != nil && len(b) == 0 {
						return "", fmt.Errorf("helm command failed (%v): install helm CLI or specify 'host' to run on a cluster node", err)
					}
					return truncate(string(b), 35000), nil
				},
			}, nil
		},
	}
}
