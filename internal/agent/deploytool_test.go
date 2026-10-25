package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"opsdeck/internal/connect"
	"opsdeck/internal/deployx"
	"opsdeck/internal/testutil/deployserver"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// deployEnv is an Env with an Argo CD (protected prod) and a Portainer
// platform, both fakes, tokens in the workspace's vault scope.
func deployEnv(t *testing.T) (*Env, *deployserver.Server, *deployserver.Server) {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	if err := v.Init("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	v.EnsureScope("t")
	it, err := v.Create(vault.Item{Scope: "t", Name: "deploy", Type: "token", Fields: []vault.Field{{Name: "token", Value: deployserver.Token}}, Policy: vault.Policy{AgentUse: true}})
	if err != nil {
		t.Fatal(err)
	}
	argo, pt := deployserver.ArgoCD(t), deployserver.Portainer(t)
	w := &workspace.Workspace{Metadata: workspace.Meta{Name: "t"}, Path: dir, Resources: []*workspace.Resource{
		{Kind: "DeployPlatform", Name: "argo", Environment: "prod", Spec: map[string]any{"platform": "argocd", "url": argo.URL, "token": it.Ref(), "notes": "GitOps for the shop"}},
		{Kind: "DeployPlatform", Name: "portainer", Spec: map[string]any{"platform": "portainer", "url": pt.URL, "token": it.Ref(), "targets": []any{"shop", "billing"}}},
	}}
	w.Environments = []workspace.Environment{{Name: "prod", Protected: true}}
	hub := connect.NewHub(v, func(string) string { return dir })
	return &Env{WS: w, Vault: v, Hub: hub, Deploy: deployx.New(hub, v)}, argo, pt
}

func TestDeployTool(t *testing.T) {
	env, argo, pt := deployEnv(t)
	tl := toolDeploy()
	ctx := context.Background()

	// platforms: every platform with its server and targets
	c := prepare(t, tl, env, map[string]any{"action": "platforms"})
	out, _ := viewOf(t, c)
	if c.Risk != RiskRead || !strings.Contains(out, "- argo: Argo CD at "+argo.URL+" env=prod (PROTECTED): version v3.1.0+abc, signed in as opsdeck, 2 targets (2 need attention)") ||
		!strings.Contains(out, "notes: GitOps for the shop") || !strings.Contains(out, "- portainer: Portainer at") || !strings.Contains(out, "targets shop,billing") {
		t.Fatalf("platforms: %s", out)
	}

	// targets needs the platform when there are several
	if _, err := tl.Prepare(ctx, env, raw(map[string]any{"action": "targets"})); err == nil || !strings.Contains(err.Error(), "name the platform: argo (argocd), portainer (portainer)") {
		t.Fatalf("ambiguous platform: %v", err)
	}
	c = prepare(t, tl, env, map[string]any{"action": "targets", "platform": "argo"})
	if c.Target != "deploy/argo" || !c.Protected || c.Environment != "prod" || strings.Join(c.Credentials, ",") != "vault://t/deploy" {
		t.Fatalf("targets call: %+v", c)
	}
	out, view := viewOf(t, c)
	if !strings.Contains(out, "argo (Argo CD): 2 targets") || !strings.Contains(out, "argocd/shop | shop [application] Healthy, OutOfSync on in-cluster / shop rev bbbbbbbb") {
		t.Fatalf("targets: %s", out)
	}
	if v := view.(map[string]any); v["kind"] != "deploy" {
		t.Fatalf("view: %v", view)
	}

	// status, diff, logs are reads
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "status", "platform": "argo", "target": "shop"}))
	if !strings.Contains(out, "shop (id argocd/shop)") || !strings.Contains(out, "deploys from Git") || !strings.Contains(out, "- Deployment shop/shop-web: Healthy · OutOfSync, out of sync") ||
		!strings.Contains(out, "- #2 sync rev bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb at 2026-10-24T10:00:30Z by alice: deployed") || !strings.Contains(out, "actions: deploy, rollback, refresh, restart, terminate") {
		t.Fatalf("status: %s", out)
	}
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "diff", "platform": "argo", "target": "shop"}))
	if !strings.Contains(out, "+      - image: shop:1.1") {
		t.Fatalf("diff: %s", out)
	}
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "diff", "platform": "portainer", "target": "shop"}))
	if !strings.Contains(out, "Portainer cannot show what a deploy of shop would change") {
		t.Fatalf("portainer diff: %s", out)
	}
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "logs", "platform": "portainer", "target": "shop", "service": "web"}))
	if !strings.Contains(out, "c-web started") {
		t.Fatalf("logs: %s", out)
	}

	// deploy: a change on a protected platform; the card shows the state
	// and the diff; nothing is sent before approval
	c = prepare(t, tl, env, map[string]any{"action": "deploy", "platform": "argo", "target": "shop"})
	if c.Risk != RiskChange || !c.Protected || c.Match != "deploy shop" || !strings.Contains(c.Preview, "sync shop on argo (Argo CD)") ||
		!strings.Contains(c.Preview, "now: shop [application] Healthy, OutOfSync") || !strings.Contains(c.Preview, "+      - image: shop:1.1") {
		t.Fatalf("deploy call: %s\n%s", c.Match, c.Preview)
	}
	if len(argo.Find("POST", "/api/v1/applications/shop/sync")) != 0 {
		t.Fatal("synced before approval")
	}
	out, view = viewOf(t, c)
	if !strings.Contains(out, "Sync of shop started") || !strings.Contains(out, "now: shop [application]") || len(argo.Find("POST", "/api/v1/applications/shop/sync")) != 1 {
		t.Fatalf("deploy: %s", out)
	}
	if v := view.(map[string]any); v["detail"] == nil {
		t.Fatalf("deploy view: %v", view)
	}
	// prune makes it destructive
	c = prepare(t, tl, env, map[string]any{"action": "deploy", "platform": "argo", "target": "shop", "prune": true})
	if c.Risk != RiskDestructive || c.Match != "deploy shop prune" || !strings.Contains(c.Preview, "DELETED") {
		t.Fatalf("prune: %+v", c)
	}
	// rollback names the history entry
	c = prepare(t, tl, env, map[string]any{"action": "rollback", "platform": "argo", "target": "shop", "history": "1"})
	if c.Risk != RiskChange || !strings.Contains(c.Preview, "back to: #1 rev aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatalf("rollback: %s", c.Preview)
	}
	// refresh is a read
	if c = prepare(t, tl, env, map[string]any{"action": "refresh", "platform": "argo", "target": "shop"}); c.Risk != RiskRead {
		t.Fatalf("refresh: %s", c.Risk)
	}
	// refused before approval: rollback under auto-sync, unknown action
	// for the platform, update of a Git stack, out-of-scope target
	for _, bad := range []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"action": "rollback", "platform": "argo", "target": "web", "history": "7"}, "auto-sync"},
		{map[string]any{"action": "pull", "platform": "argo", "target": "shop"}, "no action \"pull\""},
		{map[string]any{"action": "update", "platform": "portainer", "target": "billing", "file": "services: {}"}, "deploys from Git"},
		{map[string]any{"action": "deploy", "platform": "portainer", "target": "other"}, "not one of this workspace's targets"},
		{map[string]any{"action": "deploy", "platform": "portainer"}, "needs the target"},
		{map[string]any{"action": "launch", "platform": "portainer", "target": "shop"}, "unknown action"},
	} {
		if _, err := tl.Prepare(ctx, env, raw(bad.in)); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%v: %v, want %q", bad.in, err, bad.want)
		}
	}

	// update: the card shows the file diff
	file := "services:\n  web:\n    image: shop:1.1\n  worker:\n    image: shop:1.0\n"
	c = prepare(t, tl, env, map[string]any{"action": "update", "platform": "portainer", "target": "shop", "file": file})
	if c.Risk != RiskChange || c.Protected || !strings.Contains(c.Preview, "-    image: shop:1.0") || !strings.Contains(c.Preview, "+    image: shop:1.1") {
		t.Fatalf("update: %s", c.Preview)
	}
	out, _ = viewOf(t, c)
	if !strings.Contains(out, "Updated the stack file of shop") || pt.Files["1"] != file {
		t.Fatalf("update run: %s", out)
	}

	// the token never shows in outputs or previews
	for _, s := range []string{out, c.Preview} {
		if strings.Contains(s, deployserver.Token) {
			t.Fatal("token leaked")
		}
	}
	// the tool is offered only with a DeployPlatform
	has := func() bool {
		for _, tl := range availableTools(env.WS, "", false) {
			if tl.Def.Name == "deploy" {
				return true
			}
		}
		return false
	}
	if !has() {
		t.Fatal("deploy tool not offered")
	}
	env.WS.Resources = nil
	if has() {
		t.Fatal("deploy tool offered without a platform")
	}
}
