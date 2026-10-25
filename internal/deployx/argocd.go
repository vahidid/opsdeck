package deployx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"

	"opsdeck/internal/kube"
)

// Argo CD (v2/v3 REST API, the grpc-gateway of argocd-server): the
// applications the token's account may get. Ids are namespace/name; the
// namespace goes to the API as appNamespace (in the body for sync and
// rollback, in the query elsewhere). Auth: Authorization: Bearer.
type argocd struct {
	c *client
}

type argoSource struct {
	RepoURL        string `json:"repoURL"`
	Path           string `json:"path"`
	TargetRevision string `json:"targetRevision"`
	Chart          string `json:"chart"`
}

type argoApp struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Project     string       `json:"project"`
		Source      *argoSource  `json:"source"`
		Sources     []argoSource `json:"sources"`
		Destination struct {
			Server    string `json:"server"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"destination"`
		SyncPolicy *struct {
			Automated *struct {
				Prune bool `json:"prune"`
			} `json:"automated"`
		} `json:"syncPolicy"`
	} `json:"spec"`
	Status struct {
		Sync struct {
			Status    string   `json:"status"`
			Revision  string   `json:"revision"`
			Revisions []string `json:"revisions"`
		} `json:"sync"`
		Health struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"health"`
		OperationState *struct {
			Operation struct {
				Sync *struct {
					Revision string `json:"revision"`
				} `json:"sync"`
			} `json:"operation"`
			Phase      string    `json:"phase"`
			Message    string    `json:"message"`
			StartedAt  time.Time `json:"startedAt"`
			FinishedAt time.Time `json:"finishedAt"`
		} `json:"operationState"`
		History []struct {
			ID              int64       `json:"id"`
			Revision        string      `json:"revision"`
			Revisions       []string    `json:"revisions"`
			DeployedAt      time.Time   `json:"deployedAt"`
			DeployStartedAt time.Time   `json:"deployStartedAt"`
			Source          *argoSource `json:"source"`
			InitiatedBy     *struct {
				Username  string `json:"username"`
				Automated bool   `json:"automated"`
			} `json:"initiatedBy"`
		} `json:"history"`
		ReconciledAt time.Time `json:"reconciledAt"`
		Summary      struct {
			Images []string `json:"images"`
		} `json:"summary"`
		Resources []argoResource `json:"resources"`
	} `json:"status"`
}

type argoResource struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Health    *struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"health"`
	RequiresPruning bool `json:"requiresPruning"`
}

func (a *argocd) Verify(ctx context.Context) (Server, error) {
	var s Server
	var v struct{ Version string }
	if err := a.c.do(ctx, "GET", "/api/version", nil, nil, &v); err != nil {
		return s, err
	}
	s.Version = v.Version
	var u struct {
		LoggedIn bool   `json:"loggedIn"`
		Username string `json:"username"`
	}
	if err := a.c.do(ctx, "GET", "/api/v1/session/userinfo", nil, nil, &u); err != nil {
		return s, err
	}
	if !u.LoggedIn {
		return s, &HTTPError{Status: 401, Message: "the token is not accepted"}
	}
	s.User = u.Username
	return s, nil
}

func argoHealth(h string) string {
	switch h {
	case "Healthy":
		return HealthOK
	case "Progressing":
		return HealthBusy
	case "Degraded", "Missing":
		return HealthErr
	case "Suspended":
		return HealthWarn
	}
	return HealthUnknown
}

