// Package gitserver is a Git hosting stand-in for tests: repositories served
// over smart HTTP by git-http-backend behind token authentication, and a
// small fake of the GitLab REST API (projects, branches, merge requests).
package gitserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Token is the access token the server accepts (HTTP Basic password for
// git, PRIVATE-TOKEN for the API).
const Token = "glpat-s3cret-token-value"

// Git runs git in dir as "someone else" (Alice), outside opsdeck.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com", "GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// Repo creates <root>/acme/deploy.git with deployment files on main and
// returns its path.
func Repo(t testing.TB, root string) string {
	src := t.TempDir()
	Git(t, src, "init", "-q", "-b", "main")
	files := map[string]string{
		"README.md":           "# Deployments\n\nManifests for the shop.\n",
		"docs/runbook.md":     "# Runbook\n\nRoll back with `kubectl rollout undo`.\n",
		"k8s/app.yaml":        "image: shop:1.0\nreplicas: 2\n",
		"compose/docker.yaml": "services: {}\n",
	}
	for p, c := range files {
		os.MkdirAll(filepath.Join(src, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(src, p), []byte(c), 0o644)
	}
	Git(t, src, "add", "-A")
	Git(t, src, "commit", "-q", "-m", "initial")
	bare := filepath.Join(root, "acme", "deploy.git")
	Git(t, root, "clone", "-q", "--bare", src, bare)
	Git(t, bare, "config", "http.receivepack", "true")
	return bare
}

// Commit pushes a commit to branch of the bare repository from another
// clone.
func Commit(t testing.TB, bare, branch, file, content string) {
	c := t.TempDir()
	Git(t, c, "clone", "-q", "-b", branch, bare, ".")
	os.MkdirAll(filepath.Dir(filepath.Join(c, file)), 0o755)
	os.WriteFile(filepath.Join(c, file), []byte(content), 0o644)
	Git(t, c, "add", "-A")
	Git(t, c, "commit", "-q", "-m", "change "+file)
	Git(t, c, "push", "-q", "origin", branch)
}

// Server serves the repositories under Root and the GitLab API for
// acme/deploy.
type Server struct {
	Root string
	Bare string // acme/deploy.git

	mu    sync.Mutex
	MRs   []map[string]any
	Notes []string

	// OAuth device sign-in (GitLab application "app1"): polls before the
	// user approves, refreshes seen.
	Polls, Refreshes int
	// TokenTTL is the lifetime (seconds) of OAuth access tokens.
	TokenTTL int
}

// New makes a root with acme/deploy.git.
func New(t testing.TB) *Server {
	root := t.TempDir()
	return &Server{Root: root, Bare: Repo(t, root)}
}

func (g *Server) Handler(t testing.TB) http.Handler {
	backend := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(Git(t, g.Root, "--exec-path")), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + g.Root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v4/") {
			g.api(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/oauth/") {
			g.oauth(w, r)
			return
		}
		if _, pass, ok := r.BasicAuth(); !ok || pass != Token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "HTTP Basic: Access denied", 401)
			return
		}
		backend.ServeHTTP(w, r)
	})
}

// Lock guards MRs and Notes for readers.
func (g *Server) Lock()   { g.mu.Lock() }
func (g *Server) Unlock() { g.mu.Unlock() }

