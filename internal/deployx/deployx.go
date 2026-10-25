// Package deployx reads and drives the deployment platforms a workspace
// uses — Portainer, Argo CD and Komodo — through their HTTP APIs. A
// platform is a DeployPlatform resource of the workspace (its URL, a token
// in the workspace's vault, reached through its via chain like any other
// resource), so each workspace has its own servers and credentials and
// self-hosted instances behind a VPN or a jump host work as they are.
//
// What a platform deploys (a Portainer stack, an Argo CD application, a
// Komodo stack or deployment) comes in one provider-neutral shape, Target,
// so the Deployments page and the agent's deploy tool work the same way
// whatever the platform.
package deployx

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// Risk classes of actions (the agent's names).
const (
	RiskRead        = "read"
	RiskChange      = "change"
	RiskDestructive = "destructive"
)

// ActionInfo describes an action a platform offers on its targets.
type ActionInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Help  string `json:"help"`
	Risk  string `json:"risk"`
	// Types are the target types the action applies to (empty = all).
	Types []string `json:"types,omitempty"`
}

// PlatformInfo describes a supported platform for the UI.
type PlatformInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// TokenHelp says how to create the credential and which rights it needs.
	TokenHelp string `json:"tokenHelp"`
	// TokenPath is the page of the platform's UI where it is created.
	TokenPath string `json:"tokenPath,omitempty"`
	// Credential is the vault item type the platform takes: token, or
	// login for an API key and secret (username and password).
	Credential string       `json:"credential"`
	Actions    []ActionInfo `json:"actions"`
}

// Platforms lists the supported platforms.
var Platforms = []PlatformInfo{
	{ID: "portainer", Label: "Portainer", Credential: "token", TokenPath: "/#!/account",
		TokenHelp: "Create an access token under My account → Access tokens. It acts as that user: the stacks of the environments the user can reach are listed, and changing them needs a user allowed to (an administrator, or a stack's owner/team).",
		Actions: []ActionInfo{
			{ID: "deploy", Label: "Redeploy", Risk: RiskChange, Help: "Git stacks: pull the repository and redeploy; other stacks: redeploy the current file. pull=true pulls the images first"},
			{ID: "update", Label: "Update file", Risk: RiskChange, Types: []string{"compose", "swarm"}, Help: "Replace the stack file (compose YAML) and redeploy; not for Git stacks"},
			{ID: "restart", Label: "Restart", Risk: RiskChange, Types: []string{"compose", "swarm"}, Help: "Restart the stack's containers (service: only that compose service)"},
			{ID: "start", Label: "Start", Risk: RiskChange, Help: "Start a stopped stack"},
			{ID: "stop", Label: "Stop", Risk: RiskChange, Help: "Stop the stack (start brings it back with the same file)"},
		}},
	{ID: "argocd", Label: "Argo CD", Credential: "token", TokenPath: "/settings/accounts",
		TokenHelp: "Generate a token for an account with the apiKey capability (Settings → Accounts, or argocd account generate-token). Its RBAC role decides what it may do: applications, get to read; sync and action to deploy.",
		Actions: []ActionInfo{
			{ID: "deploy", Label: "Sync", Risk: RiskChange, Help: "Sync the application to its target revision (revision: another branch, tag or commit for this sync). prune=true also deletes resources no longer in Git"},
			{ID: "rollback", Label: "Rollback", Risk: RiskChange, Help: "Deploy an earlier revision from the history (history: its id). Needs auto-sync to be off"},
			{ID: "refresh", Label: "Refresh", Risk: RiskRead, Help: "Compare with Git again (hard=true also clears the manifest cache)"},
			{ID: "restart", Label: "Restart", Risk: RiskChange, Help: "Rollout restart of the application's Deployments, StatefulSets and DaemonSets (service: only that one)"},
			{ID: "terminate", Label: "Terminate sync", Risk: RiskChange, Help: "Stop the running sync operation"},
		}},
	{ID: "komodo", Label: "Komodo", Credential: "login", TokenPath: "/settings",
		TokenHelp: "Create an API key under Settings → Api Keys (or on a service user) and save it as a login item: the key as username, the secret as password. The user's permissions decide what it may do: Read to list, Execute to deploy.",
		Actions: []ActionInfo{
			{ID: "deploy", Label: "Deploy", Risk: RiskChange, Help: "Stacks: docker compose up with the latest file (service: only that service); deployments: recreate the container with the configured image"},
			{ID: "pull", Label: "Pull images", Risk: RiskChange, Help: "Pull the images without redeploying"},
			{ID: "restart", Label: "Restart", Risk: RiskChange, Help: "Restart the containers"},
			{ID: "start", Label: "Start", Risk: RiskChange, Help: "Start stopped containers"},
			{ID: "stop", Label: "Stop", Risk: RiskChange, Help: "Stop the containers"},
		}},
}

