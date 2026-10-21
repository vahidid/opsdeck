package agent

import (
	"path/filepath"
	"strings"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// Modes.
const (
	ModePlan = "plan" // read-only investigation, ends with a plan
	ModeAsk  = "ask"  // changes need approval (default)
	ModeAuto = "auto" // changes run without approval except on protected environments; destructive always asks
)

// Decision is the outcome of the permission check.
type Decision struct {
	Effect string // allow | ask | deny
	Reason string
}

// Decide applies, in order: plan mode, workspace rules (first match wins),
// defaults, session grants, and credential confirm-on-use.
func Decide(mode string, w *workspace.Workspace, tool string, c *Call, granted map[string]bool, v *vault.Vault) Decision {
	if mode == ModePlan && c.Risk != RiskRead {
		return Decision{"deny", "plan mode is read-only: include this step in your plan instead of running it"}
	}
	d := Decision{}
	for _, r := range w.Permissions.Rules {
		if ruleMatches(r, tool, c) {
			d = Decision{r.Effect, "workspace rule"}
			break
		}
	}
	if d.Effect == "" {
		switch {
		case c.Risk == RiskRead && c.Consent != "":
			d = Decision{"ask", c.Consent}
		case c.Risk == RiskRead:
			d = Decision{"allow", "read-only"}
		case c.Risk == RiskDestructive:
			d = Decision{"ask", "destructive action"}
		case c.Protected:
			d = Decision{"ask", "protected environment " + c.Environment}
		case mode == ModeAuto:
			d = Decision{"allow", "auto mode"}
		default:
			d = Decision{"ask", "changes need approval in ask mode"}
		}
	}
	if d.Effect == "ask" && c.Risk != RiskDestructive && isGranted(tool, c, granted) {
		d = Decision{"allow", "allowed for this session"}
	}
	if d.Effect == "allow" && v != nil {
		for _, ref := range c.Credentials {
			if it, _, err := v.Resolve(ref); err == nil && it.Policy.ConfirmOnUse {
				return Decision{"ask", "credential " + ref + " requires confirmation on use"}
			}
		}
	}
	return d
}

// grantKey scopes an "always allow" answer to the same tool, target, risk
// and exact command/action, so one approval never covers different commands.
// Tools can widen it on purpose (e.g. local reads: one folder for all read tools).
func grantKey(tool string, c *Call) string {
	if c.Grant != "" {
		return c.Grant + "|" + c.Risk
	}
	return tool + "|" + c.Target + "|" + c.Risk + "|" + c.Match
}

func isGranted(tool string, c *Call, granted map[string]bool) bool {
	if granted[grantKey(tool, c)] {
		return true
	}
	for _, g := range c.parentGrants {
		if granted[g+"|"+c.Risk] {
			return true
		}
	}
	return false
}

func ruleMatches(r workspace.Rule, tool string, c *Call) bool {
	if r.Tool != "" && r.Tool != tool {
		return false
	}
	if r.Risk != "" && r.Risk != c.Risk {
		return false
	}
	if len(r.Environment) > 0 {
		ok := false
		for _, e := range r.Environment {
			if e == c.Environment {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if r.Match != "" {
		if ok, _ := filepath.Match(r.Match, c.Match); !ok && !strings.Contains(c.Match, strings.Trim(r.Match, "*")) {
			return false
		}
	}
	return true
}
