package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/config"
	"opsdeck/internal/gitx"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// Git connectors: GitHub / GitLab accounts connected once on this machine.
// Workspaces then add repositories by picking them from the account.

func (s *Server) connectorRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/git/connections", u(s.connList))
	mux.HandleFunc("POST /api/git/connections", u(s.connCreate))
	mux.HandleFunc("POST /api/git/connections/options", u(s.connOptions))
	mux.HandleFunc("POST /api/git/connections/{id}/test", u(s.connTest))
	mux.HandleFunc("DELETE /api/git/connections/{id}", u(s.connDelete))
	mux.HandleFunc("POST /api/git/oauth", u(s.oauthStart))
	mux.HandleFunc("GET /api/git/oauth/{flow}", u(s.oauthStatus))
	mux.HandleFunc("DELETE /api/git/oauth/{flow}", u(s.oauthCancel))
	mux.HandleFunc("GET /api/workspaces/{ws}/git/connections/{id}/repos", u(s.connRepos))
}

// ConnInfo is a connector as the UI sees it (never the token).
type ConnInfo struct {
	config.GitConnection
	Host      string `json:"host"`
	TokenPage string `json:"tokenPage"`
}

func connInfo(c config.GitConnection) ConnInfo {
	return ConnInfo{GitConnection: c, Host: gitx.ConnHost(c), TokenPage: gitx.TokenPage(c)}
}

func (s *Server) connList(w http.ResponseWriter, r *http.Request) {
	out := []ConnInfo{}
	for _, c := range s.cfg.GitConnections() {
		out = append(out, connInfo(c))
	}
	writeJSON(w, 200, out)
}

// connInput is what the UI sends to connect an account.
type connInput struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	APIURL   string `json:"apiUrl"`
	CACert   string `json:"caCert"`
	Method   string `json:"method"` // token | cli
	Token    string `json:"token"`
	ClientID string `json:"clientId"`
}

// normalize checks the provider and reduces the URL to the server's root.
func (in *connInput) conn() (config.GitConnection, error) {
	c := config.GitConnection{Provider: in.Provider, APIURL: strings.TrimSpace(in.APIURL), CACert: strings.TrimSpace(in.CACert), OAuthClientID: strings.TrimSpace(in.ClientID)}
	raw := strings.TrimSpace(in.URL)
	switch in.Provider {
	case "github":
		if raw == "" {
			raw = "https://github.com"
		}
	case "gitlab":
		if raw == "" {
			raw = "https://gitlab.com"
		}
	default:
		return c, errors.New("provider must be github or gitlab")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return c, fmt.Errorf("invalid server URL %q", in.URL)
	}
	c.URL = strings.ToLower(u.Scheme + "://" + u.Host)
	if c.Provider == "github" && c.OAuthClientID == "" && gitx.ConnHost(c) == "github.com" {
		c.OAuthClientID = gitx.GitHubClientID
	}
	return c, nil
}

var idClean = regexp.MustCompile(`[^a-z0-9]+`)

