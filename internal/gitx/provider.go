package gitx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Remote is a parsed Git remote URL.
type Remote struct {
	URL     string // as configured
	SSH     bool
	Scheme  string // https | http (for SSH remotes: the web scheme, https)
	Host    string // host[:port] of the web/API server
	SSHHost string // host of an SSH remote
	Path    string // group/sub/project (no .git)
}

// Web is the repository's web URL.
func (r Remote) Web() string { return r.Scheme + "://" + r.Host + "/" + r.Path }

// Origin is scheme://host[:port]/ of an HTTP remote (config scoping).
func (r Remote) Origin() string { return r.Scheme + "://" + r.Host + "/" }

// ParseRemote understands https://host/group/project(.git),
// ssh://git@host[:port]/group/project.git and git@host:group/project.git.
func ParseRemote(raw string) (Remote, error) {
	s := strings.TrimSpace(raw)
	r := Remote{URL: s}
	switch {
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "ssh://"), strings.HasPrefix(s, "file://"):
		u, err := url.Parse(s)
		if err != nil {
			return r, fmt.Errorf("invalid repository URL %q: %v", raw, err)
		}
		r.Path = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		switch u.Scheme {
		case "ssh":
			r.SSH, r.Scheme, r.Host, r.SSHHost = true, "https", u.Hostname(), u.Hostname()
		case "file":
			r.Scheme, r.Host = "file", ""
		default:
			r.Scheme, r.Host = u.Scheme, u.Host
		}
	case strings.Contains(s, "@") && strings.Contains(s, ":"):
		// scp-like: git@host:group/project.git
		at := strings.Index(s, "@")
		colon := strings.Index(s[at:], ":") + at
		r.SSH, r.Scheme = true, "https"
		r.Host = s[at+1 : colon]
		r.SSHHost = r.Host
		r.Path = strings.TrimSuffix(strings.Trim(s[colon+1:], "/"), ".git")
	default:
		return r, fmt.Errorf("unsupported repository URL %q (use https://…, ssh://… or git@host:group/project.git)", raw)
	}
	if r.Path == "" {
		return r, fmt.Errorf("repository URL %q has no project path", raw)
	}
	return r, nil
}

// MR is a GitHub pull request or a GitLab merge request.
type MR struct {
	ID          int       `json:"id"` // PR number / MR iid
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"` // open | merged | closed
	Draft       bool      `json:"draft"`
	Source      string    `json:"source"`
	Target      string    `json:"target"`
	Author      string    `json:"author"`
	URL         string    `json:"url"`
	Mergeable   string    `json:"mergeable,omitempty"` // provider's merge status
	Checks      string    `json:"checks,omitempty"`    // CI: success | pending | failed | ""
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

// NewMR is a merge request to open.
type NewMR struct {
	Title, Description, Source, Target string
	Draft, RemoveSource                bool
}

// Info describes the repository on the server.
type Info struct {
	DefaultBranch string `json:"defaultBranch"`
	WebURL        string `json:"webUrl"`
	CanPush       bool   `json:"canPush"`
}

// User is the token's owner (commit identity fallback).
type User struct {
	Login, Name, Email string
}

// Provider is a Git hosting API.
type Provider interface {
	Kind() string // github | gitlab
	Info(ctx context.Context) (*Info, error)
	Protected(ctx context.Context, branch string) (bool, error)
	MRs(ctx context.Context, state string) ([]MR, error)
	MR(ctx context.Context, id int) (*MR, error)
	CreateMR(ctx context.Context, in NewMR) (*MR, error)
	Comment(ctx context.Context, id int, body string) error
	Merge(ctx context.Context, id int, method string, removeSource bool) (*MR, error)
	User(ctx context.Context) (*User, error)
}

// api is a small JSON client.
type api struct {
	c      *http.Client
	base   string
	header func(h http.Header)
}

// APIError is a non-2xx answer.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string {
	hint := ""
	switch e.Status {
	case 401:
		hint = " — the access token was rejected (expired or wrong)"
	case 403:
		hint = " — the token lacks permission (GitLab: api + write_repository scopes; GitHub: Contents and Pull requests read/write)"
	case 404:
		hint = " — not found, or the token cannot see this repository"
	}
	return fmt.Sprintf("HTTP %d: %s%s", e.Status, e.Msg, hint)
}

