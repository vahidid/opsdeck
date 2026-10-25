package deployx

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/testutil/deployserver"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// env is a workspace "t" with one DeployPlatform per fake, credentials in
// the workspace's vault scope.
func env(t *testing.T) (*Manager, *workspace.Workspace, map[string]*deployserver.Server) {
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
	item := func(name, typ string, fields map[string]string) string {
		var fs []vault.Field
		for k, val := range fields {
			fs = append(fs, vault.Field{Name: k, Value: val})
		}
		it, err := v.Create(vault.Item{Scope: "t", Name: name, Type: typ, Fields: fs, Policy: vault.Policy{AgentUse: true}})
		if err != nil {
			t.Fatal(err)
		}
		return it.Ref()
	}
	tok := item("deploy-token", "token", map[string]string{"token": deployserver.Token})
	km := item("komodo-key", "login", map[string]string{"username": deployserver.Key, "password": deployserver.Secret})
	fakes := map[string]*deployserver.Server{"portainer": deployserver.Portainer(t), "argocd": deployserver.ArgoCD(t), "komodo": deployserver.Komodo(t)}
	w := &workspace.Workspace{Metadata: workspace.Meta{Name: "t"}, Path: dir, Resources: []*workspace.Resource{
		{Kind: "DeployPlatform", Name: "portainer", Spec: map[string]any{"platform": "portainer", "url": fakes["portainer"].URL, "token": tok, "targets": []any{"shop", "bill*"}}},
		{Kind: "DeployPlatform", Name: "argo", Spec: map[string]any{"platform": "argocd", "url": fakes["argocd"].URL + "/", "token": tok}},
		{Kind: "DeployPlatform", Name: "komodo", Spec: map[string]any{"platform": "komodo", "url": fakes["komodo"].URL, "token": km}},
	}}
	return New(connect.NewHub(v, func(string) string { return dir }), v), w, fakes
}

