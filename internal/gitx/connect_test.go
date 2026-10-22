package gitx

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/config"
	"opsdeck/internal/testutil/gitserver"
	"opsdeck/internal/workspace"
)

// TestConnector: a GitLab account connected with OAuth device sign-in; a
// repository with nothing but its URL uses it (host match), and an
// expiring token is renewed on use.
func TestConnector(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	gs := gitserver.New(t)
	gs.TokenTTL = 60 // expires within the refresh margin
	ts := httptest.NewTLSServer(gs.Handler(t))
	defer ts.Close()

	c := config.GitConnection{ID: "gitlab-acme", Provider: "gitlab", URL: ts.URL, CACert: certPEM(ts), Method: "oauth", OAuthClientID: "app1"}
	hc, err := ConnHTTPClient(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StartDevice(ctx, hc, config.GitConnection{Provider: "gitlab", URL: ts.URL, OAuthClientID: "nope"}); err == nil || !strings.Contains(err.Error(), "unknown application") {
		t.Fatalf("bad client: %v", err)
	}
	dc, err := StartDevice(ctx, hc, c)
	if err != nil || dc.UserCode != "WDJB-MJHT" {
		t.Fatalf("device: %+v %v", dc, err)
	}
	tok, err := PollDevice(ctx, hc, c, dc)
	if err != nil || tok.AccessToken != token || tok.RefreshToken != "refresh-1" || time.Until(tok.Expires) > time.Minute {
		t.Fatalf("poll: %+v %v", tok, err)
	}
	if gs.Polls != 2 {
		t.Fatalf("polls %d", gs.Polls)
	}
	it, err := f.v.Create(TokenItem("git-gitlab-acme", c, tok))
	if err != nil {
		t.Fatal(err)
	}
	c.Credential = it.Ref()
	f.m.Connections = func() []config.GitConnection { return []config.GitConnection{c} }
	f.m.Vault = f.v

	// The account's repositories.
	acct, _ := NewAccount(c, token, nil)
	list, err := acct.Repos(ctx, "")
	if err != nil || len(list) != 2 || list[0].FullName != "acme/deploy" || !list[0].CanPush || list[1].CanPush {
		t.Fatalf("repos %+v %v", list, err)
	}
	if list, _ := acct.Repos(ctx, "web"); len(list) != 1 || list[0].Name != "website" {
		t.Fatalf("search %+v", list)
	}

	// A repository with only its URL: connector by host, token renewed.
	f.put(&workspace.Resource{Kind: "GitRemote", Name: "deploy", Spec: map[string]any{"url": list0(t, acct).HTTPURL}})
	repo, err := f.m.Open(f.w, "deploy")
	if err != nil || repo.Conn == nil || repo.Conn.ID != "gitlab-acme" {
		t.Fatalf("open %+v %v", repo, err)
	}
	if _, err := repo.Clone(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if gs.Refreshes != 1 {
		t.Fatalf("refreshes %d", gs.Refreshes)
	}
	saved, _, _ := f.v.Resolve(c.Credential)
	if saved.Get("refresh_token") != "refresh-2" {
		t.Fatalf("vault not updated: %v", saved.Get("refresh_token"))
	}
	if refs := f.m.Credentials(f.w, repo.R); len(refs) != 1 || refs[0] != c.Credential {
		t.Fatalf("credentials %v", refs)
	}
	repo.Write("k8s/app.yaml", "replicas: 9\n")
	repo.Checkout(ctx, "more", true, "")
	if _, err := repo.Commit(ctx, CommitOptions{Message: "more"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Push(ctx, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, gs.Bare, "log", "-1", "--format=%an", "more"); strings.TrimSpace(got) != "Deploy Bot" {
		t.Fatalf("author %q", got)
	}
	p, err := repo.Provider(ctx)
	if err != nil || p.Kind() != "gitlab" {
		t.Fatalf("provider %v %v", p, err)
	}
	if gs.Refreshes != 1 {
		t.Fatalf("token refreshed again: %d", gs.Refreshes)
	}

	// Naming an unknown connector is an error; a repository on another host
	// has no connector.
	f.put(&workspace.Resource{Kind: "GitRemote", Name: "gone", Spec: map[string]any{"url": "https://x.example/a/b", "connection": "nope"}})
	if _, err := f.m.Open(f.w, "gone"); err == nil || !strings.Contains(err.Error(), "Connectors") {
		t.Fatalf("unknown connector: %v", err)
	}
	f.put(&workspace.Resource{Kind: "GitRemote", Name: "other", Spec: map[string]any{"url": "https://x.example/a/b"}})
	if r, _ := f.m.Open(f.w, "other"); r.Conn != nil {
		t.Fatal("host mismatch must not use the connector")
	}
}

func list0(t *testing.T, a *Account) RemoteRepo {
	l, err := a.Repos(context.Background(), "deploy")
	if err != nil || len(l) == 0 {
		t.Fatal(err)
	}
	return l[0]
}

func TestConnHelpers(t *testing.T) {
	cases := []struct {
		c         config.GitConnection
		api, page string
	}{
		{config.GitConnection{Provider: "github", URL: "https://github.com"}, "https://api.github.com", "https://github.com/settings/tokens/new?"},
		{config.GitConnection{Provider: "github", URL: "https://ghe.corp"}, "https://ghe.corp/api/v3", "https://ghe.corp/settings/tokens/new?"},
		{config.GitConnection{Provider: "gitlab", URL: "https://git.corp:8443"}, "https://git.corp:8443/api/v4", "https://git.corp:8443/-/user_settings/personal_access_tokens?"},
	}
	for _, c := range cases {
		if got := APIBase(c.c); got != c.api {
			t.Errorf("api %s", got)
		}
		if got := TokenPage(c.c); !strings.HasPrefix(got, c.page) {
			t.Errorf("page %s", got)
		}
	}
}