func (a *api) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "opsdeck")
	a.header(req.Header)
	res, err := a.c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Message any    `json:"message"`
			Error   string `json:"error"`
		}
		json.Unmarshal(data, &e)
		msg := strings.TrimSpace(fmt.Sprint(firstNonNil(e.Message, e.Error)))
		if msg == "" || msg == "<nil>" {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		return &APIError{Status: res.StatusCode, Msg: msg}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func firstNonNil(v ...any) any {
	for _, x := range v {
		if x != nil && x != "" {
			return x
		}
	}
	return nil
}

func isStatus(err error, code int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == code
}

// ---------------------------------------------------------------------------
// GitHub (github.com and GitHub Enterprise Server)

type github struct {
	api
	owner, repo string
}

func newGitHub(c *http.Client, base, path, token string) (*github, error) {
	owner, repo, ok := strings.Cut(path, "/")
	if !ok || strings.Contains(repo, "/") {
		return nil, fmt.Errorf("GitHub repository path must be owner/repo, not %q", path)
	}
	return &github{api: api{c: c, base: strings.TrimSuffix(base, "/"), header: func(h http.Header) {
		h.Set("Accept", "application/vnd.github+json")
		h.Set("X-GitHub-Api-Version", "2022-11-28")
		if token != "" {
			h.Set("Authorization", "Bearer "+token)
		}
	}}, owner: owner, repo: repo}, nil
}

func (g *github) Kind() string { return "github" }

func (g *github) p(s string) string {
	return "/repos/" + url.PathEscape(g.owner) + "/" + url.PathEscape(g.repo) + s
}

func (g *github) Info(ctx context.Context) (*Info, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
		HTMLURL       string `json:"html_url"`
		Permissions   struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := g.do(ctx, "GET", g.p(""), nil, &r); err != nil {
		return nil, err
	}
	return &Info{DefaultBranch: r.DefaultBranch, WebURL: r.HTMLURL, CanPush: r.Permissions.Push}, nil
}

func (g *github) Protected(ctx context.Context, branch string) (bool, error) {
	var r struct {
		Protected bool `json:"protected"`
	}
	err := g.do(ctx, "GET", g.p("/branches/"+url.PathEscape(branch)), nil, &r)
	if isStatus(err, 404) {
		return false, nil
	}
	return r.Protected, err
}

type ghPR struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	HTMLURL   string    `json:"html_url"`
	MergedAt  *string   `json:"merged_at"`
	Mergeable *bool     `json:"mergeable"`
	MState    string    `json:"mergeable_state"`
	Created   time.Time `json:"created_at"`
	Updated   time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p ghPR) mr() MR {
	st := p.State
	if p.MergedAt != nil && *p.MergedAt != "" {
		st = "merged"
	}
	m := MR{ID: p.Number, Title: p.Title, Description: p.Body, State: st, Draft: p.Draft, Source: p.Head.Ref, Target: p.Base.Ref,
		Author: p.User.Login, URL: p.HTMLURL, Mergeable: p.MState, Created: p.Created, Updated: p.Updated}
	return m
}