// PlatformByID returns a platform's description.
func PlatformByID(id string) (PlatformInfo, bool) {
	for _, p := range Platforms {
		if p.ID == id {
			return p, true
		}
	}
	return PlatformInfo{}, false
}

// Action returns the description of one of the platform's actions.
func (p PlatformInfo) Action(id string) (ActionInfo, bool) {
	for _, a := range p.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return ActionInfo{}, false
}

// ActionIDs are the actions that apply to a target type.
func (p PlatformInfo) ActionIDs(typ string) []string {
	var out []string
	for _, a := range p.Actions {
		if len(a.Types) == 0 || contains(a.Types, typ) {
			out = append(out, a.ID)
		}
	}
	return out
}

// Health is a target's state in one word for the UI: ok, warn, err, busy
// or unknown.
const (
	HealthOK      = "ok"
	HealthWarn    = "warn"
	HealthErr     = "err"
	HealthBusy    = "busy"
	HealthUnknown = "unknown"
)

// Target is something a platform deploys: a Portainer stack, an Argo CD
// application, a Komodo stack or deployment.
type Target struct {
	// ID is the platform's id: a Portainer stack id, namespace/name of an
	// Argo CD application, stack:<id> or deployment:<id> at Komodo.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type: compose, swarm or kubernetes (Portainer), application (Argo
	// CD), stack or deployment (Komodo).
	Type string `json:"type"`
	// Where it runs: a Portainer environment, the cluster and namespace,
	// a Komodo server.
	Where string `json:"where,omitempty"`
	// Status is the platform's own word (active, Healthy, running…),
	// Health the same in one of the Health* words.
	Status string `json:"status"`
	Health string `json:"health"`
	// Sync is Argo CD's sync status (Synced, OutOfSync); OutOfSync is set
	// on every platform when what runs differs from what is configured.
	Sync      string `json:"sync,omitempty"`
	OutOfSync bool   `json:"outOfSync,omitempty"`
	// UpdateAvailable: a newer image exists for a service (Komodo).
	UpdateAvailable bool `json:"updateAvailable,omitempty"`
	// Revision is the deployed Git commit or image.
	Revision string `json:"revision,omitempty"`
	// Source is where it comes from: a repository (and path, branch), or
	// a stack file / image.
	Source  string    `json:"source,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	Message string    `json:"message,omitempty"`
	// URL opens the target in the platform's UI.
	URL string `json:"url,omitempty"`
	// Actions are the action ids that apply to it.
	Actions []string `json:"actions"`
}

// Service is a part of a target: a container of a stack, or a Kubernetes
// resource of an application.
type Service struct {
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Image     string `json:"image,omitempty"`
	// Status is the platform's word (running, exited, Synced…); Health
	// one of the Health* words.
	Status          string `json:"status"`
	Health          string `json:"health"`
	Message         string `json:"message,omitempty"`
	OutOfSync       bool   `json:"outOfSync,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable,omitempty"`
	// Container is the container id (logs of one container).
	Container string `json:"container,omitempty"`
}

// Deployment is one entry of a target's history.
type Deployment struct {
	ID       string    `json:"id"`
	Revision string    `json:"revision,omitempty"`
	Source   string    `json:"source,omitempty"`
	Time     time.Time `json:"time,omitzero"`
	Duration int64     `json:"durationMs,omitempty"`
	By       string    `json:"by,omitempty"`
	// Result: success, failed, running (or the platform's word).
	Result string `json:"result,omitempty"`
	// What was done (Komodo: the operation, e.g. DeployStack).
	Operation string `json:"operation,omitempty"`
}

// Operation is the running or last operation on a target.
type Operation struct {
	Name     string    `json:"name,omitempty"`
	Phase    string    `json:"phase"`
	Message  string    `json:"message,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
}