func (a *argocd) source(app *argoApp) string {
	srcs := app.Spec.Sources
	if app.Spec.Source != nil {
		srcs = append([]argoSource{*app.Spec.Source}, srcs...)
	}
	var parts []string
	for _, s := range srcs {
		p := s.RepoURL
		if s.Chart != "" {
			p += " chart " + s.Chart
		}
		if s.Path != "" {
			p += " : " + s.Path
		}
		if s.TargetRevision != "" {
			p += " @ " + s.TargetRevision
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " + ")
}

func (a *argocd) target(app *argoApp) Target {
	ns := app.Metadata.Namespace
	t := Target{ID: ns + "/" + app.Metadata.Name, Name: app.Metadata.Name, Type: "application",
		Status: firstNonEmpty(app.Status.Health.Status, "Unknown"), Health: argoHealth(app.Status.Health.Status),
		Sync: app.Status.Sync.Status, OutOfSync: app.Status.Sync.Status == "OutOfSync",
		Revision: firstNonEmpty(app.Status.Sync.Revision, strings.Join(app.Status.Sync.Revisions, ",")),
		Source:   a.source(app), Message: app.Status.Health.Message,
		URL: a.c.link("/applications/" + url.PathEscape(ns) + "/" + url.PathEscape(app.Metadata.Name))}
	dest := firstNonEmpty(app.Spec.Destination.Name, app.Spec.Destination.Server)
	if dest == "https://kubernetes.default.svc" {
		dest = "in-cluster"
	}
	t.Where = dest
	if app.Spec.Destination.Namespace != "" {
		t.Where += " / " + app.Spec.Destination.Namespace
	}
	if app.Spec.Project != "" && app.Spec.Project != "default" {
		t.Where += " (project " + app.Spec.Project + ")"
	}
	if h := app.Status.History; len(h) > 0 {
		t.Updated = h[len(h)-1].DeployedAt
	}
	if op := app.Status.OperationState; op != nil {
		switch op.Phase {
		case "Running", "Terminating":
			t.Health = HealthBusy
			t.Status = "Syncing"
		case "Failed", "Error":
			t.Message = firstNonEmpty(op.Message, t.Message)
			if t.Health == HealthOK {
				t.Health = HealthWarn
			}
		}
	}
	if t.Sync == "Unknown" && t.Health == HealthOK {
		t.Health = HealthWarn
	}
	return t
}

// appQuery is the query naming an application's namespace.
func appQuery(ns string) url.Values {
	if ns == "" {
		return url.Values{}
	}
	return url.Values{"appNamespace": {ns}}
}

func splitAppID(id string) (ns, name string) {
	if n, nm, ok := strings.Cut(id, "/"); ok {
		return n, nm
	}
	return "", id
}

func (a *argocd) Targets(ctx context.Context) ([]Target, error) {
	var list struct {
		Items []argoApp `json:"items"`
	}
	q := url.Values{"fields": {"items.metadata.name,items.metadata.namespace,items.spec,items.status.sync,items.status.health,items.status.operationState,items.status.history,items.status.summary"}}
	if err := a.c.do(ctx, "GET", "/api/v1/applications", q, nil, &list); err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, a.target(&list.Items[i]))
	}
	return out, nil
}

