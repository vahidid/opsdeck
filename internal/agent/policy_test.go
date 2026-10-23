package agent

import (
	"testing"

	"opsdeck/internal/workspace"
)

func shellCall(cmd string) *Call {
	an := AnalyzeCommand(cmd)
	return &Call{Risk: an.Risk, Target: "host/web-1", Environment: "prod", Protected: true, Match: cmd, Shell: true, Commands: an.Parts}
}

// Rules with a match pattern apply to each command of a shell script.
func TestShellRules(t *testing.T) {
	ws := func(rules ...workspace.Rule) *workspace.Workspace {
		return &workspace.Workspace{Permissions: workspace.Permissions{Rules: rules}}
	}
	restart := ws(workspace.Rule{Effect: "allow", Tool: "exec", Match: "systemctl restart nginx*"})
	status := ws(workspace.Rule{Effect: "allow", Tool: "exec", Match: "systemctl status*"})
	deny := ws(workspace.Rule{Effect: "deny", Tool: "exec", Match: "iptables -F*"})
	askDocker := ws(workspace.Rule{Effect: "ask", Tool: "exec", Match: "docker *"})
	cases := []struct {
		w    *workspace.Workspace
		mode string
		cmd  string
		want string
	}{
		// an allow rule covers the script when every non-read command matches
		{restart, ModeAsk, "systemctl restart nginx && systemctl status nginx --no-pager", "allow"},
		{restart, ModeAsk, "sudo systemctl restart nginx", "allow"},
		{restart, ModeAsk, "systemctl restart nginx; rm -rf /tmp/cache", "ask"},
		{restart, ModeAsk, "echo systemctl restart nginx; systemctl stop nginx", "ask"},
		{status, ModeAsk, "systemctl status x; systemctl restart y", "ask"},
		{restart, ModeAsk, "systemctl restart nginx; (", "ask"}, // unparsed: an allow rule covers nothing
		// writes and variables outside the commands are never covered by a command pattern
		{restart, ModeAsk, "systemctl restart nginx > /etc/cron.d/x", "ask"},
		{restart, ModeAsk, "{ systemctl restart nginx; } > /etc/cron.d/x", "ask"},
		{restart, ModeAsk, "export PATH=/tmp/x:$PATH; systemctl restart nginx", "ask"},
		{ws(workspace.Rule{Effect: "allow", Tool: "exec", Match: "psql *"}), ModeAsk, "psql -c 'DROP TABLE users'", "ask"},
		// deny and ask rules apply when any command matches
		{deny, ModeFull, "echo hi && sudo iptables -F", "deny"},
		{askDocker, ModeFull, "uptime; docker ps", "ask"},
		// reads on servers run without asking, in every mode
		{ws(), ModeFull, "set -euo pipefail; dpkg -l | grep nginx", "allow"},
		{ws(), ModeAsk, "for f in /etc/nginx/sites-enabled/*; do echo \"== $f\"; cat \"$f\"; done", "allow"},
		{ws(), ModePlan, "docker exec shop-db-1 psql -U shop -c 'select count(*) from orders'", "allow"},
		// changes on servers still ask, also in Full (App settings → Agent)
		{ws(), ModeFull, "systemctl restart nginx", "ask"},
	}
	for _, c := range cases {
		if got := Decide(c.mode, c.w, "exec", shellCall(c.cmd), nil, nil, Policy{}); got.Effect != c.want {
			t.Errorf("%s %q: got %s (%s), want %s", c.mode, c.cmd, got.Effect, got.Reason, c.want)
		}
	}
}

func TestCommandParts(t *testing.T) {
	an := AnalyzeCommand(`sudo systemctl restart nginx && bash -c 'cat /etc/hosts | grep db'`)
	var got []string
	for _, p := range an.Parts {
		got = append(got, p.Command+"="+p.Risk)
	}
	want := []string{"systemctl restart nginx=change", "cat /etc/hosts=read", "grep db=read"}
	if len(got) != len(want) {
		t.Fatalf("parts: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d: %s, want %s", i, got[i], want[i])
		}
	}
	if AnalyzeCommand("echo (").Parts != nil {
		t.Error("a script that does not parse has no parts")
	}
}
