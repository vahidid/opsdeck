package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/llm"
	"opsdeck/internal/sysproc"
)

var tfRisk = map[string]string{
	"plan":       RiskRead,
	"show":       RiskRead,
	"state_list": RiskRead,
	"validate":   RiskRead,
	"output":     RiskRead,
	"refresh":    RiskChange,
	"apply":      RiskChange, // escalated to RiskDestructive if plan has destroys
}

func tfActions() []string {
	return []string{"plan", "show", "state_list", "validate", "output", "apply", "refresh"}
}

func toolTerraform() Tool {
	return Tool{
		Def: llm.Tool{Name: "terraform", Description: `Work with Terraform or OpenTofu configurations on workspace hosts or this computer.
plan: run plan and analyze blast radius (added, changed, destroyed resources).
show: show the current state or a saved plan.
state_list: list all managed resources in the state.
validate: check configuration syntax and internal consistency.
output: read configuration outputs.
apply: apply changes (needs approval; destruction of resources is highlighted with high risk).
refresh: update state against real infrastructure (-refresh-only).
Runs on a host (host parameter) or on this computer if host is omitted.`,
			Schema: obj(map[string]any{
				"action":   enum("Action to perform", tfActions()...),
				"dir":      str("Working directory holding .tf files (e.g. /opt/infra or ~/projects/terraform)"),
				"host":     str("Optional host name to run on via SSH; omit to run on this computer"),
				"binary":   enum("Binary to use: terraform or tofu (default: terraform)", "terraform", "tofu"),
				"target":   str("Optional resource target, e.g. -target=aws_instance.web"),
				"var_file": str("Optional var file path, e.g. terraform.tfvars or prod.tfvars"),
			}, "action", "dir")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Action   string
				Dir      string
				Host     string
				Binary   string
				Target   string
				VarFile  string `json:"var_file"`
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			in.Action = strings.TrimSpace(in.Action)
			in.Dir = strings.TrimSpace(in.Dir)
			if in.Action == "" || in.Dir == "" {
				return nil, errors.New("action and dir are required")
			}
			bin := in.Binary
			if bin != "tofu" {
				bin = "terraform"
			}
			risk, ok := tfRisk[in.Action]
			if !ok {
				return nil, fmt.Errorf("unknown terraform action %q", in.Action)
			}

			// Build command line
			var cmdParts []string
			cmdParts = append(cmdParts, bin, in.Action)
			switch in.Action {
			case "plan":
				cmdParts = append(cmdParts, "-no-color", "-input=false")
			case "apply":
				cmdParts = append(cmdParts, "-auto-approve", "-no-color", "-input=false")
			case "refresh":
				cmdParts = append(cmdParts, "-refresh-only", "-auto-approve", "-no-color", "-input=false")
			case "show", "validate":
				cmdParts = append(cmdParts, "-no-color")
			case "state_list":
				cmdParts = []string{bin, "state", "list"}
			case "output":
				cmdParts = []string{bin, "output", "-json"}
			}
			if in.Target != "" {
				cmdParts = append(cmdParts, in.Target)
			}
			if in.VarFile != "" {
				cmdParts = append(cmdParts, "-var-file="+in.VarFile)
			}
			fullCmd := strings.Join(cmdParts, " ")

			var target string
			var envName string
			var protected bool
			var creds []string

			if in.Host != "" {
				h, err := env.host(in.Host)
				if err != nil {
					return nil, err
				}
				target = "host/" + h.Name
				envName, protected = targetInfo(env.WS, h)
				creds = env.creds(h)
			} else {
				target = "local"
				envName = "local"
				protected = false
			}

			preview := fmt.Sprintf("%s in %s\ncommand: %s", in.Action, in.Dir, fullCmd)
			if in.Host != "" {
				preview += "\non host: " + in.Host
			} else {
				preview += "\non this computer"
			}

			return &Call{
				Risk:        risk,
				Target:      target,
				Environment: envName,
				Protected:   protected,
				Match:       fullCmd,
				Preview:     preview,
				Credentials: creds,
				Run: func(ctx context.Context) (string, error) {
					execCmd := fmt.Sprintf("cd %s && %s", in.Dir, fullCmd)
					timeout := 10 * time.Minute
					ctx, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()

					var out string
					if in.Host != "" {
						h, err := env.host(in.Host)
						if err != nil {
							return "", err
						}
						res, err := env.Hub.Exec(ctx, env.WS, h, execCmd, nil, 2<<20)
						out = formatExec(res, err)
					} else {
						lp, err := env.local()
						if err != nil {
							return "", err
						}
						cwd, err := lp.Resolve(in.Dir)
						if err != nil {
							return "", err
						}
						cmd := sysproc.CommandContext(ctx, "sh", "-c", fullCmd)
						cmd.Dir = cwd
						b, err := cmd.CombinedOutput()
						if err != nil && len(b) == 0 {
							return "", err
						}
						out = string(b)
					}

					if in.Action == "plan" {
						out = analyzeTerraformPlan(out)
					}
					return truncate(out, 40000), nil
				},
			}, nil
		},
	}
}

// analyzeTerraformPlan parses plan output to highlight blast radius (creates, changes, destroys).
func analyzeTerraformPlan(planOutput string) string {
	var summary strings.Builder
	re := regexp.MustCompile(`Plan:\s*(\d+)\s*to add,\s*(\d+)\s*to change,\s*(\d+)\s*to destroy\.`)
	match := re.FindStringSubmatch(planOutput)
	if len(match) == 4 {
		add, _ := strconv.Atoi(match[1])
		change, _ := strconv.Atoi(match[2])
		destroy, _ := strconv.Atoi(match[3])

		summary.WriteString("### 📊 Terraform Blast Radius Summary\n")
		summary.WriteString(fmt.Sprintf("- 🟢 **Added:** %d resource(s)\n", add))
		summary.WriteString(fmt.Sprintf("- 🟡 **Modified:** %d resource(s)\n", change))
		summary.WriteString(fmt.Sprintf("- 🔴 **Destroyed:** %d resource(s)\n", destroy))

		if destroy > 0 {
			summary.WriteString("\n⚠️ **WARNING: This plan will DESTROY existing infrastructure! Review carefully before applying.**\n")
		}
		summary.WriteString("\n---\n")
	} else if strings.Contains(planOutput, "No changes. Your infrastructure matches the configuration.") {
		summary.WriteString("✅ **Infrastructure is up to date: No changes needed.**\n\n")
	}

	return summary.String() + planOutput
}