// Detail is a target with its parts, history and file.
type Detail struct {
	Target
	Services  []Service    `json:"services"`
	History   []Deployment `json:"history"`
	Operation *Operation   `json:"operation,omitempty"`
	// File is the stack's compose file, when the platform keeps one.
	File string `json:"file,omitempty"`
	// Git: the stack/application deploys from a repository (no file
	// to update).
	Git bool `json:"git,omitempty"`
	// AutoSync: the platform deploys changes by itself (Argo CD automated
	// sync, Komodo auto-update).
	AutoSync bool `json:"autoSync,omitempty"`
}

// Server is what the platform says about itself and the credential.
type Server struct {
	Version string `json:"version,omitempty"`
	User    string `json:"user,omitempty"`
}

// Action is a change to run on a target.
type Action struct {
	ID string `json:"action"`
	// Revision: Argo CD sync to this revision instead of the target one.
	Revision string `json:"revision,omitempty"`
	// Prune: Argo CD also deletes resources gone from Git; Portainer
	// removes services gone from the file.
	Prune bool `json:"prune,omitempty"`
	// History is the history id to roll back to.
	History string `json:"history,omitempty"`
	// Service limits the action to one service / resource.
	Service string `json:"service,omitempty"`
	// File is the new stack file (update).
	File string `json:"file,omitempty"`
	// Pull pulls images before redeploying (Portainer).
	Pull bool `json:"pull,omitempty"`
	// Hard: a hard refresh (Argo CD).
	Hard bool `json:"hard,omitempty"`
}

// Risk is the action's risk class (destructive when it deletes).
func (a Action) Risk(p PlatformInfo) string {
	if a.Prune && a.ID == "deploy" || a.Prune && a.ID == "rollback" || a.Prune && a.ID == "update" {
		return RiskDestructive
	}
	if info, ok := p.Action(a.ID); ok {
		return info.Risk
	}
	return RiskChange
}

// Result is what an action did.
type Result struct {
	Message string `json:"message"`
	// Output is the platform's log of the operation (Komodo), if any.
	Output string `json:"output,omitempty"`
	OK     bool   `json:"ok"`
}

// LogOptions select the logs of a target.
type LogOptions struct {
	Service string // a service / container / pod name (empty: all)
	Tail    int    // lines per container (default 200)
}

// Platform talks to one platform's API.
type Platform interface {
	// Verify checks the URL and the credential.
	Verify(ctx context.Context) (Server, error)
	// Targets lists what the platform deploys.
	Targets(ctx context.Context) ([]Target, error)
	// Detail describes one target.
	Detail(ctx context.Context, id string) (*Detail, error)
	// Logs returns recent log lines of a target's containers / pods.
	Logs(ctx context.Context, d *Detail, o LogOptions) (string, error)
	// Diff says what deploying would change: a unified diff, "" when
	// nothing would, ErrNoDiff when the platform cannot tell.
	Diff(ctx context.Context, d *Detail) (string, error)
	// Run performs an action.
	Run(ctx context.Context, d *Detail, a Action) (*Result, error)
}

// ErrNoDiff: the platform cannot tell what a deploy would change.
var ErrNoDiff = errors.New("this platform does not show what a deploy would change")

// Manager opens the DeployPlatform resources of workspaces.
type Manager struct {
	hub   *connect.Hub
	vault *vault.Vault
	// open builds a platform (tests replace the transport).
	open func(kind string, c *client) (Platform, error)
}

// New returns a manager reaching platforms through hub, with credentials
// from v.
func New(hub *connect.Hub, v *vault.Vault) *Manager {
	return &Manager{hub: hub, vault: v, open: openPlatform}
}

