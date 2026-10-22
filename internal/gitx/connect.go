package gitx

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/config"
	"opsdeck/internal/connect"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// Connectors: a GitHub or GitLab account connected once on this machine
// (config.GitConnection, token in the vault). Repositories of any workspace
// use it when they name it (spec.connection) or when their URL is on its
// host, so a repository needs nothing but its URL.

// GitHubClientID is the OAuth app used for "Sign in with GitHub" on
// github.com when the connector does not name its own (set at build time
// with -ldflags "-X opsdeck/internal/gitx.GitHubClientID=…").
var GitHubClientID = ""

// ConnHost is the host[:port] of a connector.
func ConnHost(c config.GitConnection) string {
	u, err := url.Parse(c.URL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// APIBase is the REST API root of a connector.
func APIBase(c config.GitConnection) string {
	if c.APIURL != "" {
		return strings.TrimSuffix(c.APIURL, "/")
	}
	root := strings.TrimSuffix(c.URL, "/")
	switch {
	case c.Provider == "github" && ConnHost(c) == "github.com":
		return "https://api.github.com"
	case c.Provider == "github":
		return root + "/api/v3"
	}
	return root + "/api/v4"
}

// connection finds the connector of a repository: the one it names, else
// the one whose host serves its URL.
func (m *Manager) connection(r *workspace.Resource, rem Remote) (*config.GitConnection, error) {
	if m.Connections == nil {
		return nil, nil
	}
	conns := m.Connections()
	if id := r.Str("connection"); id != "" {
		for i := range conns {
			if conns[i].ID == id {
				return &conns[i], nil
			}
		}
		return nil, fmt.Errorf("connector %q is not set up on this computer (add it under Connectors)", id)
	}
	host := strings.ToLower(rem.Host)
	if rem.SSH {
		host = strings.ToLower(rem.SSHHost)
	}
	for i := range conns {
		ch := ConnHost(conns[i])
		if ch == host || (rem.SSH && strings.Split(ch, ":")[0] == host) {
			return &conns[i], nil
		}
	}
	return nil, nil
}

func connClient(c config.GitConnection) (*http.Client, error) {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 20 * time.Second, ForceAttemptHTTP2: true}
	if ca := strings.TrimSpace(c.CACert); ca != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, errors.New("the connector's CA certificate is not valid PEM")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}, nil
}

// basicUser is the user name git sends with a token.
func basicUser(provider string) string {
	if provider == "github" {
		return "x-access-token"
	}
	return "oauth2"
}

var refreshMu sync.Mutex

