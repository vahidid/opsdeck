package deployx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pmezard/go-difflib/difflib"
)

// Komodo (v1 and v2): stacks and deployments. The API is RPC over POST:
// /read, /write, /execute and /auth with {"type": <request>, "params":
// {…}}. An execute call returns an Update at once and runs on; it is polled
// with GetUpdate until Complete. Auth: X-Api-Key and X-Api-Secret.
type komodo struct {
	c *client

	mu      sync.Mutex
	servers map[string]string // id → name
	// poll is the interval between GetUpdate calls (tests shorten it).
	poll time.Duration
}

// call sends one request to an RPC endpoint.
func (k *komodo) call(ctx context.Context, endpoint, typ string, params any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	err := k.c.do(ctx, "POST", "/"+endpoint, nil, map[string]any{"type": typ, "params": params}, out)
	if err != nil {
		return fmt.Errorf("%s: %w", typ, err)
	}
	return nil
}

func (k *komodo) Verify(ctx context.Context) (Server, error) {
	var s Server
	var v struct {
		Version string `json:"version"`
	}
	if err := k.call(ctx, "read", "GetVersion", nil, &v); err != nil {
		return s, err
	}
	s.Version = v.Version
	var u struct {
		Username   string `json:"username"`
		Admin      bool   `json:"admin"`
		SuperAdmin bool   `json:"super_admin"`
	}
	// v1: POST /auth GetUser; v2: GET /user
	if err := k.call(ctx, "auth", "GetUser", nil, &u); err != nil {
		if err2 := k.c.do(ctx, "GET", "/user", nil, nil, &u); err2 != nil {
			return s, err
		}
	}
	s.User = u.Username
	if u.Admin || u.SuperAdmin {
		s.User += " (admin)"
	}
	return s, nil
}

type kmListItem struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags"`
	Info struct {
		State           string `json:"state"`
		Status          string `json:"status"`
		ServerID        string `json:"server_id"`
		ServerName      string `json:"server_name"`
		Image           string `json:"image"`
		UpdateAvailable bool   `json:"update_available"`
		Repo            string `json:"repo"`
		Branch          string `json:"branch"`
		RepoLink        string `json:"repo_link"`
		FilesOnHost     bool   `json:"files_on_host"`
		DeployedHash    string `json:"deployed_hash"`
		LatestHash      string `json:"latest_hash"`
		Services        []struct {
			Service         string `json:"service"`
			Image           string `json:"image"`
			UpdateAvailable bool   `json:"update_available"`
		} `json:"services"`
	} `json:"info"`
}

// serverName names a server (listed once).
func (k *komodo) serverName(ctx context.Context, id, name string) string {
	if name != "" || id == "" {
		return name
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.servers == nil {
		var list []kmListItem
		if k.call(ctx, "read", "ListServers", map[string]any{}, &list) == nil {
			k.servers = map[string]string{}
			for _, s := range list {
				k.servers[s.ID] = s.Name
			}
		}
	}
	return firstNonEmpty(k.servers[id], id)
}

func komodoHealth(state string) string {
	switch strings.ToLower(state) {
	case "running":
		return HealthOK
	case "deploying", "restarting", "removing", "created":
		return HealthBusy
	case "unhealthy", "dead":
		return HealthErr
	case "stopped", "paused", "down", "exited", "not_deployed":
		return HealthWarn
	}
	return HealthUnknown
}

func (k *komodo) target(ctx context.Context, typ string, it kmListItem) Target {
	t := Target{ID: typ + ":" + it.ID, Name: it.Name, Type: typ, Status: firstNonEmpty(it.Info.State, "unknown"), Health: komodoHealth(it.Info.State),
		Where: k.serverName(ctx, it.Info.ServerID, it.Info.ServerName), Message: it.Info.Status, UpdateAvailable: it.Info.UpdateAvailable,
		URL: k.c.link("/" + typ + "s/" + it.ID)}
	if typ == "deployment" {
		t.Source = it.Info.Image
		t.Revision = it.Info.Image
		return t
	}
	for _, s := range it.Info.Services {
		t.UpdateAvailable = t.UpdateAvailable || s.UpdateAvailable
	}
	switch {
	case it.Info.Repo != "":
		t.Source = it.Info.Repo
		if it.Info.Branch != "" {
			t.Source += " @ " + it.Info.Branch
		}
	case it.Info.FilesOnHost:
		t.Source = "files on the server"
	default:
		t.Source = "compose file in Komodo"
	}
	t.Revision = it.Info.DeployedHash
	if it.Info.DeployedHash != "" && it.Info.LatestHash != "" && it.Info.DeployedHash != it.Info.LatestHash {
		t.OutOfSync = true
	}
	return t
}

func (k *komodo) list(ctx context.Context, typ string) ([]kmListItem, error) {
	req := map[string]string{"stack": "ListStacks", "deployment": "ListDeployments"}[typ]
	var items []kmListItem
	err := k.call(ctx, "read", req, map[string]any{}, &items)
	return items, err
}

func (k *komodo) Targets(ctx context.Context) ([]Target, error) {
	var out []Target
	for _, typ := range []string{"stack", "deployment"} {
		items, err := k.list(ctx, typ)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			out = append(out, k.target(ctx, typ, it))
		}
	}
	return out, nil
}