func (g *github) MRs(ctx context.Context, state string) ([]MR, error) {
	s := map[string]string{"": "open", "open": "open", "opened": "open", "closed": "closed", "merged": "closed", "all": "all"}[state]
	var list []ghPR
	if err := g.do(ctx, "GET", g.p("/pulls?per_page=50&state="+s), nil, &list); err != nil {
		return nil, err
	}
	out := []MR{}
	for _, p := range list {
		m := p.mr()
		if state == "merged" && m.State != "merged" {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func (g *github) MR(ctx context.Context, id int) (*MR, error) {
	var p ghPR
	if err := g.do(ctx, "GET", g.p("/pulls/"+strconv.Itoa(id)), nil, &p); err != nil {
		return nil, err
	}
	m := p.mr()
	m.Checks = g.checks(ctx, p.Head.SHA)
	return &m, nil
}

// checks summarises check runs and commit statuses of a commit.
func (g *github) checks(ctx context.Context, sha string) string {
	if sha == "" {
		return ""
	}
	var runs struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	var status struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
	}
	g.do(ctx, "GET", g.p("/commits/"+sha+"/check-runs"), nil, &runs)
	g.do(ctx, "GET", g.p("/commits/"+sha+"/status"), nil, &status)
	n, pending, failed := 0, false, false
	for _, r := range runs.CheckRuns {
		n++
		switch {
		case r.Status != "completed":
			pending = true
		case r.Conclusion != "success" && r.Conclusion != "skipped" && r.Conclusion != "neutral":
			failed = true
		}
	}
	if status.TotalCount > 0 {
		n++
		switch status.State {
		case "pending":
			pending = true
		case "failure", "error":
			failed = true
		}
	}
	switch {
	case n == 0:
		return ""
	case failed:
		return "failed"
	case pending:
		return "pending"
	}
	return "success"
}

func (g *github) CreateMR(ctx context.Context, in NewMR) (*MR, error) {
	var p ghPR
	err := g.do(ctx, "POST", g.p("/pulls"), map[string]any{"title": in.Title, "head": in.Source, "base": in.Target, "body": in.Description, "draft": in.Draft}, &p)
	if err != nil {
		return nil, err
	}
	m := p.mr()
	return &m, nil
}

func (g *github) Comment(ctx context.Context, id int, body string) error {
	return g.do(ctx, "POST", g.p("/issues/"+strconv.Itoa(id)+"/comments"), map[string]string{"body": body}, nil)
}

func (g *github) Merge(ctx context.Context, id int, method string, removeSource bool) (*MR, error) {
	if method == "" {
		method = "merge"
	}
	pr, err := g.MR(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := g.do(ctx, "PUT", g.p("/pulls/"+strconv.Itoa(id)+"/merge"), map[string]string{"merge_method": method}, nil); err != nil {
		return nil, err
	}
	if removeSource {
		g.do(ctx, "DELETE", g.p("/git/refs/heads/"+pr.Source), nil, nil)
	}
	return g.MR(ctx, id)
}

func (g *github) User(ctx context.Context) (*User, error) {
	var u struct {
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
		ID    int    `json:"id"`
	}
	if err := g.do(ctx, "GET", "/user", nil, &u); err != nil {
		return nil, err
	}
	email := u.Email
	if email == "" {
		email = fmt.Sprintf("%d+%s@users.noreply.github.com", u.ID, u.Login)
	}
	return &User{Login: u.Login, Name: firstStr(u.Name, u.Login), Email: email}, nil
}

// ---------------------------------------------------------------------------
// GitLab (gitlab.com and self-hosted)

type gitlab struct {
	api
	project string // URL-encoded project path
}

func newGitLab(c *http.Client, base, path, token string) *gitlab {
	return &gitlab{api: api{c: c, base: strings.TrimSuffix(base, "/"), header: func(h http.Header) {
		if token != "" {
			h.Set("PRIVATE-TOKEN", token)
		}
	}}, project: url.PathEscape(path)}
}

func (g *gitlab) Kind() string { return "gitlab" }

func (g *gitlab) p(s string) string { return "/projects/" + g.project + s }

func (g *gitlab) Info(ctx context.Context) (*Info, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
		WebURL        string `json:"web_url"`
		Permissions   struct {
			Project *struct {
				Access int `json:"access_level"`
			} `json:"project_access"`
			Group *struct {
				Access int `json:"access_level"`
			} `json:"group_access"`
		} `json:"permissions"`
	}
	if err := g.do(ctx, "GET", g.p(""), nil, &r); err != nil {
		return nil, err
	}
	lvl := 0
	if r.Permissions.Project != nil {
		lvl = r.Permissions.Project.Access
	}
	if r.Permissions.Group != nil && r.Permissions.Group.Access > lvl {
		lvl = r.Permissions.Group.Access
	}
	return &Info{DefaultBranch: r.DefaultBranch, WebURL: r.WebURL, CanPush: lvl >= 30}, nil
}

func (g *gitlab) Protected(ctx context.Context, branch string) (bool, error) {
	var r struct {
		Protected bool `json:"protected"`
	}
	err := g.do(ctx, "GET", g.p("/repository/branches/"+url.PathEscape(branch)), nil, &r)
	if isStatus(err, 404) {
		return false, nil
	}
	return r.Protected, err
}

type glMR struct {
	IID          int       `json:"iid"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	State        string    `json:"state"`
	Draft        bool      `json:"draft"`
	Source       string    `json:"source_branch"`
	Target       string    `json:"target_branch"`
	WebURL       string    `json:"web_url"`
	MergeStatus  string    `json:"detailed_merge_status"`
	OldStatus    string    `json:"merge_status"`
	Created      time.Time `json:"created_at"`
	Updated      time.Time `json:"updated_at"`
	HeadPipeline *struct {
		Status string `json:"status"`
	} `json:"head_pipeline"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
}

func (p glMR) mr() MR {
	st := map[string]string{"opened": "open", "merged": "merged", "closed": "closed", "locked": "open"}[p.State]
	m := MR{ID: p.IID, Title: p.Title, Description: p.Description, State: firstStr(st, p.State), Draft: p.Draft, Source: p.Source, Target: p.Target,
		Author: p.Author.Username, URL: p.WebURL, Mergeable: firstStr(p.MergeStatus, p.OldStatus), Created: p.Created, Updated: p.Updated}
	if p.HeadPipeline != nil {
		m.Checks = map[string]string{"success": "success", "failed": "failed", "canceled": "failed", "running": "pending", "pending": "pending", "created": "pending"}[p.HeadPipeline.Status]
	}
	return m
}

func (g *gitlab) MRs(ctx context.Context, state string) ([]MR, error) {
	s := map[string]string{"": "opened", "open": "opened", "opened": "opened", "closed": "closed", "merged": "merged", "all": "all"}[state]
	var list []glMR
	if err := g.do(ctx, "GET", g.p("/merge_requests?per_page=50&state="+s), nil, &list); err != nil {
		return nil, err
	}
	out := []MR{}
	for _, p := range list {
		out = append(out, p.mr())
	}
	return out, nil
}

func (g *gitlab) MR(ctx context.Context, id int) (*MR, error) {
	var p glMR
	if err := g.do(ctx, "GET", g.p("/merge_requests/"+strconv.Itoa(id)), nil, &p); err != nil {
		return nil, err
	}
	m := p.mr()
	return &m, nil
}

func (g *gitlab) CreateMR(ctx context.Context, in NewMR) (*MR, error) {
	title := in.Title
	if in.Draft && !strings.HasPrefix(strings.ToLower(title), "draft:") {
		title = "Draft: " + title
	}
	var p glMR
	err := g.do(ctx, "POST", g.p("/merge_requests"), map[string]any{"title": title, "source_branch": in.Source, "target_branch": in.Target,
		"description": in.Description, "remove_source_branch": in.RemoveSource}, &p)
	if err != nil {
		return nil, err
	}
	m := p.mr()
	return &m, nil
}

func (g *gitlab) Comment(ctx context.Context, id int, body string) error {
	return g.do(ctx, "POST", g.p("/merge_requests/"+strconv.Itoa(id)+"/notes"), map[string]string{"body": body}, nil)
}

func (g *gitlab) Merge(ctx context.Context, id int, method string, removeSource bool) (*MR, error) {
	body := map[string]any{"should_remove_source_branch": removeSource}
	if method == "squash" {
		body["squash"] = true
	}
	var p glMR
	if err := g.do(ctx, "PUT", g.p("/merge_requests/"+strconv.Itoa(id)+"/merge"), body, &p); err != nil {
		return nil, err
	}
	m := p.mr()
	return &m, nil
}

func (g *gitlab) User(ctx context.Context) (*User, error) {
	var u struct {
		Username    string `json:"username"`
		Name        string `json:"name"`
		Email       string `json:"email"`
		CommitEmail string `json:"commit_email"`
		PublicEmail string `json:"public_email"`
	}
	if err := g.do(ctx, "GET", "/user", nil, &u); err != nil {
		return nil, err
	}
	return &User{Login: u.Username, Name: firstStr(u.Name, u.Username), Email: firstStr(u.CommitEmail, u.Email, u.PublicEmail)}, nil
}

func firstStr(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// detect guesses the hosting software of a remote: explicit setting, the
// well-known hosts, then the servers' API endpoints.
func detect(ctx context.Context, c *http.Client, r Remote, setting string) (kind, base string, err error) {
	origin := r.Scheme + "://" + r.Host
	switch setting {
	case "github":
		if r.Host == "github.com" {
			return "github", "https://api.github.com", nil
		}
		return "github", origin + "/api/v3", nil
	case "gitlab":
		return "gitlab", origin + "/api/v4", nil
	}
	host := strings.ToLower(r.Host)
	switch {
	case host == "github.com" || host == "www.github.com":
		return "github", "https://api.github.com", nil
	case strings.Contains(host, "gitlab"):
		return "gitlab", origin + "/api/v4", nil
	}
	probe := func(path string) int {
		req, _ := http.NewRequestWithContext(ctx, "GET", origin+path, nil)
		res, err := c.Do(req)
		if err != nil {
			return 0
		}
		res.Body.Close()
		return res.StatusCode
	}
	if s := probe("/api/v4/version"); s == 200 || s == 401 {
		return "gitlab", origin + "/api/v4", nil
	}
	if s := probe("/api/v3/meta"); s == 200 {
		return "github", origin + "/api/v3", nil
	}
	return "", "", fmt.Errorf("cannot tell whether %s is GitLab or GitHub: set the repository's provider", r.Host)
}