// ConnSecret returns a connector's token, refreshing an expiring OAuth
// token first. ws scopes the vault lookup (and the agent policy) when the
// token is used from a workspace; "" for the connector page itself.
func (m *Manager) ConnSecret(ctx context.Context, c config.GitConnection, w *workspace.Workspace) (string, error) {
	var it vault.Item
	var field string
	var err error
	if w != nil {
		it, field, err = m.Hub.Credential(ctx, w, c.Credential)
	} else {
		it, field, err = m.Vault.Resolve(c.Credential)
	}
	if err != nil {
		return "", err
	}
	_, secret := Secret(it, field)
	if c.Method != "oauth" || it.Get("refresh_token") == "" {
		return secret, nil
	}
	exp, _ := time.Parse(time.RFC3339, it.Get("expires"))
	if exp.IsZero() || time.Until(exp) > 5*time.Minute {
		return secret, nil
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	// Another caller may have refreshed meanwhile.
	if cur, _, err := m.Vault.Resolve(c.Credential); err == nil {
		if e, _ := time.Parse(time.RFC3339, cur.Get("expires")); time.Until(e) > 5*time.Minute {
			return cur.Get("token"), nil
		}
		it = cur
	}
	hc, err := connClient(c)
	if err != nil {
		return "", err
	}
	tok, err := RefreshToken(ctx, hc, c, it.Get("refresh_token"))
	if err != nil {
		return "", fmt.Errorf("connector %s: the sign-in expired and could not be renewed (%v): sign in again under Connectors", c.ID, err)
	}
	if _, err := m.Vault.Update(it.ID, TokenItem(it.Name, c, tok), nil); err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// TokenItem is the vault item that stores a connector's token.
func TokenItem(name string, c config.GitConnection, t *OAuthToken) vault.Item {
	it := vault.Item{Scope: vault.GlobalScope, Type: "token", Name: name,
		Notes:  "Git connector " + c.ID + " (" + c.URL + "). Used by opsdeck for clone, pull, push and merge requests.",
		Tags:   []string{"git", c.Provider},
		Policy: vault.Policy{AgentUse: true},
		Fields: []vault.Field{{Name: "token", Value: t.AccessToken, Secret: true}, {Name: "url", Value: c.URL}}}
	if t.RefreshToken != "" {
		it.Fields = append(it.Fields, vault.Field{Name: "refresh_token", Value: t.RefreshToken, Secret: true})
	}
	if !t.Expires.IsZero() {
		it.Fields = append(it.Fields, vault.Field{Name: "expires", Value: t.Expires.UTC().Format(time.RFC3339)})
	}
	return it
}

// ----------------------------------------------------------------------------
// Accounts: who the token belongs to and which repositories it can see.

// RemoteRepo is a repository of a connected account.
type RemoteRepo struct {
	FullName      string    `json:"fullName"` // group/sub/project
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	HTTPURL       string    `json:"httpUrl"`
	SSHURL        string    `json:"sshUrl"`
	WebURL        string    `json:"webUrl"`
	DefaultBranch string    `json:"defaultBranch"`
	Private       bool      `json:"private"`
	Archived      bool      `json:"archived"`
	CanPush       bool      `json:"canPush"`
	Updated       time.Time `json:"updated"`
}

// Account talks to a connector's API.
type Account struct {
	c    config.GitConnection
	api  api
	kind string
}

// NewAccount builds an API client for c with token (hc nil = direct, with
// the connector's CA).
func NewAccount(c config.GitConnection, token string, hc *http.Client) (*Account, error) {
	if hc == nil {
		var err error
		if hc, err = connClient(c); err != nil {
			return nil, err
		}
	}
	a := &Account{c: c, kind: c.Provider}
	switch c.Provider {
	case "github":
		g, _ := newGitHub(hc, APIBase(c), "x/x", token)
		a.api = g.api
	case "gitlab":
		a.api = newGitLab(hc, APIBase(c), "x", token).api
	default:
		return nil, fmt.Errorf("unknown provider %q", c.Provider)
	}
	return a, nil
}

func (a *Account) User(ctx context.Context) (*User, error) {
	if a.kind == "github" {
		return (&github{api: a.api}).User(ctx)
	}
	return (&gitlab{api: a.api}).User(ctx)
}

// Repos lists repositories the account can access, most recently active
// first, filtered by query (substring of the full name or description).
func (a *Account) Repos(ctx context.Context, query string) ([]RemoteRepo, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []RemoteRepo
	if a.kind == "github" {
		for page := 1; page <= 5; page++ {
			var list []struct {
				FullName      string    `json:"full_name"`
				Name          string    `json:"name"`
				Description   string    `json:"description"`
				CloneURL      string    `json:"clone_url"`
				SSHURL        string    `json:"ssh_url"`
				HTMLURL       string    `json:"html_url"`
				DefaultBranch string    `json:"default_branch"`
				Private       bool      `json:"private"`
				Archived      bool      `json:"archived"`
				PushedAt      time.Time `json:"pushed_at"`
				Permissions   struct {
					Push bool `json:"push"`
				} `json:"permissions"`
			}
			err := a.api.do(ctx, "GET", "/user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member&page="+strconv.Itoa(page), nil, &list)
			if err != nil {
				return nil, err
			}
			for _, r := range list {
				out = append(out, RemoteRepo{FullName: r.FullName, Name: r.Name, Description: r.Description, HTTPURL: r.CloneURL, SSHURL: r.SSHURL,
					WebURL: r.HTMLURL, DefaultBranch: r.DefaultBranch, Private: r.Private, Archived: r.Archived, CanPush: r.Permissions.Push, Updated: r.PushedAt})
			}
			if len(list) < 100 {
				break
			}
		}
	} else {
		path := "/projects?membership=true&per_page=100&order_by=last_activity_at&archived=false"
		if q != "" {
			path += "&search=" + url.QueryEscape(q) + "&search_namespaces=true"
		}
		var list []struct {
			PathWithNamespace string    `json:"path_with_namespace"`
			Name              string    `json:"name"`
			Description       string    `json:"description"`
			HTTPURL           string    `json:"http_url_to_repo"`
			SSHURL            string    `json:"ssh_url_to_repo"`
			WebURL            string    `json:"web_url"`
			DefaultBranch     string    `json:"default_branch"`
			Visibility        string    `json:"visibility"`
			Archived          bool      `json:"archived"`
			LastActivity      time.Time `json:"last_activity_at"`
			Permissions       struct {
				Project *struct {
					Access int `json:"access_level"`
				} `json:"project_access"`
				Group *struct {
					Access int `json:"access_level"`
				} `json:"group_access"`
			} `json:"permissions"`
		}
		if err := a.api.do(ctx, "GET", path, nil, &list); err != nil {
			return nil, err
		}
		for _, r := range list {
			lvl := 0
			if r.Permissions.Project != nil {
				lvl = r.Permissions.Project.Access
			}
			if r.Permissions.Group != nil && r.Permissions.Group.Access > lvl {
				lvl = r.Permissions.Group.Access
			}
			out = append(out, RemoteRepo{FullName: r.PathWithNamespace, Name: r.Name, Description: r.Description, HTTPURL: r.HTTPURL, SSHURL: r.SSHURL,
				WebURL: r.WebURL, DefaultBranch: r.DefaultBranch, Private: r.Visibility != "public", Archived: r.Archived, CanPush: lvl >= 30, Updated: r.LastActivity})
		}
	}
	if q != "" {
		f := out[:0]
		for _, r := range out {
			if strings.Contains(strings.ToLower(r.FullName), q) || strings.Contains(strings.ToLower(r.Description), q) {
				f = append(f, r)
			}
		}
		out = f
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// TokenPage is where the user creates a token with the right scopes
// already selected.
func TokenPage(c config.GitConnection) string {
	root := strings.TrimSuffix(c.URL, "/")
	if c.Provider == "github" {
		return root + "/settings/tokens/new?scopes=repo,read:user,user:email,workflow&description=opsdeck"
	}
	return root + "/-/user_settings/personal_access_tokens?name=opsdeck&scopes=api,read_user,write_repository"
}

// ----------------------------------------------------------------------------
// OAuth device sign-in (RFC 8628): GitHub OAuth apps and GitLab
// applications (GitLab 17.2+); no client secret needed.

// DeviceCode is the code the user enters on the provider's page.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// OAuthToken is the result of a sign-in or a refresh.
type OAuthToken struct {
	AccessToken  string
	RefreshToken string
	Expires      time.Time
}

func oauthURLs(c config.GitConnection) (device, token string) {
	root := strings.TrimSuffix(c.URL, "/")
	if c.Provider == "github" {
		return root + "/login/device/code", root + "/login/oauth/access_token"
	}
	return root + "/oauth/authorize_device", root + "/oauth/token"
}

func oauthScopes(provider string) string {
	if provider == "github" {
		return "repo read:user user:email workflow"
	}
	return "api read_user write_repository"
}

func postForm(ctx context.Context, hc *http.Client, u string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err := json.Unmarshal(b, out); err != nil {
		return res.StatusCode, fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return res.StatusCode, nil
}

// StartDevice asks for a device code.
func StartDevice(ctx context.Context, hc *http.Client, c config.GitConnection) (*DeviceCode, error) {
	if c.OAuthClientID == "" {
		return nil, errors.New("no OAuth application ID for this connector")
	}
	devURL, _ := oauthURLs(c)
	var dc DeviceCode
	var e struct {
		Error string `json:"error"`
		Desc  string `json:"error_description"`
	}
	raw := map[string]any{}
	if _, err := postForm(ctx, hc, devURL, url.Values{"client_id": {c.OAuthClientID}, "scope": {oauthScopes(c.Provider)}}, &raw); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(raw)
	json.Unmarshal(b, &dc)
	json.Unmarshal(b, &e)
	if dc.DeviceCode == "" {
		return nil, fmt.Errorf("sign-in refused: %s", firstStr(e.Desc, e.Error, "no device code"))
	}
	if dc.Interval <= 0 {
		dc.Interval = 5
	}
	if dc.ExpiresIn <= 0 {
		dc.ExpiresIn = 900
	}
	return &dc, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Desc         string `json:"error_description"`
}

func (t tokenResponse) token() *OAuthToken {
	o := &OAuthToken{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken}
	if t.ExpiresIn > 0 {
		o.Expires = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return o
}

// PollDevice waits until the user approved the code (or it expired).
func PollDevice(ctx context.Context, hc *http.Client, c config.GitConnection, dc *DeviceCode) (*OAuthToken, error) {
	_, tokURL := oauthURLs(c)
	interval := time.Duration(dc.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		var t tokenResponse
		if _, err := postForm(ctx, hc, tokURL, url.Values{"client_id": {c.OAuthClientID}, "device_code": {dc.DeviceCode},
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, &t); err != nil {
			return nil, err
		}
		switch t.Error {
		case "":
			if t.AccessToken == "" {
				return nil, errors.New("sign-in returned no token")
			}
			return t.token(), nil
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "expired_token":
			return nil, errors.New("the code expired: start the sign-in again")
		case "access_denied":
			return nil, errors.New("the sign-in was denied")
		default:
			return nil, fmt.Errorf("sign-in failed: %s", firstStr(t.Desc, t.Error))
		}
	}
	return nil, errors.New("the code expired: start the sign-in again")
}

// RefreshToken renews an expiring OAuth token.
func RefreshToken(ctx context.Context, hc *http.Client, c config.GitConnection, refresh string) (*OAuthToken, error) {
	_, tokURL := oauthURLs(c)
	var t tokenResponse
	if _, err := postForm(ctx, hc, tokURL, url.Values{"client_id": {c.OAuthClientID}, "grant_type": {"refresh_token"}, "refresh_token": {refresh}}, &t); err != nil {
		return nil, err
	}
	if t.AccessToken == "" {
		return nil, errors.New(firstStr(t.Desc, t.Error, "no token"))
	}
	o := t.token()
	if o.RefreshToken == "" {
		o.RefreshToken = refresh
	}
	return o, nil
}

// ----------------------------------------------------------------------------
// gh / glab logins

// CLIToken reads the token of an existing GitHub CLI (gh) or GitLab CLI
// (glab) login for the connector's host. Returns the tool name too.
func CLIToken(ctx context.Context, c config.GitConnection) (tool, token string, err error) {
	host := strings.Split(ConnHost(c), ":")[0]
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if c.Provider == "github" {
		tool = "gh"
		cmd = exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", host)
	} else {
		tool = "glab"
		cmd = exec.CommandContext(ctx, "glab", "config", "get", "token", "--host", host)
	}
	if _, err := exec.LookPath(tool); err != nil {
		return tool, "", fmt.Errorf("%s is not installed", tool)
	}
	out, err := cmd.Output()
	token = strings.TrimSpace(string(out))
	if err != nil || token == "" {
		return tool, "", fmt.Errorf("%s is not logged in to %s", tool, host)
	}
	return tool, token, nil
}

// ConnHTTPClient reaches a connector's server directly (with its CA).
func ConnHTTPClient(c config.GitConnection) (*http.Client, error) { return connClient(c) }

// ViaClient reaches a connector's server through a workspace network.
func ViaClient(c config.GitConnection, dial connect.DialFunc) (*http.Client, error) {
	hc, err := connClient(c)
	if err != nil {
		return nil, err
	}
	tr := hc.Transport.(*http.Transport)
	tr.Proxy = nil
	tr.DialContext = dial
	return hc, nil
}