func splitKomodoID(id string) (typ, kid string, err error) {
	typ, kid, ok := strings.Cut(id, ":")
	if !ok || (typ != "stack" && typ != "deployment") || kid == "" {
		return "", "", fmt.Errorf("invalid Komodo target id %q (stack:<id> or deployment:<id>)", id)
	}
	return typ, kid, nil
}

type kmStack struct {
	Config struct {
		FileContents string `json:"file_contents"`
		Repo         string `json:"repo"`
		AutoUpdate   bool   `json:"auto_update"`
		AutoPull     bool   `json:"auto_pull"`
	} `json:"config"`
	Info struct {
		DeployedContents []kmFile `json:"deployed_contents"`
		RemoteContents   []kmFile `json:"remote_contents"`
		DeployedMessage  string   `json:"deployed_message"`
		LatestMessage    string   `json:"latest_message"`
	} `json:"info"`
}

type kmFile struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

type kmUpdate struct {
	ID  string `json:"id"`
	OID struct {
		OID string `json:"$oid"`
	} `json:"_id"`
	Operation string `json:"operation"`
	StartTS   int64  `json:"start_ts"`
	EndTS     int64  `json:"end_ts"`
	Success   bool   `json:"success"`
	Status    string `json:"status"` // Queued, InProgress, Complete
	Username  string `json:"username"`
	Operator  string `json:"operator"`
	Version   any    `json:"version"`
	Commit    string `json:"commit_hash"`
	Logs      []struct {
		Stage   string `json:"stage"`
		Command string `json:"command"`
		Stdout  string `json:"stdout"`
		Stderr  string `json:"stderr"`
		Success bool   `json:"success"`
	} `json:"logs"`
}

func (u *kmUpdate) id() string { return firstNonEmpty(u.ID, u.OID.OID) }

func (k *komodo) Detail(ctx context.Context, id string) (*Detail, error) {
	typ, kid, err := splitKomodoID(id)
	if err != nil {
		return nil, err
	}
	items, err := k.list(ctx, typ)
	if err != nil {
		return nil, err
	}
	var it *kmListItem
	for i := range items {
		if items[i].ID == kid {
			it = &items[i]
		}
	}
	if it == nil {
		return nil, fmt.Errorf("%s %s not found", typ, kid)
	}
	d := &Detail{Target: k.target(ctx, typ, *it)}
	if typ == "deployment" {
		d.Services = []Service{{Name: it.Name, Image: it.Info.Image, Status: d.Status, Health: d.Health, Message: it.Info.Status, UpdateAvailable: it.Info.UpdateAvailable}}
	} else {
		for _, s := range it.Info.Services {
			d.Services = append(d.Services, Service{Name: s.Service, Image: s.Image, Status: d.Status, Health: d.Health, UpdateAvailable: s.UpdateAvailable})
		}
		var st kmStack
		if err := k.call(ctx, "read", "GetStack", map[string]any{"stack": kid}, &st); err == nil {
			d.File = st.Config.FileContents
			d.Git = st.Config.Repo != ""
			d.AutoSync = st.Config.AutoUpdate
			if d.OutOfSync && st.Info.LatestMessage != "" {
				d.Message = firstNonEmpty(d.Message, "latest commit: "+st.Info.LatestMessage)
			}
		}
	}
	// history: the target's updates, newest first
	var ups struct {
		Updates []kmUpdate `json:"updates"`
	}
	q := map[string]any{"target.type": strings.ToUpper(typ[:1]) + typ[1:], "target.id": kid}
	if err := k.call(ctx, "read", "ListUpdates", map[string]any{"query": q, "page": 0}, &ups); err == nil {
		for i, u := range ups.Updates {
			if i == 30 {
				break
			}
			e := Deployment{ID: u.id(), Operation: u.Operation, Time: unixTime(u.StartTS), By: firstNonEmpty(u.Username, u.Operator), Revision: short(u.Commit)}
			if u.EndTS > 0 && u.StartTS > 0 {
				e.Duration = u.EndTS - u.StartTS
			}
			switch {
			case u.Status != "" && u.Status != "Complete":
				e.Result = "running"
			case u.Success:
				e.Result = "success"
			default:
				e.Result = "failed"
			}
			d.History = append(d.History, e)
			if i == 0 && (e.Result == "running" || e.Result == "failed") {
				d.Operation = &Operation{Name: u.Operation, Phase: map[string]string{"running": "Running", "failed": "Failed"}[e.Result], Started: unixTime(u.StartTS), Finished: unixTime(u.EndTS)}
				if e.Result == "running" {
					d.Health = HealthBusy
				}
			}
			if d.Updated.IsZero() && u.Success && strings.HasPrefix(u.Operation, "Deploy") {
				d.Updated = unixTime(u.StartTS)
			}
		}
	}
	return d, nil
}

