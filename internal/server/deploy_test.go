package server

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/deployserver"
)

// TestDeployments: a workspace with an Argo CD and a Komodo platform
// (tokens in its vault): the platforms with their servers, the targets,
// one target's detail, logs and diff, an action (audited), and a platform
// whose credential is refused.
func TestDeployments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)
	c.must("PUT", "/api/workspaces/acme", map[string]any{"metadata": map[string]any{"name": "acme"}, "environments": []map[string]any{{"name": "prod", "protected": true}}}, nil)

	argo, km := deployserver.ArgoCD(t), deployserver.Komodo(t)
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "name": "argo-token", "type": "token", "fields": []map[string]any{{"name": "token", "value": deployserver.Token}}}, nil)
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "name": "komodo-key", "type": "login", "fields": []map[string]any{{"name": "username", "value": deployserver.Key}, {"name": "password", "value": deployserver.Secret}}}, nil)
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "name": "stale", "type": "token", "fields": []map[string]any{{"name": "token", "value": "revoked"}}}, nil)
	// a literal token in the spec is refused
	if code := c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "DeployPlatform", "name": "x", "spec": map[string]any{"platform": "argocd", "url": argo.URL, "token": "abc"}}, nil); code != 400 {
		t.Fatalf("literal token: %d", code)
	}
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "DeployPlatform", "name": "argo", "environment": "prod", "spec": map[string]any{"platform": "argocd", "url": argo.URL, "token": "vault://acme/argo-token"}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "DeployPlatform", "name": "komodo", "spec": map[string]any{"platform": "komodo", "url": km.URL, "token": "vault://acme/komodo-key"}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "DeployPlatform", "name": "old", "spec": map[string]any{"platform": "argocd", "url": argo.URL, "token": "vault://acme/stale"}}, nil)

	var types []map[string]any
	c.must("GET", "/api/deploy/platforms", nil, &types)
	if len(types) != 3 || types[0]["id"] != "portainer" || types[2]["credential"] != "login" {
		t.Fatalf("platform types: %v", types)
	}
	var list []map[string]any
	c.must("GET", "/api/workspaces/acme/deploy", nil, &list)
	if len(list) != 3 {
		t.Fatalf("platforms: %v", list)
	}
	if p := list[0]; p["name"] != "argo" || p["label"] != "Argo CD" || p["protected"] != true || p["error"] != nil || p["server"].(map[string]any)["user"] != "opsdeck" {
		t.Fatalf("argo: %v", p)
	}
	if p := list[1]; p["server"].(map[string]any)["version"] != "2.3.3" {
		t.Fatalf("komodo: %v", p)
	}
	if p := list[2]; !strings.Contains(p["error"].(string), "HTTP 401") {
		t.Fatalf("stale token: %v", p)
	}

	var targets []map[string]any
	c.must("GET", "/api/workspaces/acme/deploy/komodo/targets", nil, &targets)
	if len(targets) != 2 || targets[1]["id"] != "stack:st1" || targets[1]["updateAvailable"] != true {
		t.Fatalf("targets: %v", targets)
	}
	var d map[string]any
	c.must("GET", "/api/workspaces/acme/deploy/argo/target?id="+url.QueryEscape("argocd/shop"), nil, &d)
	if d["name"] != "shop" || len(d["services"].([]any)) != 2 || len(d["history"].([]any)) != 2 || d["git"] != true {
		t.Fatalf("detail: %v", d)
	}
	var diff map[string]any
	c.must("GET", "/api/workspaces/acme/deploy/argo/diff?id=shop", nil, &diff)
	if diff["supported"] != true || !strings.Contains(diff["diff"].(string), "image: shop:1.1") {
		t.Fatalf("diff: %v", diff)
	}
	c.must("GET", "/api/workspaces/acme/deploy/komodo/diff?id=api", nil, &diff)
	if diff["supported"] != false {
		t.Fatalf("deployment diff: %v", diff)
	}
	var logs map[string]string
	c.must("GET", "/api/workspaces/acme/deploy/komodo/logs?id=shop&tail=20", nil, &logs)
	if !strings.Contains(logs["text"], "ready") {
		t.Fatalf("logs: %v", logs)
	}
	if code := c.do("GET", "/api/workspaces/acme/deploy/nope/targets", nil, nil); code != 404 {
		t.Fatalf("unknown platform: %d", code)
	}

	// actions: checked first, then run and audited
	if code := c.do("POST", "/api/workspaces/acme/deploy/argo/action", map[string]any{"id": "web", "action": "rollback", "history": "7"}, nil); code != 400 {
		t.Fatalf("rollback under auto-sync: %d", code)
	}
	var res map[string]any
	c.must("POST", "/api/workspaces/acme/deploy/argo/action", map[string]any{"id": "argocd/shop", "action": "deploy", "revision": "v1.2.0"}, &res)
	if res["ok"] != true || !strings.Contains(res["message"].(string), "Sync of shop started to revision v1.2.0") {
		t.Fatalf("sync: %v", res)
	}
	if s := argo.Find("POST", "/api/v1/applications/shop/sync"); len(s) != 1 || s[0].Body["revision"] != "v1.2.0" {
		t.Fatalf("sync call: %+v", s)
	}
	var audit []map[string]any
	c.must("GET", "/api/audit?limit=20", nil, &audit)
	found := false
	for _, e := range audit {
		if e["action"] == "deploy.deploy" && e["target"] == "deploy/argo/shop" && e["workspace"] == "acme" {
			det := e["detail"].(map[string]any)
			found = det["revision"] == "v1.2.0" && strings.Contains(det["before"].(string), "OutOfSync")
		}
	}
	if !found {
		t.Fatalf("audit: %v", audit)
	}
	// nothing secret in what the page gets
	for _, v := range []any{list, targets, d, res} {
		if strings.Contains(strings.ToLower(toJSON(v)), strings.ToLower(deployserver.Token)) || strings.Contains(toJSON(v), deployserver.Secret) {
			t.Fatal("credential leaked to the page")
		}
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
