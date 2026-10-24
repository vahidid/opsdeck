package agent

import (
	"path/filepath"
	"regexp"
	"strings"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// Modes, from the most to the least careful.
const (
	ModePlan = "plan" // read-only investigation, ends with a plan
	ModeAsk  = "ask"  // changes need approval (default)
	ModeAuto = "auto" // everything on this computer runs, except destructive commands and credentials; servers per Policy
	ModeFull = "full" // everything on this computer runs; servers per Policy
)

// ValidMode reports whether m is a mode.
func ValidMode(m string) bool { return m == ModePlan || m == ModeAsk || m == ModeAuto || m == ModeFull }

// Policy is the user's agent policy (App settings → Agent).
type Policy struct {
	// AutoServerChanges lets Auto and Full change servers (hosts, clusters,
	// remote repositories) without asking. Off: every change on a server
	// asks, in every mode.
	AutoServerChanges bool
}

// Where a call has its effect.
const (
	ScopeLocal  = "local"  // this computer: its files and commands, the workspace, working copies, the vault
	ScopeRemote = "remote" // servers: hosts, clusters, remote repositories, MCP servers
)

var localEnvironments = map[string]bool{"local": true, "working copy": true, "workspace files": true, "this computer": true, "vault": true, "workspace topology": true}
var localTargets = []string{"local", "docs/", "skill/", "memory", "workspace/", "script/", "scripts", "reports", "artifacts", "topology", "network/", "knowledge", "session", "user"}

// scopeOf says where a call has its effect: what the tool set, else what
// its environment and target say; anything unknown counts as a server.
func scopeOf(c *Call) string {
	if c.Scope != "" {
		return c.Scope
	}
	if localEnvironments[c.Environment] {
		return ScopeLocal
	}
	for _, t := range localTargets {
		if c.Target == t || (strings.HasSuffix(t, "/") || t == "local") && strings.HasPrefix(c.Target, t) {
			return ScopeLocal
		}
	}
	return ScopeRemote
}

// Decision is the outcome of the permission check.
type Decision struct {
	Effect string // allow | ask | deny
	Reason string
}

// Decide applies, in order: plan mode, workspace rules (first match wins),
// the mode's defaults, session grants, and credential confirm-on-use.
//
//	                     Plan   Ask    Auto              Full
//	reads                yes    yes    yes               yes
//	reads with consent   ask    ask    yes (credentials  yes
//	                                   and secrets ask)
//	this computer        no     ask    yes (destructive  yes
//	                                   and guarded ask)
//	servers: changes     no     ask    ask, or per Policy (protected environments and destructive ask in Auto)
func Decide(mode string, w *workspace.Workspace, tool string, c *Call, granted map[string]bool, v *vault.Vault, pol Policy) Decision {
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
		d = byMode(mode, c, pol)
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

func byMode(mode string, c *Call, pol Policy) Decision {
	local := scopeOf(c) == ScopeLocal
	if c.Risk == RiskRead && c.Consent == "" {
		return Decision{"allow", "read-only"}
	}
	switch mode {
	case ModeFull:
		if local || c.Risk == RiskRead {
			return Decision{"allow", "full access"}
		}
		if !pol.AutoServerChanges {
			return Decision{"ask", "changes on servers ask in every mode (App settings → Agent)"}
		}
		return Decision{"allow", "full access"}
	case ModeAuto:
		switch {
		case c.Risk == RiskRead && c.Sensitive:
			return Decision{"ask", c.Consent}
		case c.Risk == RiskRead:
			return Decision{"allow", "auto mode"}
		case c.Risk == RiskDestructive:
			return Decision{"ask", "destructive action"}
		case local && c.Guarded:
			return Decision{"ask", firstNonEmpty(c.Consent, "asks in every mode but Full")}
		case local:
			return Decision{"allow", "auto mode: this computer"}
		case !pol.AutoServerChanges:
			return Decision{"ask", "changes on servers ask in every mode (App settings → Agent)"}
		case c.Protected:
			return Decision{"ask", "protected environment " + c.Environment}
		}
		return Decision{"allow", "auto mode"}
	}
	switch {
	case c.Risk == RiskRead:
		return Decision{"ask", c.Consent}
	case c.Risk == RiskDestructive:
		return Decision{"ask", "destructive action"}
	case c.Protected:
		return Decision{"ask", "protected environment " + c.Environment}
	}
	return Decision{"ask", "changes need approval in ask mode"}
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
		if c.Shell {
			return shellRuleMatches(r, c)
		}
		if ok, _ := filepath.Match(r.Match, c.Match); !ok && !strings.Contains(c.Match, strings.Trim(r.Match, "*")) {
			return false
		}
	}
	return true
}

// shellRuleMatches applies a rule's match pattern to a shell script command
// by command, like the prefix rules of Claude Code: an allow rule covers
// the script only when every command in it that is not a read matches (so
// "systemctl status*" never allows "systemctl status x; rm -rf /"); ask and
// deny rules apply when any command, or the script as a whole, matches.
func shellRuleMatches(r workspace.Rule, c *Call) bool {
	if r.Effect == "allow" {
		if c.Commands == nil {
			return false // not parsed: nothing is known to be covered
		}
		for _, p := range c.Commands {
			if p.Risk != RiskRead && !commandGlob(r.Match, p.Text) && !commandGlob(r.Match, p.Command) {
				return false
			}
		}
		return true
	}
	if commandGlob(r.Match, c.Match) || strings.Contains(c.Match, strings.Trim(r.Match, "*")) {
		return true
	}
	for _, p := range c.Commands {
		if commandGlob(r.Match, p.Text) || commandGlob(r.Match, p.Command) {
			return true
		}
	}
	return false
}

// commandGlob matches a command against a pattern where * stands for any
// text (spaces and slashes included) and ? for one character.
func commandGlob(pattern, s string) bool {
	var b strings.Builder
	b.WriteString(`^`)
	for _, r := range strings.TrimSpace(pattern) {
		switch r {
		case '*':
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString(`$`)
	ok, _ := regexp.MatchString(b.String(), strings.TrimSpace(s))
	return ok
}