func (k *komodo) Logs(ctx context.Context, d *Detail, o LogOptions) (string, error) {
	typ, kid, err := splitKomodoID(d.ID)
	if err != nil {
		return "", err
	}
	var log struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	tail := max(o.Tail, 1)
	if typ == "stack" {
		services := []string{}
		if o.Service != "" {
			services = append(services, o.Service)
		}
		err = k.call(ctx, "read", "GetStackLog", map[string]any{"stack": kid, "services": services, "tail": tail, "timestamps": true}, &log)
	} else {
		err = k.call(ctx, "read", "GetDeploymentLog", map[string]any{"deployment": kid, "tail": tail, "timestamps": true}, &log)
	}
	if err != nil {
		return "", err
	}
	out := log.Stdout
	if strings.TrimSpace(log.Stderr) != "" {
		out += "\n[stderr]\n" + log.Stderr
	}
	return out, nil
}

// Diff compares a stack's deployed files with what a deploy would use:
// the compose file kept in Komodo, or the repository's latest files.
func (k *komodo) Diff(ctx context.Context, d *Detail) (string, error) {
	typ, kid, err := splitKomodoID(d.ID)
	if err != nil {
		return "", err
	}
	if typ != "stack" {
		return "", ErrNoDiff
	}
	var st kmStack
	if err := k.call(ctx, "read", "GetStack", map[string]any{"stack": kid}, &st); err != nil {
		return "", err
	}
	want := st.Info.RemoteContents
	if st.Config.Repo == "" && st.Config.FileContents != "" {
		want = []kmFile{{Path: "compose.yaml", Contents: st.Config.FileContents}}
		if len(st.Info.DeployedContents) == 1 {
			want[0].Path = st.Info.DeployedContents[0].Path
		}
	}
	if len(want) == 0 && len(st.Info.DeployedContents) == 0 {
		return "", ErrNoDiff
	}
	have := map[string]string{}
	var paths []string
	for _, f := range st.Info.DeployedContents {
		have[f.Path] = f.Contents
		paths = append(paths, f.Path)
	}
	wantBy := map[string]string{}
	for _, f := range want {
		wantBy[f.Path] = f.Contents
		if _, ok := have[f.Path]; !ok {
			paths = append(paths, f.Path)
		}
	}
	var b strings.Builder
	for _, p := range paths {
		a, z := have[p], wantBy[p]
		if a == z {
			continue
		}
		ud := difflib.UnifiedDiff{A: difflib.SplitLines(a), B: difflib.SplitLines(z), FromFile: "deployed", ToFile: "latest", Context: 3}
		s, _ := difflib.GetUnifiedDiffString(ud)
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n%s", p, p, s)
	}
	return b.String(), nil
}

func (k *komodo) Run(ctx context.Context, d *Detail, a Action) (*Result, error) {
	typ, kid, err := splitKomodoID(d.ID)
	if err != nil {
		return nil, err
	}
	var req string
	params := map[string]any{typ: kid}
	if typ == "stack" {
		req = map[string]string{"deploy": "DeployStack", "pull": "PullStack", "restart": "RestartStack", "start": "StartStack", "stop": "StopStack"}[a.ID]
		if a.Service != "" {
			params["services"] = []string{a.Service}
		}
	} else {
		req = map[string]string{"deploy": "Deploy", "pull": "PullDeployment", "restart": "RestartDeployment", "start": "StartDeployment", "stop": "StopDeployment"}[a.ID]
	}
	if req == "" {
		return nil, fmt.Errorf("Komodo has no action %q", a.ID)
	}
	var u kmUpdate
	if err := k.call(ctx, "execute", req, params, &u); err != nil {
		return nil, err
	}
	// follow the update until it completes (or the call's time is up)
	poll := k.poll
	if poll == 0 {
		poll = time.Second
	}
	for u.Status != "" && u.Status != "Complete" {
		select {
		case <-ctx.Done():
			return &Result{OK: true, Message: fmt.Sprintf("%s of %s is still running (update %s); check status later", req, d.Name, u.id())}, nil
		case <-time.After(poll):
		}
		var next kmUpdate
		if err := k.call(ctx, "read", "GetUpdate", map[string]any{"id": u.id()}, &next); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				continue
			}
			return nil, err
		}
		if next.id() == "" {
			next.ID = u.id()
		}
		u = next
	}
	var out strings.Builder
	for _, l := range u.Logs {
		fmt.Fprintf(&out, "== %s", l.Stage)
		if !l.Success {
			out.WriteString(" (failed)")
		}
		out.WriteString("\n")
		if l.Command != "" {
			out.WriteString("$ " + l.Command + "\n")
		}
		if s := strings.TrimSpace(l.Stdout); s != "" {
			out.WriteString(s + "\n")
		}
		if s := strings.TrimSpace(l.Stderr); s != "" {
			out.WriteString(s + "\n")
		}
	}
	res := &Result{OK: u.Success, Output: out.String()}
	if u.Success {
		res.Message = fmt.Sprintf("%s of %s succeeded", req, d.Name)
	} else {
		res.Message = fmt.Sprintf("%s of %s failed", req, d.Name)
	}
	return res, nil
}