func (a *argocd) app(ctx context.Context, id string, refresh string) (*argoApp, error) {
	ns, name := splitAppID(id)
	q := appQuery(ns)
	if refresh != "" {
		q.Set("refresh", refresh)
	}
	var app argoApp
	if err := a.c.do(ctx, "GET", "/api/v1/applications/"+url.PathEscape(name), q, nil, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (a *argocd) Detail(ctx context.Context, id string) (*Detail, error) {
	app, err := a.app(ctx, id, "")
	if err != nil {
		return nil, err
	}
	d := &Detail{Target: a.target(app), Git: true}
	d.AutoSync = app.Spec.SyncPolicy != nil && app.Spec.SyncPolicy.Automated != nil
	if op := app.Status.OperationState; op != nil {
		d.Operation = &Operation{Name: "sync", Phase: op.Phase, Message: op.Message, Started: op.StartedAt, Finished: op.FinishedAt}
	}
	// v3 keeps resource health in the resource tree, not on the app
	var tree map[string]argoResource
	for _, r := range app.Status.Resources {
		if r.Health == nil {
			tree = a.treeHealth(ctx, id)
			break
		}
	}
	for _, r := range app.Status.Resources {
		s := Service{Name: r.Name, Kind: r.Kind, Namespace: r.Namespace, Status: r.Status, OutOfSync: r.Status == "OutOfSync", Health: HealthUnknown}
		h := r.Health
		if h == nil {
			if t, ok := tree[r.Kind+"/"+r.Namespace+"/"+r.Name]; ok {
				h = t.Health
			}
		}
		if h != nil {
			s.Health, s.Message = argoHealth(h.Status), h.Message
			s.Status = strings.TrimSpace(h.Status + " · " + r.Status)
		}
		if r.RequiresPruning {
			s.Message = strings.TrimSpace("not in Git: deleted by a sync with prune. " + s.Message)
		}
		d.Services = append(d.Services, s)
	}
	for i := len(app.Status.History) - 1; i >= 0; i-- {
		h := app.Status.History[i]
		e := Deployment{ID: strconv.FormatInt(h.ID, 10), Revision: firstNonEmpty(h.Revision, strings.Join(h.Revisions, ",")), Time: h.DeployedAt, Operation: "sync", Result: "deployed"}
		if !h.DeployStartedAt.IsZero() && !h.DeployedAt.IsZero() {
			e.Duration = h.DeployedAt.Sub(h.DeployStartedAt).Milliseconds()
		}
		if h.Source != nil {
			e.Source = h.Source.RepoURL
		}
		if h.InitiatedBy != nil {
			e.By = h.InitiatedBy.Username
			if h.InitiatedBy.Automated {
				e.By = "auto-sync"
			}
		}
		d.History = append(d.History, e)
	}
	return d, nil
}

// treeHealth maps kind/namespace/name to the resource's health from the
// resource tree (nil when it cannot be read).
func (a *argocd) treeHealth(ctx context.Context, id string) map[string]argoResource {
	ns, name := splitAppID(id)
	var tree struct {
		Nodes []argoResource `json:"nodes"`
	}
	if a.c.do(ctx, "GET", "/api/v1/applications/"+url.PathEscape(name)+"/resource-tree", appQuery(ns), nil, &tree) != nil {
		return nil
	}
	out := map[string]argoResource{}
	for _, n := range tree.Nodes {
		if n.Health != nil {
			out[n.Kind+"/"+n.Namespace+"/"+n.Name] = n
		}
	}
	return out
}

// workloads are the kinds whose pods have logs and that restart.
var workloads = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "Rollout": true}

func (a *argocd) Logs(ctx context.Context, d *Detail, o LogOptions) (string, error) {
	app, err := a.app(ctx, d.ID, "")
	if err != nil {
		return "", err
	}
	var picked []argoResource
	for _, r := range app.Status.Resources {
		if o.Service != "" && r.Name == o.Service || o.Service == "" && workloads[r.Kind] {
			picked = append(picked, r)
		}
	}
	if len(picked) == 0 {
		if o.Service != "" {
			return "", fmt.Errorf("%s has no resource %q", d.Name, o.Service)
		}
		return d.Name + " has no Deployments, StatefulSets or DaemonSets (name a pod with service).", nil
	}
	if len(picked) > 8 {
		picked = picked[:8]
	}
	ns, name := splitAppID(d.ID)
	var b strings.Builder
	for _, r := range picked {
		q := appQuery(ns)
		q.Set("namespace", r.Namespace)
		q.Set("kind", r.Kind)
		q.Set("group", r.Group)
		q.Set("resourceName", r.Name)
		q.Set("tailLines", strconv.Itoa(max(o.Tail, 1)))
		q.Set("follow", "false")
		if r.Kind == "Pod" {
			q = appQuery(ns)
			q.Set("namespace", r.Namespace)
			q.Set("podName", r.Name)
			q.Set("tailLines", strconv.Itoa(max(o.Tail, 1)))
			q.Set("follow", "false")
		}
		fmt.Fprintf(&b, "==> %s %s/%s\n", r.Kind, r.Namespace, r.Name)
		text, err := a.logStream(ctx, "/api/v1/applications/"+url.PathEscape(name)+"/logs", q)
		if err != nil {
			fmt.Fprintf(&b, "(could not read: %v)\n\n", err)
			continue
		}
		b.WriteString(text + "\n")
	}
	return b.String(), nil
}

// logStream reads Argo CD's log stream: one JSON object per line,
// {"result":{"content","podName","timeStampStr","last"}} or {"error":…}.
func (a *argocd) logStream(ctx context.Context, p string, q url.Values) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res, err := a.c.send(ctx, "GET", p, q, nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var buf [4096]byte
		n, _ := res.Body.Read(buf[:])
		return "", &HTTPError{Status: res.StatusCode, Message: errorMessage(buf[:n], "")}
	}
	var b strings.Builder
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	pods := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "data:"))
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		var e struct {
			Result *struct {
				Content      string `json:"content"`
				PodName      string `json:"podName"`
				TimeStampStr string `json:"timeStampStr"`
				Last         bool   `json:"last"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.Error != nil {
			return b.String(), errors.New(e.Error.Message)
		}
		if e.Result == nil {
			continue
		}
		if e.Result.Last {
			break
		}
		pods[e.Result.PodName] = true
		if e.Result.TimeStampStr != "" {
			b.WriteString(e.Result.TimeStampStr + " ")
		}
		if len(pods) > 1 || e.Result.PodName != "" {
			b.WriteString("[" + e.Result.PodName + "] ")
		}
		b.WriteString(e.Result.Content + "\n")
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return b.String(), err
	}
	return b.String(), nil
}

// Diff compares each managed resource's live state with the state a sync
// would leave (predicted, else the target from Git): a unified diff per
// resource that differs; Secrets are masked by Argo CD.
func (a *argocd) Diff(ctx context.Context, d *Detail) (string, error) {
	ns, name := splitAppID(d.ID)
	var mr struct {
		Items []struct {
			Group               string `json:"group"`
			Kind                string `json:"kind"`
			Namespace           string `json:"namespace"`
			Name                string `json:"name"`
			TargetState         string `json:"targetState"`
			LiveState           string `json:"liveState"`
			NormalizedLiveState string `json:"normalizedLiveState"`
			PredictedLiveState  string `json:"predictedLiveState"`
			Hook                bool   `json:"hook"`
			Modified            bool   `json:"modified"`
		} `json:"items"`
	}
	if err := a.c.do(ctx, "GET", "/api/v1/applications/"+url.PathEscape(name)+"/managed-resources", appQuery(ns), nil, &mr); err != nil {
		return "", err
	}
	sort.SliceStable(mr.Items, func(i, j int) bool {
		x, y := mr.Items[i], mr.Items[j]
		return x.Kind+"/"+x.Namespace+"/"+x.Name < y.Kind+"/"+y.Namespace+"/"+y.Name
	})
	var b strings.Builder
	for _, it := range mr.Items {
		if it.Hook {
			continue
		}
		live := decodeState(firstNonEmpty(it.NormalizedLiveState, it.LiveState))
		want := decodeState(firstNonEmpty(it.PredictedLiveState, it.TargetState))
		if !it.Modified && (live == nil) == (want == nil) {
			continue
		}
		diff := stateDiff(live, want)
		if diff == "" {
			continue
		}
		ref := it.Kind + "/" + it.Name
		if it.Namespace != "" {
			ref = it.Kind + "/" + it.Namespace + "/" + it.Name
		}
		switch {
		case live == nil:
			ref += " (created)"
		case want == nil:
			ref += " (not in Git: deleted only with prune)"
		}
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n%s", ref, ref, diff)
		if !strings.HasSuffix(diff, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// stateDiff is a unified diff of a resource's live state and the state
// after a sync, without the fields the server maintains.
func stateDiff(live, want kube.Object) string {
	a, b := "", ""
	if live != nil {
		a, _ = kube.YAMLOf(kube.Clean(live), false)
	}
	if want != nil {
		b, _ = kube.YAMLOf(kube.Clean(want), false)
	}
	if a == b {
		return ""
	}
	d := difflib.UnifiedDiff{A: difflib.SplitLines(a), B: difflib.SplitLines(b), FromFile: "live", ToFile: "after sync", Context: 3}
	s, _ := difflib.GetUnifiedDiffString(d)
	return s
}

// decodeState reads a state Argo CD sends as a JSON string ("null" or ""
// when the side is missing).
func decodeState(s string) kube.Object {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return nil
	}
	var o kube.Object
	if json.Unmarshal([]byte(s), &o) != nil {
		return nil
	}
	return o
}

func (a *argocd) Run(ctx context.Context, d *Detail, act Action) (*Result, error) {
	ns, name := splitAppID(d.ID)
	path := "/api/v1/applications/" + url.PathEscape(name)
	switch act.ID {
	case "deploy":
		body := map[string]any{"prune": act.Prune}
		if ns != "" {
			body["appNamespace"] = ns
		}
		if act.Revision != "" {
			body["revision"] = act.Revision
		}
		if err := a.c.do(ctx, "POST", path+"/sync", nil, body, nil); err != nil {
			return nil, err
		}
		msg := "Sync of " + d.Name + " started"
		if act.Revision != "" {
			msg += " to revision " + act.Revision
		}
		if act.Prune {
			msg += " (with prune)"
		}
		return &Result{OK: true, Message: msg}, nil
	case "rollback":
		id, err := strconv.ParseInt(act.History, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid history id %q", act.History)
		}
		body := map[string]any{"id": id, "prune": act.Prune}
		if ns != "" {
			body["appNamespace"] = ns
		}
		if err := a.c.do(ctx, "POST", path+"/rollback", nil, body, nil); err != nil {
			return nil, err
		}
		return &Result{OK: true, Message: fmt.Sprintf("Rollback of %s to history #%d started", d.Name, id)}, nil
	case "refresh":
		mode := "normal"
		if act.Hard {
			mode = "hard"
		}
		app, err := a.app(ctx, d.ID, mode)
		if err != nil {
			return nil, err
		}
		t := a.target(app)
		return &Result{OK: true, Message: fmt.Sprintf("Refreshed %s (%s): %s, %s", d.Name, mode, t.Sync, t.Status)}, nil
	case "terminate":
		if err := a.c.do(ctx, "DELETE", path+"/operation", appQuery(ns), nil, nil); err != nil {
			return nil, err
		}
		return &Result{OK: true, Message: "Terminating the running sync of " + d.Name}, nil
	case "restart":
		app, err := a.app(ctx, d.ID, "")
		if err != nil {
			return nil, err
		}
		var done, failed []string
		for _, r := range app.Status.Resources {
			if act.Service != "" && r.Name != act.Service || act.Service == "" && !workloads[r.Kind] {
				continue
			}
			if !workloads[r.Kind] {
				return nil, fmt.Errorf("%s %s cannot be restarted (Deployments, StatefulSets, DaemonSets, Rollouts)", r.Kind, r.Name)
			}
			q := appQuery(ns)
			q.Set("namespace", r.Namespace)
			q.Set("resourceName", r.Name)
			q.Set("kind", r.Kind)
			q.Set("group", r.Group)
			q.Set("version", r.Version)
			// the action name is the whole body, as a JSON string
			if err := a.c.do(ctx, "POST", path+"/resource/actions", q, "restart", nil); err != nil {
				failed = append(failed, r.Kind+"/"+r.Name+": "+err.Error())
				continue
			}
			done = append(done, r.Kind+"/"+r.Name)
		}
		if len(done)+len(failed) == 0 {
			return nil, fmt.Errorf("%s has no workloads to restart", d.Name)
		}
		res := &Result{OK: len(failed) == 0, Message: fmt.Sprintf("Rollout restart of %s", strings.Join(done, ", "))}
		if len(done) == 0 {
			res.Message = "Restart failed"
		}
		if len(failed) > 0 {
			res.Output = strings.Join(failed, "\n")
		}
		return res, nil
	}
	return nil, fmt.Errorf("Argo CD has no action %q", act.ID)
}