// connID reuses the ID of a connector for the same server, else derives
// one from the host.
func (s *Server) connID(c config.GitConnection) string {
	existing := s.cfg.GitConnections()
	for _, x := range existing {
		if x.Provider == c.Provider && x.URL == c.URL {
			return x.ID
		}
	}
	host := gitx.ConnHost(c)
	base := c.Provider
	if host != "github.com" && host != "gitlab.com" {
		label := strings.Split(strings.Split(host, ":")[0], ".")[0]
		if label == c.Provider {
			parts := strings.Split(host, ".")
			if len(parts) > 1 {
				label = parts[1]
			}
		}
		base = strings.Trim(c.Provider+"-"+idClean.ReplaceAllString(label, "-"), "-")
	}
	id := base
	for n := 2; ; n++ {
		taken := false
		for _, x := range existing {
			taken = taken || x.ID == id
		}
		if !taken {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

// saveConn checks the token against the account and stores the connector.
func (s *Server) saveConn(ctx context.Context, c config.GitConnection, tok *gitx.OAuthToken) (config.GitConnection, error) {
	acct, err := gitx.NewAccount(c, tok.AccessToken, nil)
	if err != nil {
		return c, err
	}
	u, err := acct.User(ctx)
	if err != nil {
		return c, fmt.Errorf("could not sign in to %s: %w", c.URL, err)
	}
	c.ID = s.connID(c)
	c.Login, c.Name = u.Login, u.Name
	if c.Created.IsZero() {
		c.Created = time.Now().UTC()
	}
	name := "git-" + c.ID
	item := gitx.TokenItem(name, c, tok)
	items, err := s.vault.List(vault.GlobalScope)
	if err != nil {
		return c, err
	}
	var saved vault.Item
	for _, it := range items {
		if it.Name == name {
			item.Policy = it.Policy // keep what the user chose
			saved, err = s.vault.Update(it.ID, item, nil)
			break
		}
	}
	if saved.ID == "" && err == nil {
		saved, err = s.vault.Create(item)
	}
	if err != nil {
		return c, err
	}
	c.Credential = saved.Ref()
	if err := s.cfg.PutGitConnection(c); err != nil {
		return c, err
	}
	return c, nil
}

func (s *Server) connOptions(w http.ResponseWriter, r *http.Request) {
	var in connInput
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	c, err := in.conn()
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"url": c.URL, "host": gitx.ConnHost(c), "tokenPage": gitx.TokenPage(c), "clientId": c.OAuthClientID}
	if c.Provider == "github" {
		out["oauthAppPage"] = c.URL + "/settings/applications/new"
	} else {
		out["oauthAppPage"] = c.URL + "/-/user_settings/applications"
	}
	tool, _, cliErr := gitx.CLIToken(r.Context(), c)
	cli := map[string]any{"tool": tool, "ready": cliErr == nil}
	if cliErr != nil {
		cli["reason"] = cliErr.Error()
	}
	out["cli"] = cli
	writeJSON(w, 200, out)
}

func (s *Server) connCreate(w http.ResponseWriter, r *http.Request) {
	var in connInput
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	c, err := in.conn()
	if err != nil {
		fail(w, err)
		return
	}
	token := strings.TrimSpace(in.Token)
	c.Method = "token"
	if in.Method == "cli" {
		tool, t, err := gitx.CLIToken(r.Context(), c)
		if err != nil {
			fail(w, err)
			return
		}
		c.Method, token = tool, t
	}
	if token == "" {
		fail(w, errors.New("a token is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c, err = s.saveConn(ctx, c, &gitx.OAuthToken{AccessToken: token})
	s.record(r, "", "git.connect", c.URL, map[string]any{"method": c.Method, "login": c.Login}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, connInfo(c))
}

func (s *Server) findConn(id string) (config.GitConnection, bool) {
	for _, c := range s.cfg.GitConnections() {
		if c.ID == id {
			return c, true
		}
	}
	return config.GitConnection{}, false
}

func (s *Server) connTest(w http.ResponseWriter, r *http.Request) {
	c, ok := s.findConn(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "connector not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := map[string]any{"ok": false}
	token, err := s.git.ConnSecret(ctx, c, nil)
	if err == nil {
		var acct *gitx.Account
		if acct, err = gitx.NewAccount(c, token, nil); err == nil {
			var u *gitx.User
			if u, err = acct.User(ctx); err == nil {
				res["ok"], res["login"], res["name"] = true, u.Login, u.Name
			}
		}
	}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, 200, res)
}

func (s *Server) connDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := s.findConn(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "connector not found")
		return
	}
	if it, _, err := s.vault.Resolve(c.Credential); err == nil && it.Name == "git-"+c.ID {
		s.vault.Delete(it.ID)
	}
	err := s.cfg.DeleteGitConnection(c.ID)
	s.record(r, "", "git.disconnect", c.URL, nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// connRepos lists the account's repositories from a workspace, so a server
// behind one of its VPNs can be reached (via).
func (s *Server) connRepos(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	c, ok := s.findConn(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "connector not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	token, err := s.git.ConnSecret(ctx, c, ws)
	if err != nil {
		fail(w, err)
		return
	}
	var hc *http.Client
	if via := r.URL.Query().Get("via"); via != "" {
		dial, err := s.hub.Dialer(ctx, ws, &workspace.Resource{Kind: "GitRemote", Name: "connector-" + c.ID, Via: via})
		if err != nil {
			fail(w, err)
			return
		}
		hc, err = gitx.ViaClient(c, dial)
		if err != nil {
			fail(w, err)
			return
		}
	}
	acct, err := gitx.NewAccount(c, token, hc)
	if err == nil {
		var list []gitx.RemoteRepo
		if list, err = acct.Repos(ctx, r.URL.Query().Get("q")); err == nil {
			if list == nil {
				list = []gitx.RemoteRepo{}
			}
			writeJSON(w, 200, list)
			return
		}
	}
	fail(w, err)
}

// ----------------------------------------------------------------------------
// OAuth device sign-in

type oauthFlow struct {
	mu     sync.Mutex
	status string // pending | done | error
	err    string
	conn   *ConnInfo
	cancel context.CancelFunc
}

var (
	flowsMu sync.Mutex
	flows   = map[string]*oauthFlow{}
)

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	var in connInput
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	c, err := in.conn()
	if err != nil {
		fail(w, err)
		return
	}
	if c.OAuthClientID == "" {
		fail(w, errors.New("signing in with the browser needs an OAuth application ID (see Advanced), or connect with a token"))
		return
	}
	c.Method = "oauth"
	hc, err := gitx.ConnHTTPClient(c)
	if err != nil {
		fail(w, err)
		return
	}
	dc, err := gitx.StartDevice(r.Context(), hc, c)
	if err != nil {
		fail(w, err)
		return
	}
	b := make([]byte, 12)
	rand.Read(b)
	id := hex.EncodeToString(b)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(dc.ExpiresIn+30)*time.Second)
	f := &oauthFlow{status: "pending", cancel: cancel}
	flowsMu.Lock()
	flows[id] = f
	flowsMu.Unlock()
	go func() {
		defer cancel()
		tok, err := gitx.PollDevice(ctx, hc, c, dc)
		var saved config.GitConnection
		if err == nil {
			saved, err = s.saveConn(ctx, c, tok)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if err != nil {
			f.status, f.err = "error", err.Error()
		} else {
			info := connInfo(saved)
			f.status, f.conn = "done", &info
		}
		s.record(r, "", "git.connect", c.URL, map[string]any{"method": "oauth", "login": saved.Login}, err)
		time.AfterFunc(10*time.Minute, func() {
			flowsMu.Lock()
			delete(flows, id)
			flowsMu.Unlock()
		})
	}()
	writeJSON(w, 200, map[string]any{"flow": id, "userCode": dc.UserCode, "verificationUri": dc.VerificationURI,
		"verificationUriComplete": dc.VerificationURIComplete, "expiresIn": dc.ExpiresIn})
}

func (s *Server) oauthStatus(w http.ResponseWriter, r *http.Request) {
	flowsMu.Lock()
	f := flows[r.PathValue("flow")]
	flowsMu.Unlock()
	if f == nil {
		writeErr(w, 404, "sign-in not found (expired)")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON(w, 200, map[string]any{"status": f.status, "error": f.err, "connection": f.conn})
}

func (s *Server) oauthCancel(w http.ResponseWriter, r *http.Request) {
	flowsMu.Lock()
	f := flows[r.PathValue("flow")]
	delete(flows, r.PathValue("flow"))
	flowsMu.Unlock()
	if f != nil {
		f.cancel()
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