func open(t *testing.T, m *Manager, w *workspace.Workspace, name string) *Opened {
	t.Helper()
	o, err := m.Open(context.Background(), w, w.Find("DeployPlatform", name))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func detail(t *testing.T, o *Opened, ref string) *Detail {
	t.Helper()
	d, err := o.Detail(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPortainer(t *testing.T) {
	m, w, fakes := env(t)
	f := fakes["portainer"]
	ctx := context.Background()
	o := open(t, m, w, "portainer")

	s, err := o.Verify(ctx)
	if err != nil || s.Version != "2.21.0 CE" || s.User != "admin (administrator)" {
		t.Fatalf("verify: %+v %v", s, err)
	}
	// targets: spec.targets keeps shop and billing, not other
	ts, err := o.Targets(ctx)
	if err != nil || len(ts) != 2 || ts[0].Name != "billing" || ts[1].Name != "shop" {
		t.Fatalf("targets: %+v %v", ts, err)
	}
	if ts[0].Source != "https://git.example.com/billing.git @ main : compose.yml" || ts[0].Revision == "" || ts[1].Where != "local" || ts[1].Health != HealthOK ||
		!strings.Contains(ts[1].URL, "/#!/1/docker/stacks/shop?id=1&type=2") || strings.Join(ts[1].Actions, ",") != "deploy,update,restart,start,stop" {
		t.Fatalf("target fields: %+v", ts)
	}
	if _, err := o.Detail(ctx, "other"); err == nil || !strings.Contains(err.Error(), "not one of this workspace's targets") {
		t.Fatalf("other is outside spec.targets: %v", err)
	}
	if _, err := o.Detail(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "targets: billing, shop") {
		t.Fatalf("unknown: %v", err)
	}

	// detail: file, containers through the Docker proxy; an unhealthy one
	// makes the stack a warning
	d := detail(t, o, "shop")
	if d.ID != "1" || !strings.Contains(d.File, "image: shop:1.0") || d.Git || len(d.Services) != 2 || d.Services[1].Name != "worker" || d.Services[1].Health != HealthErr || d.Health != HealthWarn {
		t.Fatalf("detail: %+v", d)
	}
	logs, err := o.Logs(ctx, d, LogOptions{Service: "web", Tail: 50})
	if err != nil || !strings.Contains(logs, "==> web (shop-web-1, running)") || !strings.Contains(logs, "c-web started") || !strings.Contains(logs, "[stderr] 2026-10-25T10:00:01Z c-web warning") || strings.Contains(logs, "c-worker") {
		t.Fatalf("logs: %q %v", logs, err)
	}
	if _, err := o.Diff(ctx, d); err != ErrNoDiff {
		t.Fatalf("diff: %v", err)
	}

	// update: a new file, Env sent back as it was
	if err := o.Check(d, Action{ID: "update", File: d.File}); err == nil {
		t.Fatal("an unchanged file should be refused")
	}
	newFile := strings.Replace(d.File, "shop:1.0", "shop:1.1", 1)
	res, err := o.Run(ctx, d, Action{ID: "update", File: newFile, Pull: true})
	if err != nil || !res.OK {
		t.Fatalf("update: %+v %v", res, err)
	}
	put := f.Find("PUT", "/api/stacks/1")
	if len(put) != 1 || put[0].Query != "endpointId=1" || put[0].Body["StackFileContent"] != newFile || put[0].Body["PullImage"] != true || put[0].Body["RepullImageAndRedeploy"] != true {
		t.Fatalf("update call: %+v", put)
	}
	if env, _ := put[0].Body["Env"].([]any); len(env) != 1 || env[0].(map[string]any)["name"] != "TAG" {
		t.Fatalf("env not kept: %+v", put[0].Body["Env"])
	}
	// deploy of a file stack resends the file
	d = detail(t, o, "shop")
	if _, err := o.Run(ctx, d, Action{ID: "deploy"}); err != nil {
		t.Fatal(err)
	}
	if put = f.Find("PUT", "/api/stacks/1"); len(put) != 2 || put[1].Body["StackFileContent"] != newFile {
		t.Fatalf("redeploy: %+v", put)
	}
	// deploy of a Git stack: git/redeploy keeping the stored credentials
	b := detail(t, o, "billing")
	if !b.Git || o.Check(b, Action{ID: "update", File: "x"}) == nil {
		t.Fatalf("git stack: %+v", b)
	}
	if res, err := o.Run(ctx, b, Action{ID: "deploy"}); err != nil || !strings.Contains(res.Message, "pulled the repository") {
		t.Fatalf("git redeploy: %+v %v", res, err)
	}
	g := f.Find("PUT", "/api/stacks/2/git/redeploy")
	if len(g) != 1 || g[0].Body["RepositoryReferenceName"] != "refs/heads/main" || g[0].Body["RepositoryAuthentication"] != true || g[0].Body["RepositoryUsername"] != "deploy" || g[0].Body["RepositoryPassword"] != nil {
		t.Fatalf("git redeploy call: %+v", g)
	}
	// restart one service through the proxy; stop
	if res, err := o.Run(ctx, d, Action{ID: "restart", Service: "worker"}); err != nil || !res.OK || len(f.Find("POST", "/api/endpoints/1/docker/containers/c-worker/restart")) != 1 ||
		len(f.Find("POST", "/api/endpoints/1/docker/containers/c-web/restart")) != 0 {
		t.Fatalf("restart: %+v %v", res, err)
	}
	if err := o.Check(d, Action{ID: "restart", Service: "nope"}); err == nil || !strings.Contains(err.Error(), "no service") {
		t.Fatalf("unknown service: %v", err)
	}
	if _, err := o.Run(ctx, d, Action{ID: "stop"}); err != nil || len(f.Find("POST", "/api/stacks/1/stop")) != 1 {
		t.Fatalf("stop: %v", err)
	}
	if err := o.Check(d, Action{ID: "rollback", History: "1"}); err == nil || !strings.Contains(err.Error(), "no action") {
		t.Fatalf("portainer has no rollback: %v", err)
	}
}

func TestArgoCD(t *testing.T) {
	m, w, fakes := env(t)
	f := fakes["argocd"]
	ctx := context.Background()
	o := open(t, m, w, "argo")

	if s, err := o.Verify(ctx); err != nil || s.Version != "v3.1.0+abc" || s.User != "opsdeck" {
		t.Fatalf("verify: %+v %v", s, err)
	}
	ts, err := o.Targets(ctx)
	if err != nil || len(ts) != 2 {
		t.Fatalf("targets: %+v %v", ts, err)
	}
	shop, web := ts[0], ts[1]
	if shop.ID != "argocd/shop" || shop.Sync != "OutOfSync" || !shop.OutOfSync || shop.Health != HealthOK || shop.Where != "in-cluster / shop" ||
		shop.Source != "https://git.example.com/deploy.git : shop @ main" || shop.URL != f.URL+"/applications/argocd/shop" || !shop.Updated.Equal(time.Date(2026, 10, 24, 10, 0, 30, 0, time.UTC)) {
		t.Fatalf("shop: %+v", shop)
	}
	if web.Health != HealthErr || web.Where != "prod-eu / web (project frontend)" || !strings.Contains(web.Message, "0/2 ready") {
		t.Fatalf("web: %+v", web)
	}

	d := detail(t, o, "shop")
	if !d.Git || d.AutoSync || len(d.History) != 2 || d.History[0].ID != "2" || d.History[0].By != "alice" || d.History[1].Duration != 10000 ||
		len(d.Services) != 2 || !d.Services[0].OutOfSync || d.Operation == nil || d.Operation.Phase != "Succeeded" {
		t.Fatalf("detail: %+v", d)
	}
	// v3: resource health from the resource tree
	wd := detail(t, o, "argocd/web")
	if !wd.AutoSync || len(wd.Services) != 1 || wd.Services[0].Health != HealthErr || wd.Services[0].Message != "0/2 ready" {
		t.Fatalf("web detail: %+v", wd)
	}
	if err := o.Check(wd, Action{ID: "rollback", History: "7"}); err == nil || !strings.Contains(err.Error(), "auto-sync") {
		t.Fatalf("rollback under auto-sync: %v", err)
	}

	// diff: only the modified Deployment, live vs predicted
	diff, err := o.Diff(ctx, d)
	if err != nil || !strings.Contains(diff, "diff --git a/Deployment/shop/shop-web") || !strings.Contains(diff, "-      - image: shop:1.0") || !strings.Contains(diff, "+      - image: shop:1.1") || !strings.Contains(diff, "+++ after sync") || strings.Contains(diff, "Service") {
		t.Fatalf("diff: %s %v", diff, err)
	}
	logs, err := o.Logs(ctx, d, LogOptions{Tail: 100})
	if err != nil || !strings.Contains(logs, "==> Deployment shop/shop-web") || !strings.Contains(logs, "[shop-web-7d9f-abc] GET /healthz 200") {
		t.Fatalf("logs: %q %v", logs, err)
	}
	if q := f.Find("GET", "/api/v1/applications/shop/logs"); len(q) != 1 || !strings.Contains(q[0].Query, "kind=Deployment") || !strings.Contains(q[0].Query, "tailLines=100") || !strings.Contains(q[0].Query, "resourceName=shop-web") {
		t.Fatalf("logs query: %+v", q)
	}

	// sync and rollback carry appNamespace in the body
	if res, err := o.Run(ctx, d, Action{ID: "deploy", Revision: "v1.2.0", Prune: true}); err != nil || res.Message != "Sync of shop started to revision v1.2.0 (with prune)" {
		t.Fatalf("sync: %+v %v", res, err)
	}
	if s := f.Find("POST", "/api/v1/applications/shop/sync"); len(s) != 1 || s[0].Body["revision"] != "v1.2.0" || s[0].Body["prune"] != true || s[0].Body["appNamespace"] != "argocd" {
		t.Fatalf("sync call: %+v", s)
	}
	if (Action{ID: "deploy", Prune: true}).Risk(o.Info) != RiskDestructive || (Action{ID: "deploy"}).Risk(o.Info) != RiskChange || (Action{ID: "refresh"}).Risk(o.Info) != RiskRead {
		t.Fatal("risks")
	}
	if _, err := o.Run(ctx, d, Action{ID: "rollback", History: "1"}); err != nil {
		t.Fatal(err)
	}
	if r := f.Find("POST", "/api/v1/applications/shop/rollback"); len(r) != 1 || r[0].Body["id"] != float64(1) || r[0].Body["appNamespace"] != "argocd" {
		t.Fatalf("rollback call: %+v", r)
	}
	if res, err := o.Run(ctx, d, Action{ID: "refresh", Hard: true}); err != nil || !strings.Contains(res.Message, "Refreshed shop (hard)") {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	if res, err := o.Run(ctx, d, Action{ID: "restart"}); err != nil || !res.OK || res.Message != "Rollout restart of Deployment/shop-web" {
		t.Fatalf("restart: %+v %v", res, err)
	}
	if a := f.Find("POST", "/api/v1/applications/shop/resource/actions"); len(a) != 1 || !strings.Contains(a[0].Query, "group=apps") || !strings.Contains(a[0].Query, "version=v1") || !strings.Contains(a[0].Query, "kind=Deployment") {
		t.Fatalf("action call: %+v", a)
	}
	if _, err := o.Run(ctx, d, Action{ID: "terminate"}); err != nil || len(f.Find("DELETE", "/api/v1/applications/shop/operation")) != 1 {
		t.Fatalf("terminate: %v", err)
	}
	if err := o.Check(d, Action{ID: "restart", Revision: "x"}); err == nil {
		t.Fatal("revision only goes with deploy")
	}
}

func TestKomodo(t *testing.T) {
	m, w, fakes := env(t)
	f := fakes["komodo"]
	ctx := context.Background()
	o := open(t, m, w, "komodo")
	o.Platform.(*komodo).poll = time.Millisecond

	// v2: GetUser on /auth is gone, GET /user answers
	if s, err := o.Verify(ctx); err != nil || s.Version != "2.3.3" || s.User != "opsdeck-bot" {
		t.Fatalf("verify: %+v %v", s, err)
	}
	ts, err := o.Targets(ctx)
	if err != nil || len(ts) != 2 || ts[0].Name != "api" || ts[1].Name != "shop" {
		t.Fatalf("targets: %+v %v", ts, err)
	}
	api, shop := ts[0], ts[1]
	if api.ID != "deployment:d1" || api.Health != HealthWarn || api.Where != "prod-1" || api.Source != "api:3.2" || api.URL != f.URL+"/deployments/d1" {
		t.Fatalf("api: %+v", api)
	}
	if shop.ID != "stack:st1" || shop.Health != HealthOK || !shop.UpdateAvailable || shop.Source != "compose file in Komodo" {
		t.Fatalf("shop: %+v", shop)
	}
	d := detail(t, o, "shop")
	if len(d.Services) != 2 || !d.Services[0].UpdateAvailable || !strings.Contains(d.File, "shop:1.1") || len(d.History) != 1 || d.History[0].By != "alice" || d.History[0].Result != "success" || d.History[0].Duration != 12000 {
		t.Fatalf("detail: %+v", d)
	}
	if q := f.Find("POST", "/read"); !strings.Contains(lastBody(q, "ListUpdates"), `"target.id":"st1"`) || !strings.Contains(lastBody(q, "ListUpdates"), `"target.type":"Stack"`) {
		t.Fatalf("updates query: %s", lastBody(q, "ListUpdates"))
	}
	diff, err := o.Diff(ctx, d)
	if err != nil || !strings.Contains(diff, "diff --git a/compose.yaml") || !strings.Contains(diff, "-    image: shop:1.0") || !strings.Contains(diff, "+    image: shop:1.1") {
		t.Fatalf("diff: %s %v", diff, err)
	}
	logs, err := o.Logs(ctx, d, LogOptions{Service: "web", Tail: 20})
	if err != nil || !strings.Contains(logs, "web-1  | ready") || !strings.Contains(lastBody(f.Find("POST", "/read"), "GetStackLog"), `"services":["web"]`) {
		t.Fatalf("logs: %q %v", logs, err)
	}
	// deploy: execute, then follow the update until Complete
	res, err := o.Run(ctx, d, Action{ID: "deploy", Service: "web"})
	if err != nil || !res.OK || res.Message != "DeployStack of shop succeeded" || !strings.Contains(res.Output, "$ docker compose up -d") {
		t.Fatalf("deploy: %+v %v", res, err)
	}
	if !strings.Contains(lastBody(f.Find("POST", "/execute"), "DeployStack"), `"services":["web"]`) || !strings.Contains(lastBody(f.Find("POST", "/read"), "GetUpdate"), `"id":"u10"`) {
		t.Fatal("deploy calls")
	}
	a := detail(t, o, "deployment:d1")
	if _, err := o.Diff(ctx, a); err != ErrNoDiff {
		t.Fatalf("deployment diff: %v", err)
	}
	if logs, err := o.Logs(ctx, a, LogOptions{}); err != nil || !strings.Contains(logs, "[stderr]\npanic: missing DATABASE_URL") {
		t.Fatalf("deployment logs: %q %v", logs, err)
	}
	if _, err := o.Run(ctx, a, Action{ID: "stop"}); err != nil || !strings.Contains(lastBody(f.Find("POST", "/execute"), "StopDeployment"), `"deployment":"d1"`) {
		t.Fatalf("stop: %v", err)
	}
}

func lastBody(calls []deployserver.Call, typ string) string {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Body["type"] == typ {
			return calls[i].Raw
		}
	}
	return ""
}

func TestOpenErrors(t *testing.T) {
	m, w, _ := env(t)
	ctx := context.Background()
	bad := func(spec map[string]any, want string) {
		t.Helper()
		_, err := m.Open(ctx, w, &workspace.Resource{Kind: "DeployPlatform", Name: "x", Spec: spec})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: %v, want %q", spec, err, want)
		}
	}
	tok := w.Find("DeployPlatform", "argo").Str("token")
	bad(map[string]any{"platform": "jenkins", "url": "https://x", "token": tok}, "unknown platform")
	bad(map[string]any{"platform": "argocd", "url": "", "token": tok}, "URL is empty")
	bad(map[string]any{"platform": "argocd", "url": "ftp://x", "token": tok}, "invalid URL")
	bad(map[string]any{"platform": "argocd", "url": "https://x"}, "has no credential")
	bad(map[string]any{"platform": "komodo", "url": "https://x", "token": tok}, "login item with the API key")
	bad(map[string]any{"platform": "argocd", "url": "https://x", "token": tok, "caCert": "nope"}, "not valid PEM")

	// a wrong token: the platform's 401 says so
	m2, w2, fakes := env(t)
	o := open(t, m2, w2, "argo")
	o.Platform.(*argocd).c.cred.token = "wrong"
	if _, err := o.Verify(ctx); err == nil || !strings.Contains(err.Error(), "HTTP 401: the credential was refused") {
		t.Fatalf("401: %v", err)
	}
	_ = fakes
	// the agent may only use credentials allowed for agent use
	v := m.vault
	it, _ := v.Create(vault.Item{Scope: "t", Name: "human-only", Type: "token", Fields: []vault.Field{{Name: "token", Value: deployserver.Token}}})
	r := &workspace.Resource{Kind: "DeployPlatform", Name: "y", Spec: map[string]any{"platform": "argocd", "url": "https://x", "token": it.Ref()}}
	if _, err := m.Open(connect.WithActor(ctx, connect.Actor{Kind: "agent"}), w, r); err == nil || !strings.Contains(err.Error(), "not allowed for agent use") {
		t.Fatalf("agent use: %v", err)
	}
	if _, err := m.Open(ctx, w, r); err != nil {
		t.Fatalf("the user may: %v", err)
	}
}

func TestDockerLog(t *testing.T) {
	frame := func(typ byte, s string) []byte {
		h := []byte{typ, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
		return append(h, s...)
	}
	raw := append(frame(1, "out 1\nout 2\n"), frame(2, "err 1\n")...)
	if got := dockerLog(raw); got != "out 1\nout 2\n[stderr] err 1\n" {
		t.Fatalf("multiplexed: %q", got)
	}
	if got := dockerLog([]byte("plain tty output\n")); got != "plain tty output\n" {
		t.Fatalf("tty: %q", got)
	}
}

func TestFormatAndManages(t *testing.T) {
	o := &Opened{patterns: []string{"shop-*", "billing"}}
	for name, want := range map[string]bool{"shop-web": true, "shop": false, "billing": true, "billing-2": false} {
		if o.Manages(name) != want {
			t.Fatalf("manages %s", name)
		}
	}
	s := Format(Target{Name: "shop", Type: "application", Status: "Healthy", Sync: "OutOfSync", Where: "in-cluster / shop", Revision: "0123456789abcdef0123456789abcdef01234567", Message: "a\n b"})
	if s != "shop [application] Healthy, OutOfSync on in-cluster / shop rev 01234567 — a b" {
		t.Fatalf("format: %s", s)
	}
}
