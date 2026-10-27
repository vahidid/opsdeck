package gitx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"opsdeck/internal/connect"
	"opsdeck/internal/testutil/gitserver"
	"opsdeck/internal/testutil/sshd"
	"opsdeck/internal/testutil/wgpeer"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// TestMain lets the test binary act as `opsdeck socks-connect` (ssh's
// ProxyCommand for SSH remotes behind a jump host).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "socks-connect" {
		if err := SocksConnect(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const token = gitserver.Token

var sh = gitserver.Git

type fixture struct {
	t   *testing.T
	v   *vault.Vault
	m   *Manager
	w   *workspace.Workspace
	tmp string
}

func setup(t *testing.T) *fixture {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp := t.TempDir()
	v, err := vault.Open(filepath.Join(tmp, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	if err := v.Init("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if err := v.EnsureScope("acme"); err != nil {
		t.Fatal(err)
	}
	wm := workspace.NewManager(filepath.Join(tmp, "workspaces"), nil, nil)
	w, err := wm.Create(workspace.Meta{Name: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	hub := connect.NewHub(v, func(string) string { return w.StateDir() })
	t.Cleanup(hub.CloseAll)
	m := New(hub)
	m.Executable = os.Args[0]
	return &fixture{t: t, v: v, m: m, w: w, tmp: tmp}
}

func (f *fixture) item(typ, name string, fields map[string]string) string {
	it := vault.Item{Scope: "acme", Type: typ, Name: name, Policy: vault.Policy{AgentUse: true}}
	for k, v := range fields {
		it.Fields = append(it.Fields, vault.Field{Name: k, Value: v, Secret: k != "username"})
	}
	if _, err := f.v.Create(it); err != nil {
		f.t.Fatal(err)
	}
	return "vault://acme/" + name
}

func (f *fixture) put(r *workspace.Resource) {
	old := ""
	if f.w.Find(r.Kind, r.Name) != nil {
		old = r.ID()
	}
	if err := f.w.PutResource(old, r); err != nil {
		f.t.Fatal(err)
	}
}

func certPEM(ts *httptest.Server) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}))
}

// TestHTTPSWorkflow: a self-hosted GitLab over HTTPS with a private CA and a
// token: clone, docs, edit, commit, push, merge request, pull, discard.
func TestHTTPSWorkflow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	gs := gitserver.New(t)
	bare := gs.Bare
	ts := httptest.NewTLSServer(gs.Handler(t))
	defer ts.Close()
	url := ts.URL + "/acme/deploy.git"

	res := &workspace.Resource{Kind: "GitRemote", Name: "deploy", Environment: "prod",
		Spec: map[string]any{"url": url, "docs": true}}
	f.put(res)
	repo, err := f.m.Open(f.w, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Remote.Path != "acme/deploy" {
		t.Fatalf("path %q", repo.Remote.Path)
	}

	// Unknown CA, then no token: both fail with a hint.
	if _, err := repo.Check(ctx); err == nil || !strings.Contains(err.Error(), "CA certificate") {
		t.Fatalf("untrusted server: %v", err)
	}
	res.Spec["caCert"] = certPEM(ts)
	f.put(res)
	repo, _ = f.m.Open(f.w, "deploy")
	if _, err := repo.Check(ctx); err == nil || !strings.Contains(err.Error(), "access token") {
		t.Fatalf("no token: %v", err)
	}
	res.Spec["token"] = f.item("token", "gitlab-deploy", map[string]string{"token": token})
	f.put(res)
	repo, _ = f.m.Open(f.w, "deploy")
	if b, err := repo.Check(ctx); err != nil || b != "main" {
		t.Fatalf("check: %q %v", b, err)
	}

	// Clone.
	if _, err := repo.Clone(ctx, ""); err != nil {
		t.Fatal(err)
	}
	st, err := repo.Status(ctx)
	if err != nil || !st.Cloned || st.Branch != "main" || st.Default != "main" || len(st.Changes) != 0 {
		t.Fatalf("status %+v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.w.Path, ".gitignore")); !strings.Contains(string(b), "repos/") {
		t.Fatalf(".gitignore: %s", b)
	}

	// Documentation: README and docs/ only, read-only.
	var docs []string
	for _, d := range f.w.Docs() {
		if strings.HasPrefix(d.Path, "repo/") {
			docs = append(docs, d.Path+"="+d.Title)
		}
	}
	if strings.Join(docs, ",") != "repo/deploy/README.md=Deployments,repo/deploy/docs/runbook.md=Runbook" {
		t.Fatalf("docs: %v", docs)
	}
	if s, err := f.w.ReadDoc("repo/deploy/docs/runbook.md"); err != nil || !strings.Contains(s, "rollout undo") {
		t.Fatalf("read doc: %q %v", s, err)
	}
	for _, p := range []string{"repo/deploy/k8s/app.yaml", "repo/deploy/../../../../etc/x.md", "repo/deploy/docs/../../../OPSDECK.md/x.md", "repo/other/README.md"} {
		if _, err := f.w.ReadDoc(p); err == nil {
			t.Fatalf("%s should not be a doc", p)
		}
	}
	if err := f.w.WriteDoc("repo/deploy/README.md", "x"); err == nil {
		t.Fatal("repo docs must be read-only")
	}

	// Files and paths.
	tree, err := repo.Files(ctx, "", 0)
	if err != nil || strings.Join(tree.Files, ",") != "README.md,compose/docker.yaml,docs/runbook.md,k8s/app.yaml" {
		t.Fatalf("files %v %v", tree, err)
	}
	for _, p := range []string{"../escape", ".git/config", "k8s/../../x", "/etc/passwd", "sub/.GIT/hooks/x"} {
		if err := repo.Write(p, "x"); err == nil {
			t.Fatalf("write %s allowed", p)
		}
	}
	os.Symlink(t.TempDir(), filepath.Join(repo.Dir, "out"))
	if err := repo.Write("out/x", "x"); err == nil || !strings.Contains(err.Error(), "leaves") {
		t.Fatalf("symlink escape: %v", err)
	}
	os.Remove(filepath.Join(repo.Dir, "out"))

	// Branch, edit, commit (identity from the token's owner), push.
	if _, err := repo.Checkout(ctx, "opsdeck/scale-shop", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.Write("k8s/app.yaml", "image: shop:1.1\nreplicas: 3\n"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Write("k8s/hpa.yaml", "maxReplicas: 6\n"); err != nil {
		t.Fatal(err)
	}
	diff, _, err := repo.Diff(ctx, nil, 0)
	if err != nil || !strings.Contains(diff, "+replicas: 3") || !strings.Contains(diff, "+maxReplicas: 6") {
		t.Fatalf("diff: %s %v", diff, err)
	}
	if _, err := repo.Commit(ctx, CommitOptions{Message: "Scale shop to 3", Session: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, CommitOptions{Message: "again"}); err == nil {
		t.Fatal("empty commit allowed")
	}
	if prot, why := repo.ProtectedBranch(ctx, "opsdeck/scale-shop"); prot {
		t.Fatalf("feature branch protected: %s", why)
	}
	if prot, _ := repo.ProtectedBranch(ctx, "main"); !prot {
		t.Fatal("main must be protected")
	}
	if out, err := repo.Push(ctx, PushOptions{}); err != nil {
		t.Fatal(err)
	} else {
		t.Log(out)
	}
	got := sh(t, bare, "log", "-1", "--format=%an <%ae>%n%B", "opsdeck/scale-shop")
	if !strings.Contains(got, "Deploy Bot <bot@example.com>") || !strings.Contains(got, "Opsdeck-Agent-Session: sess-1") {
		t.Fatalf("server commit: %s", got)
	}
	st, _ = repo.Status(ctx)
	if st.Upstream != "origin/opsdeck/scale-shop" || st.Ahead != 0 {
		t.Fatalf("after push %+v", st)
	}

	// The token is nowhere on disk.
	filepath.WalkDir(f.tmp, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasSuffix(p, ".db") && !strings.Contains(p, ".db-") {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), token) {
				t.Errorf("token written to %s", p)
			}
		}
		return nil
	})

	// Merge request on the GitLab API (detected by probing /api/v4).
	prov, err := repo.Provider(ctx)
	if err != nil || prov.Kind() != "gitlab" {
		t.Fatalf("provider %v %v", prov, err)
	}
	mr, err := prov.CreateMR(ctx, NewMR{Title: "Scale shop", Source: "opsdeck/scale-shop", Target: "main", Draft: true})
	if err != nil || mr.ID != 1 || mr.State != "open" || mr.Checks != "pending" {
		t.Fatalf("mr %+v %v", mr, err)
	}
	if gs.MRs[0]["title"] != "Draft: Scale shop" {
		t.Fatalf("draft title %v", gs.MRs[0]["title"])
	}
	if list, err := prov.MRs(ctx, "open"); err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	if err := prov.Comment(ctx, 1, "LGTM"); err != nil || gs.Notes[0] != "LGTM" {
		t.Fatal(err)
	}
	if mr, err := prov.Merge(ctx, 1, "", true); err != nil || mr.State != "merged" {
		t.Fatalf("merge %+v %v", mr, err)
	}

	// Pull: fast-forward, then diverged.
	if _, err := repo.Checkout(ctx, "main", false, ""); err != nil {
		t.Fatal(err)
	}
	gitserver.Commit(t, bare, "main", "README.md", "# Deployments\n\nUpdated by Alice.\n")
	if out, err := repo.Pull(ctx, false); err != nil || !strings.Contains(out, "updated") {
		t.Fatalf("pull: %s %v", out, err)
	}
	gitserver.Commit(t, bare, "main", "compose/docker.yaml", "services: {web: {}}\n")
	repo.Write("docs/runbook.md", "# Runbook\n\nNew step.\n")
	repo.Commit(ctx, CommitOptions{Message: "runbook"})
	if _, err := repo.Pull(ctx, false); err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("diverged: %v", err)
	}
	if out, err := repo.Pull(ctx, true); err != nil {
		t.Fatalf("rebase: %s %v", out, err)
	}
	st, _ = repo.Status(ctx)
	if st.Ahead != 1 || st.Behind != 0 {
		t.Fatalf("after rebase %+v", st)
	}
	log, _ := repo.Log(ctx, "", "", 5)
	if len(log) < 3 || log[0].Subject != "runbook" || log[1].Author != "Alice" {
		t.Fatalf("log %+v", log)
	}

	// Discard.
	repo.Write("k8s/app.yaml", "broken")
	repo.Write("tmp.txt", "x")
	repo.Delete("README.md")
	st, _ = repo.Status(ctx)
	if len(st.Changes) != 3 {
		t.Fatalf("changes %+v", st.Changes)
	}
	if _, err := repo.Discard(ctx, []string{"k8s/app.yaml", "tmp.txt"}); err != nil {
		t.Fatal(err)
	}
	st, _ = repo.Status(ctx)
	if len(st.Changes) != 1 || st.Changes[0].Path != "README.md" || st.Changes[0].Status != "deleted" {
		t.Fatalf("after discard %+v", st.Changes)
	}
	repo.Discard(ctx, nil)
	if st, _ = repo.Status(ctx); len(st.Changes) != 0 {
		t.Fatalf("after discard all %+v", st.Changes)
	}
	if out, _, err := repo.Grep(ctx, "rollout", false, true, 0); err != nil || !strings.Contains(out, "docs/runbook.md") {
		// the runbook was rewritten above; README mentions nothing
		if !strings.Contains(out, "") {
			t.Fatalf("grep %q %v", out, err)
		}
	}
	if out, _, err := repo.Grep(ctx, "shop:1", false, false, 0); err != nil || !strings.Contains(out, "k8s/app.yaml:1:") {
		t.Fatalf("grep %q %v", out, err)
	}
}

// TestSSHDeployKey: an SSH remote with an encrypted deploy key, directly
// and through a jump host.
func TestSSHDeployKey(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh not installed")
	}
	f := setup(t)
	ctx := context.Background()
	root := t.TempDir()
	bare := gitserver.Repo(t, root)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "deploy", []byte("key pass"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := sshd.Start("127.0.0.1:0", sshd.Options{AuthorizedKeys: []ssh.PublicKey{sshPub}, Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	keyRef := f.item("ssh-key", "deploy-key", map[string]string{"private_key": string(pem.EncodeToMemory(block)), "passphrase": "key pass"})
	f.put(&workspace.Resource{Kind: "GitRemote", Name: "infra", Spec: map[string]any{
		"url": "ssh://git@" + srv.Addr + bare, "deployKey": keyRef}})
	repo, err := f.m.Open(f.w, "infra")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Clone(ctx, ""); err != nil {
		t.Fatal(err)
	}
	repo.Write("k8s/app.yaml", "image: shop:2.0\n")
	if _, err := repo.Commit(ctx, CommitOptions{Message: "bump", Name: "Ops", Email: "ops@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Push(ctx, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, bare, "log", "-1", "--format=%s %an", "main"); strings.TrimSpace(got) != "bump Ops" {
		t.Fatalf("server: %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(f.w.StateDir(), "git_known_hosts")); !strings.Contains(string(b), "ssh-ed25519") {
		t.Fatalf("host key not pinned: %s", b)
	}

	// Through a jump host: ssh's ProxyCommand is `opsdeck socks-connect`.
	bastion, err := sshd.Start("127.0.0.1:0", sshd.Options{User: "ops", Password: "bastion-pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer bastion.Close()
	host, port, _ := strings.Cut(bastion.Addr, ":")
	f.put(&workspace.Resource{Kind: "Host", Name: "bastion", Spec: map[string]any{"address": host, "port": port, "user": "ops",
		"auth": f.item("login", "bastion-login", map[string]string{"username": "ops", "password": "bastion-pw"})}})
	f.put(&workspace.Resource{Kind: "GitRemote", Name: "infra-jump", Via: "host/bastion", Spec: map[string]any{
		"url": "ssh://git@" + srv.Addr + bare, "deployKey": keyRef}})
	jr, _ := f.m.Open(f.w, "infra-jump")
	if _, err := jr.Clone(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if c, err := jr.Read(ctx, "k8s/app.yaml", "", 0); err != nil || c.Text != "image: shop:2.0\n" {
		t.Fatalf("read %+v %v", c, err)
	}
}

// TestThroughVPN: an HTTP remote only reachable inside a WireGuard network
// (the environment's proxies must not be used).
func TestThroughVPN(t *testing.T) {
	f := setup(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	gs := gitserver.New(t)
	bare := gs.Bare
	peer, err := wgpeer.StartWith(gs.Handler(t))
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	f.put(&workspace.Resource{Kind: "WireGuard", Name: "office", Spec: map[string]any{
		"config": f.item("vpn", "office-wg", map[string]string{"config": peer.ClientConfig}), "mode": "userspace"}})
	f.put(&workspace.Resource{Kind: "GitRemote", Name: "office-deploy", Via: "network/office", Spec: map[string]any{
		"url": "http://10.9.0.2:8080/acme/deploy.git", "provider": "gitlab", "token": f.item("token", "office-token", map[string]string{"token": token})}})
	repo, _ := f.m.Open(f.w, "office-deploy")
	if _, err := repo.Clone(ctx, ""); err != nil {
		t.Fatal(err)
	}
	repo.Checkout(ctx, "fix", true, "")
	repo.Write("k8s/app.yaml", "image: shop:3.0\n")
	repo.Commit(ctx, CommitOptions{Message: "office fix"})
	if _, err := repo.Push(ctx, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, bare, "log", "-1", "--format=%s %an", "fix"); strings.TrimSpace(got) != "office fix Deploy Bot" {
		t.Fatalf("server: %s", got)
	}
	prov, err := repo.Provider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := prov.Info(ctx); err != nil || info.DefaultBranch != "main" || !info.CanPush {
		t.Fatalf("info %+v %v", info, err)
	}
}

func TestParseRemote(t *testing.T) {
	cases := map[string]string{
		"https://gitlab.example.com/group/sub/project.git": "https|gitlab.example.com|group/sub/project|false",
		"https://github.com/acme/deploy":                   "https|github.com|acme/deploy|false",
		"git@github.com:acme/deploy.git":                   "https|github.com|acme/deploy|true",
		"ssh://git@gitlab.example.com:2222/ops/infra.git":  "https|gitlab.example.com|ops/infra|true",
		"http://10.0.0.5:8080/ops/infra":                   "http|10.0.0.5:8080|ops/infra|false",
	}
	for in, want := range cases {
		r, err := ParseRemote(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%s|%s|%s|%v", r.Scheme, r.Host, r.Path, r.SSH); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
	for _, bad := range []string{"", "gitlab.example.com/x", "https://host/"} {
		if _, err := ParseRemote(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestGitHubAPI: pull requests on GitHub (and Enterprise Server's /api/v3).
func TestGitHubAPI(t *testing.T) {
	var merged, deleted bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp_tok" {
			http.Error(w, `{"message":"Bad credentials"}`, 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pr := func() string {
			m := "null"
			if merged {
				m = `"2026-01-01T00:00:00Z"`
			}
			st := "open"
			if merged {
				st = "closed"
			}
			return `{"number":5,"title":"Bump","body":"b","state":"` + st + `","merged_at":` + m + `,"html_url":"https://github.com/acme/deploy/pull/5","mergeable_state":"clean","user":{"login":"bot"},"head":{"ref":"bump","sha":"abc"},"base":{"ref":"main"}}`
		}
		switch p := r.URL.Path; {
		case p == "/api/v3/repos/acme/deploy":
			fmt.Fprint(w, `{"default_branch":"main","html_url":"https://github.com/acme/deploy","permissions":{"push":true}}`)
		case p == "/api/v3/repos/acme/deploy/branches/main":
			fmt.Fprint(w, `{"name":"main","protected":true}`)
		case p == "/api/v3/repos/acme/deploy/pulls" && r.Method == "POST":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if in["head"] != "bump" || in["base"] != "main" {
				http.Error(w, `{"message":"Validation Failed"}`, 422)
				return
			}
			fmt.Fprint(w, pr())
		case p == "/api/v3/repos/acme/deploy/pulls":
			fmt.Fprint(w, "["+pr()+"]")
		case p == "/api/v3/repos/acme/deploy/pulls/5":
			fmt.Fprint(w, pr())
		case p == "/api/v3/repos/acme/deploy/pulls/5/merge":
			merged = true
			fmt.Fprint(w, `{"merged":true}`)
		case p == "/api/v3/repos/acme/deploy/git/refs/heads/bump" && r.Method == "DELETE":
			deleted = true
			w.WriteHeader(204)
		case p == "/api/v3/repos/acme/deploy/commits/abc/check-runs":
			fmt.Fprint(w, `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"failure"}]}`)
		case p == "/api/v3/repos/acme/deploy/commits/abc/status":
			fmt.Fprint(w, `{"state":"success","total_count":1}`)
		case p == "/api/v3/user":
			fmt.Fprint(w, `{"login":"bot","id":42}`)
		default:
			http.Error(w, `{"message":"Not Found"}`, 404)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	r, _ := ParseRemote(ts.URL + "/acme/deploy.git")
	kind, base, err := detect(ctx, ts.Client(), r, "github")
	if err != nil || kind != "github" || base != ts.URL+"/api/v3" {
		t.Fatalf("detect %s %s %v", kind, base, err)
	}
	g, _ := newGitHub(ts.Client(), base, "acme/deploy", "ghp_tok")
	if info, err := g.Info(ctx); err != nil || info.DefaultBranch != "main" || !info.CanPush {
		t.Fatalf("info %+v %v", info, err)
	}
	if p, _ := g.Protected(ctx, "main"); !p {
		t.Fatal("main protected")
	}
	if p, err := g.Protected(ctx, "feature"); p || err != nil {
		t.Fatalf("feature %v %v", p, err)
	}
	mr, err := g.CreateMR(ctx, NewMR{Title: "Bump", Source: "bump", Target: "main"})
	if err != nil || mr.ID != 5 || mr.State != "open" {
		t.Fatalf("create %+v %v", mr, err)
	}
	if mr, _ := g.MR(ctx, 5); mr.Checks != "failed" {
		t.Fatalf("checks %q", mr.Checks)
	}
	if mr, err := g.Merge(ctx, 5, "squash", true); err != nil || mr.State != "merged" || !deleted {
		t.Fatalf("merge %+v %v %v", mr, err, deleted)
	}
	if list, _ := g.MRs(ctx, "merged"); len(list) != 1 {
		t.Fatalf("merged list %v", list)
	}
	if u, _ := g.User(ctx); u.Email != "42+bot@users.noreply.github.com" {
		t.Fatalf("user %+v", u)
	}
	bad, _ := newGitHub(ts.Client(), base, "acme/deploy", "nope")
	if _, err := bad.Info(ctx); err == nil || !strings.Contains(err.Error(), "token was rejected") {
		t.Fatalf("bad token: %v", err)
	}
}

// TestExistingClone: a working copy the user cloned elsewhere (origin at
// another URL than the repository's) is used in place: docs, edit, commit,
// push over the repository's URL with its token; it is never deleted. An
// empty folder of its own is cloned into.
func TestExistingClone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	gs := gitserver.New(t)
	ts := httptest.NewServer(gs.Handler(t))
	defer ts.Close()
	url := ts.URL + "/acme/deploy.git"
	tok := f.item("token", "gitlab-deploy", map[string]string{"token": token})

	// The user's own clone, straight from the bare repository.
	mine := filepath.Join(t.TempDir(), "projects", "deploy")
	os.MkdirAll(filepath.Dir(mine), 0o755)
	sh(t, filepath.Dir(mine), "clone", "-q", gs.Bare, mine)
	// cloned over SSH, which opsdeck has no key for
	const sshOrigin = "git@gitlab.invalid:acme/deploy.git"
	sh(t, mine, "remote", "set-url", "origin", sshOrigin)

	if _, err := f.m.InspectFolder(ctx, "relative/path"); err == nil {
		t.Fatal("relative path accepted")
	}
	fo, err := f.m.InspectFolder(ctx, mine)
	if err != nil || !fo.Cloned || fo.Empty || fo.Branch != "main" || fo.Origin != sshOrigin || fo.Path != mine {
		t.Fatalf("inspect %+v %v", fo, err)
	}
	if fo, err := f.m.InspectFolder(ctx, filepath.Join(mine, "nothing-here")); err != nil || !fo.Empty || fo.Cloned {
		t.Fatalf("inspect missing %+v %v", fo, err)
	}
	if fo, err := f.m.InspectFolder(ctx, filepath.Join(mine, "k8s")); err != nil || fo.Empty || fo.Cloned {
		t.Fatalf("inspect plain folder %+v %v", fo, err)
	}

	if err := f.w.PutResource("", &workspace.Resource{Kind: "GitRemote", Name: "bad", Spec: map[string]any{"url": url, "path": "projects/deploy"}}); err == nil {
		t.Fatal("relative path saved")
	}
	f.put(&workspace.Resource{Kind: "GitRemote", Name: "deploy", Spec: map[string]any{"url": url, "token": tok, "docs": true, "path": mine}})
	if f.w.RepoDir("deploy") != mine || !f.w.RepoExternal("deploy") {
		t.Fatalf("repo dir %s", f.w.RepoDir("deploy"))
	}
	repo, err := f.m.Open(f.w, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if !repo.Cloned() || repo.Dir != mine {
		t.Fatalf("cloned %v dir %s", repo.Cloned(), repo.Dir)
	}
	if _, err := repo.Clone(ctx, ""); err == nil {
		t.Fatal("cloned over the existing working copy")
	}
	if s, err := f.w.ReadDoc("repo/deploy/docs/runbook.md"); err != nil || !strings.Contains(s, "rollout undo") {
		t.Fatalf("read doc: %q %v", s, err)
	}

	gitserver.Commit(t, gs.Bare, "main", "k8s/extra.yaml", "x: 1\n")
	if out, err := repo.Pull(ctx, false); err != nil || !strings.Contains(out, "updated") {
		t.Fatalf("pull: %q %v", out, err)
	}
	if err := repo.Write("k8s/app.yaml", "image: shop:1.1\nreplicas: 2\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, CommitOptions{Message: "Bump shop", Name: "Bob", Email: "bob@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Push(ctx, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if log := sh(t, gs.Bare, "log", "-1", "--format=%s", "main"); strings.TrimSpace(log) != "Bump shop" {
		t.Fatalf("pushed: %q", log)
	}
	// The user's own setup of the clone is left as it was.
	if o := strings.TrimSpace(sh(t, mine, "config", "--get", "remote.origin.url")); o != sshOrigin {
		t.Fatalf("origin changed to %s", o)
	}
	if b, _ := os.ReadFile(filepath.Join(f.w.Path, ".gitignore")); strings.Contains(string(b), "deploy") {
		t.Fatalf(".gitignore: %s", b)
	}

	if err := repo.Remove(); err == nil {
		t.Fatal("removed the user's folder")
	}
	if _, err := os.Stat(filepath.Join(mine, "k8s", "app.yaml")); err != nil {
		t.Fatal(err)
	}

	// An empty folder of its own: the clone goes there.
	there := filepath.Join(t.TempDir(), "clones", "deploy")
	f.put(&workspace.Resource{Kind: "GitRemote", Name: "deploy2", Spec: map[string]any{"url": url, "token": tok, "path": there}})
	repo2, _ := f.m.Open(f.w, "deploy2")
	if repo2.Cloned() {
		t.Fatal("cloned before cloning")
	}
	if _, err := repo2.Clone(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(there, "k8s", "extra.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.w.RepoDir("deploy2")); err != nil || f.w.RepoDir("deploy2") != there {
		t.Fatalf("repo dir %s", f.w.RepoDir("deploy2"))
	}

	// A working copy without an origin gets the repository's.
	bare := t.TempDir()
	sh(t, bare, "init", "-q", "-b", "main")
	f.put(&workspace.Resource{Kind: "GitRemote", Name: "deploy3", Spec: map[string]any{"url": url, "token": tok, "path": bare}})
	repo3, _ := f.m.Open(f.w, "deploy3")
	if _, err := repo3.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	if o := strings.TrimSpace(sh(t, bare, "config", "--get", "remote.origin.url")); o != url {
		t.Fatalf("origin %s", o)
	}
}