func (g *Server) api(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	if path == "/api/v4/version" {
		w.WriteHeader(401) // GitLab answers 401 without a token
		return
	}
	if r.Header.Get("PRIVATE-TOKEN") != Token {
		http.Error(w, `{"message":"401 Unauthorized"}`, 401)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	proj := "/api/v4/projects/acme%2Fdeploy"
	mr := func(id string) map[string]any {
		for _, m := range g.MRs {
			if fmt.Sprint(m["iid"]) == id {
				return m
			}
		}
		return nil
	}
	switch rest, isMR := strings.CutPrefix(path, proj+"/merge_requests/"); {
	case path == "/api/v4/user":
		fmt.Fprint(w, `{"username":"deploy-bot","name":"Deploy Bot","email":"bot@example.com"}`)
	case path == "/api/v4/projects":
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base := scheme + "://" + r.Host
		list := []map[string]any{
			{"path_with_namespace": "acme/deploy", "name": "deploy", "description": "Kubernetes manifests", "http_url_to_repo": base + "/acme/deploy.git",
				"ssh_url_to_repo": "git@" + r.Host + ":acme/deploy.git", "web_url": base + "/acme/deploy", "default_branch": "main", "visibility": "private",
				"last_activity_at": "2026-10-22T09:00:00Z", "permissions": map[string]any{"project_access": map[string]any{"access_level": 40}}},
			{"path_with_namespace": "acme/website", "name": "website", "description": "Marketing site", "http_url_to_repo": base + "/acme/website.git",
				"web_url": base + "/acme/website", "default_branch": "main", "visibility": "internal", "last_activity_at": "2026-10-01T09:00:00Z",
				"permissions": map[string]any{"project_access": map[string]any{"access_level": 20}}},
		}
		if q := r.URL.Query().Get("search"); q != "" {
			out := []map[string]any{}
			for _, p := range list {
				if strings.Contains(p["path_with_namespace"].(string), q) {
					out = append(out, p)
				}
			}
			list = out
		}
		json.NewEncoder(w).Encode(list)
	case path == proj:
		fmt.Fprint(w, `{"default_branch":"main","web_url":"https://gitlab.example/acme/deploy","permissions":{"project_access":{"access_level":40}}}`)
	case path == proj+"/repository/branches/main":
		fmt.Fprint(w, `{"name":"main","protected":true}`)
	case strings.HasPrefix(path, proj+"/repository/branches/"):
		http.Error(w, `{"message":"404 Branch Not Found"}`, 404)
	case path == proj+"/merge_requests" && r.Method == "POST":
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		id := len(g.MRs) + 1
		m := map[string]any{"iid": id, "title": in["title"], "description": in["description"], "state": "opened",
			"source_branch": in["source_branch"], "target_branch": in["target_branch"],
			"web_url": fmt.Sprintf("https://gitlab.example/acme/deploy/-/merge_requests/%d", id), "created_at": "2026-10-22T10:00:00Z", "updated_at": "2026-10-22T10:00:00Z",
			"detailed_merge_status": "mergeable", "author": map[string]any{"username": "deploy-bot"}, "head_pipeline": map[string]any{"status": "running"}}
		g.MRs = append(g.MRs, m)
		json.NewEncoder(w).Encode(m)
	case path == proj+"/merge_requests":
		out := []map[string]any{}
		for _, m := range g.MRs {
			if s := r.URL.Query().Get("state"); s == "all" || m["state"] == s {
				out = append(out, m)
			}
		}
		json.NewEncoder(w).Encode(out)
	case isMR && strings.HasSuffix(rest, "/notes") && mr(strings.TrimSuffix(rest, "/notes")) != nil:
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		g.Notes = append(g.Notes, in["body"])
		fmt.Fprint(w, `{"id":7}`)
	case isMR && strings.HasSuffix(rest, "/merge") && r.Method == "PUT" && mr(strings.TrimSuffix(rest, "/merge")) != nil:
		m := mr(strings.TrimSuffix(rest, "/merge"))
		m["state"] = "merged"
		// Merge for real, so pulling the target shows the change.
		g.merge(m["source_branch"].(string), m["target_branch"].(string))
		json.NewEncoder(w).Encode(m)
	case isMR && mr(rest) != nil:
		json.NewEncoder(w).Encode(mr(rest))
	default:
		http.Error(w, `{"message":"404 Not Found"}`, 404)
	}
}

func (g *Server) merge(src, dst string) {
	dir, err := os.MkdirTemp("", "gitserver-merge-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=GitLab", "GIT_AUTHOR_EMAIL=gitlab@example.com", "GIT_COMMITTER_NAME=GitLab", "GIT_COMMITTER_EMAIL=gitlab@example.com")
		cmd.Run()
	}
	run("clone", "-q", "-b", dst, g.Bare, ".")
	run("merge", "-q", "--no-ff", "-m", "Merge branch '"+src+"'", "origin/"+src)
	run("push", "-q", "origin", dst)
}

func (g *Server) oauth(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	w.Header().Set("Content-Type", "application/json")
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.PostForm.Get("client_id") != "app1" {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"invalid_client","error_description":"unknown application"}`)
		return
	}
	ttl := g.TokenTTL
	if ttl == 0 {
		ttl = 7200
	}
	switch r.URL.Path {
	case "/oauth/authorize_device":
		if !strings.Contains(r.PostForm.Get("scope"), "api") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_scope"}`)
			return
		}
		fmt.Fprint(w, `{"device_code":"dc-1","user_code":"WDJB-MJHT","verification_uri":"https://`+r.Host+`/oauth/device","verification_uri_complete":"https://`+r.Host+`/oauth/device?user_code=WDJB-MJHT","expires_in":300,"interval":1}`)
	case "/oauth/token":
		switch r.PostForm.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			g.Polls++
			if g.Polls < 2 {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"authorization_pending"}`)
				return
			}
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"refresh-1","expires_in":%d,"token_type":"Bearer"}`, Token, ttl)
		case "refresh_token":
			if !strings.HasPrefix(r.PostForm.Get("refresh_token"), "refresh-") {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			g.Refreshes++
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"refresh-%d","expires_in":7200}`, Token, g.Refreshes+1)
		default:
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"unsupported_grant_type"}`)
		}
	default:
		http.NotFound(w, r)
	}
}