func openPlatform(id string, c *client) (Platform, error) {
	switch id {
	case "portainer":
		return &portainer{c: c}, nil
	case "argocd":
		return &argocd{c: c}, nil
	case "komodo":
		return &komodo{c: c}, nil
	}
	return nil, fmt.Errorf("unknown platform %q (portainer, argocd, komodo)", id)
}

// PlatformOf is the platform id of a DeployPlatform resource.
func PlatformOf(r *workspace.Resource) string {
	return strings.ToLower(strings.TrimSpace(r.Str("platform")))
}

// Opened is a platform of a workspace, limited to the targets that belong
// to the workspace (spec.targets).
type Opened struct {
	Platform
	Info     PlatformInfo
	Resource *workspace.Resource
	patterns []string
}

// Open connects to the platform of a DeployPlatform resource. A call made
// by the agent may only use a credential allowed for agent use.
func (m *Manager) Open(ctx context.Context, w *workspace.Workspace, r *workspace.Resource) (*Opened, error) {
	if r.Kind != "DeployPlatform" {
		return nil, fmt.Errorf("%s is not a deployment platform", r.ID())
	}
	info, ok := PlatformByID(PlatformOf(r))
	if !ok {
		return nil, fmt.Errorf("%s: unknown platform %q (portainer, argocd, komodo)", r.ID(), r.Str("platform"))
	}
	base, err := baseURL(r.Str("url"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.ID(), err)
	}
	ref := r.Str("token")
	if ref == "" {
		return nil, fmt.Errorf("%s has no credential: set token to a vault item (%s)", r.ID(), info.Credential)
	}
	it, field, err := m.vault.ResolveFor(w.Name(), ref)
	if err != nil {
		return nil, err
	}
	if a := connect.ActorFrom(ctx); a.Kind == "agent" && !it.Policy.AgentUse {
		return nil, fmt.Errorf("credential %s is not allowed for agent use", ref)
	}
	cred := credential{key: it.Get("username"), secret: it.Get("password")}
	if field != "" {
		cred.token = it.Get(field)
	} else {
		cred.token = firstNonEmpty(it.Get("token"), it.Get("password"))
	}
	if info.ID == "komodo" && (cred.key == "" || cred.secret == "") {
		return nil, fmt.Errorf("%s: Komodo needs a login item with the API key as username and the secret as password (%s)", r.ID(), ref)
	}
	if info.ID != "komodo" && cred.token == "" {
		return nil, fmt.Errorf("credential %s has no token", ref)
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: r.Bool("insecure", false)}
	if ca := strings.TrimSpace(r.Str("caCert")); ca != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, fmt.Errorf("%s: the CA certificate is not valid PEM", r.ID())
		}
		tlsCfg.RootCAs = pool
	}
	// Each connection asks the hub for the path: SSH clients and VPNs are
	// cached there and come back after a reconnect.
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d, err := m.hub.Dialer(ctx, w, r)
			if err != nil {
				return nil, err
			}
			return d(ctx, network, addr)
		},
		TLSClientConfig: tlsCfg, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 90 * time.Second,
		ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 4, IdleConnTimeout: 60 * time.Second,
	}
	c := &client{base: base, cred: cred, platform: info.ID,
		hc: &http.Client{Transport: tr, Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	p, err := m.open(info.ID, c)
	if err != nil {
		return nil, err
	}
	m.vault.Touch()
	return &Opened{Platform: p, Info: info, Resource: r, patterns: r.List("targets")}, nil
}

// Manages reports whether a target name belongs to the workspace
// (spec.targets: glob patterns; none = all).
func (o *Opened) Manages(name string) bool {
	if len(o.patterns) == 0 {
		return true
	}
	for _, p := range o.patterns {
		if ok, _ := path.Match(strings.TrimSpace(p), name); ok {
			return true
		}
	}
	return false
}

// Targets lists the workspace's targets on the platform, sorted by name.
func (o *Opened) Targets(ctx context.Context) ([]Target, error) {
	all, err := o.Platform.Targets(ctx)
	if err != nil {
		return nil, err
	}
	out := []Target{}
	for _, t := range all {
		if o.Manages(t.Name) {
			if t.Actions == nil {
				t.Actions = o.Info.ActionIDs(t.Type)
			}
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Where < out[j].Where
	})
	return out, nil
}

// Detail describes a target given by id or name; it must belong to the
// workspace.
func (o *Opened) Detail(ctx context.Context, ref string) (*Detail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("name the target (a stack or application; list them with targets)")
	}
	id := ref
	all, err := o.Platform.Targets(ctx)
	if err != nil {
		return nil, err
	}
	var byName []Target
	found := false
	for _, t := range all {
		if t.ID == ref {
			found = true
			break
		}
		if t.Name == ref {
			byName = append(byName, t)
		}
	}
	if !found {
		switch len(byName) {
		case 0:
			var names []string
			for _, t := range all {
				if o.Manages(t.Name) {
					names = append(names, t.Name)
				}
			}
			if len(names) == 0 {
				return nil, fmt.Errorf("%q not found on %s (it has no targets for this workspace)", ref, o.Resource.Name)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("%q not found on %s; targets: %s", ref, o.Resource.Name, strings.Join(names, ", "))
		case 1:
			id = byName[0].ID
		default:
			var ids []string
			for _, t := range byName {
				ids = append(ids, fmt.Sprintf("%s (%s)", t.ID, t.Where))
			}
			return nil, fmt.Errorf("%d targets are named %s; give the id of the one you mean: %s", len(byName), ref, strings.Join(ids, ", "))
		}
	}
	d, err := o.Platform.Detail(ctx, id)
	if err != nil {
		return nil, err
	}
	if !o.Manages(d.Name) {
		return nil, fmt.Errorf("%s is not one of this workspace's targets on %s (spec.targets)", d.Name, o.Resource.Name)
	}
	if d.Actions == nil {
		d.Actions = o.Info.ActionIDs(d.Type)
	}
	if d.Services == nil {
		d.Services = []Service{}
	}
	if d.History == nil {
		d.History = []Deployment{}
	}
	return d, nil
}

// Check refuses an action the target does not take, before anything is
// sent.
func (o *Opened) Check(d *Detail, a Action) error {
	if _, ok := o.Info.Action(a.ID); !ok {
		var ids []string
		for _, x := range o.Info.Actions {
			ids = append(ids, x.ID)
		}
		return fmt.Errorf("%s has no action %q (%s)", o.Info.Label, a.ID, strings.Join(ids, ", "))
	}
	if !contains(d.Actions, a.ID) {
		return fmt.Errorf("%s %s does not support %s", d.Type, d.Name, a.ID)
	}
	switch {
	case a.ID == "update" && d.Git:
		return fmt.Errorf("%s deploys from Git: change the file in the repository, then deploy", d.Name)
	case a.ID == "update" && strings.TrimSpace(a.File) == "":
		return errors.New("update needs the new stack file (file)")
	case a.ID == "update" && strings.TrimSpace(a.File) == strings.TrimSpace(d.File):
		return fmt.Errorf("the stack file of %s already has this content", d.Name)
	case a.ID == "rollback" && a.History == "":
		return errors.New("rollback needs the history id to go back to (see status)")
	case a.ID == "rollback" && d.AutoSync:
		return fmt.Errorf("%s syncs automatically: turn auto-sync off before a rollback, or revert the change in Git", d.Name)
	case a.Revision != "" && a.ID != "deploy":
		return errors.New("revision only applies to deploy")
	}
	if a.Service != "" && len(d.Services) > 0 {
		ok := false
		for _, s := range d.Services {
			ok = ok || s.Name == a.Service
		}
		if !ok {
			var names []string
			for _, s := range d.Services {
				names = append(names, s.Name)
			}
			return fmt.Errorf("%s has no service %q (%s)", d.Name, a.Service, strings.Join(names, ", "))
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// HTTP

type credential struct {
	token       string // Portainer, Argo CD
	key, secret string // Komodo
}

// client calls a platform's JSON API. Redirects are not followed, so the
// credential never goes to another server.
type client struct {
	base     *url.URL
	hc       *http.Client
	cred     credential
	platform string
}

func baseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("the URL is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("invalid URL %q", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

// link is a page of the platform's UI.
func (c *client) link(p string) string {
	return c.base.String() + p
}

// HTTPError is an error answer of the platform.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string {
	switch e.Status {
	case 401:
		return "HTTP 401: the credential was refused (expired or revoked?)" + suffix(e.Message)
	case 403:
		return "HTTP 403: the credential is not allowed to do this" + suffix(e.Message)
	}
	return fmt.Sprintf("HTTP %d%s", e.Status, suffix(e.Message))
}

func suffix(m string) string {
	if m == "" {
		return ""
	}
	return ": " + m
}

// do sends a request and decodes a JSON answer into out (nil: ignored).
func (c *client) do(ctx context.Context, method, p string, query url.Values, body, out any) error {
	raw, err := c.raw(ctx, method, p, query, body)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: unexpected answer: %v", method, p, err)
	}
	return nil
}

// raw sends a request and returns the body of a 2xx answer.
func (c *client) raw(ctx context.Context, method, p string, query url.Values, body any) ([]byte, error) {
	res, err := c.send(ctx, method, p, query, body)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, &HTTPError{Status: res.StatusCode, Message: errorMessage(b, res.Header.Get("Location"))}
	}
	return b, nil
}

// send sends a request; the caller reads and closes the body.
func (c *client) send(ctx context.Context, method, p string, query url.Values, body any) (*http.Response, error) {
	u := *c.base
	u.Path = c.base.Path + p
	u.RawQuery = query.Encode()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "opsdeck")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch c.platform {
	case "portainer":
		req.Header.Set("X-API-Key", c.cred.token)
	case "komodo":
		req.Header.Set("X-Api-Key", c.cred.key)
		req.Header.Set("X-Api-Secret", c.cred.secret)
	default:
		req.Header.Set("Authorization", "Bearer "+c.cred.token)
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.base.Host, unwrapURLError(err))
	}
	return res, nil
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// errorMessage takes the message out of an error body: the usual JSON
// shapes ({"message"}, {"error"}, {"details"}), else the start of the text.
func errorMessage(b []byte, location string) string {
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		for _, k := range []string{"message", "error", "details", "trace"} {
			if s, ok := m[k].(string); ok && s != "" {
				if k == "error" {
					if d, ok := m["details"].(string); ok && d != "" {
						return s + ": " + d
					}
				}
				return s
			}
		}
	}
	s := strings.TrimSpace(string(b))
	if location != "" && s == "" {
		return "redirected to " + location + " (check the URL)"
	}
	if strings.HasPrefix(s, "<") {
		return "an HTML page instead of the API (check the URL)"
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// ----------------------------------------------------------------------------
// helpers

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// unixTime reads a time in seconds (Portainer) or milliseconds (Komodo).
func unixTime(n int64) time.Time {
	switch {
	case n <= 0:
		return time.Time{}
	case n > 1e12:
		return time.UnixMilli(n).UTC()
	}
	return time.Unix(n, 0).UTC()
}

// short is a commit hash cut to 8 characters.
func short(rev string) string {
	if len(rev) == 40 && strings.Trim(rev, "0123456789abcdef") == "" {
		return rev[:8]
	}
	return rev
}

// Format is a one-line description of a target, for the agent.
func Format(t Target) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] %s", t.Name, t.Type, t.Status)
	if t.Sync != "" {
		fmt.Fprintf(&b, ", %s", t.Sync)
	} else if t.OutOfSync {
		b.WriteString(", out of sync")
	}
	if t.UpdateAvailable {
		b.WriteString(", image update available")
	}
	if t.Where != "" {
		fmt.Fprintf(&b, " on %s", t.Where)
	}
	if t.Revision != "" {
		fmt.Fprintf(&b, " rev %s", short(t.Revision))
	}
	if t.Source != "" {
		fmt.Fprintf(&b, " from %s", t.Source)
	}
	if !t.Updated.IsZero() {
		fmt.Fprintf(&b, " (updated %s)", t.Updated.Format(time.RFC3339))
	}
	if t.Message != "" {
		fmt.Fprintf(&b, " — %s", oneLine(t.Message, 200))
	}
	return b.String()
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
