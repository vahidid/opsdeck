package deployx

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Portainer (CE/BE 2.x): stacks of the environments the token's user can
// reach. Containers, logs and restarts go through Portainer's Docker
// proxy (/api/endpoints/{id}/docker/…). Auth: X-API-Key.
type portainer struct {
	c *client

	mu        sync.Mutex
	endpoints map[int]string // id → name
}

type ptStack struct {
	ID         int    `json:"Id"`
	Name       string `json:"Name"`
	Type       int    `json:"Type"` // 1 swarm, 2 compose, 3 kubernetes
	EndpointID int    `json:"EndpointId"`
	Status     int    `json:"Status"` // 1 active, 2 inactive, 3 deploying, 4 error
	EntryPoint string `json:"EntryPoint"`
	Env        []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"Env"`
	CreationDate int64  `json:"CreationDate"`
	CreatedBy    string `json:"CreatedBy"`
	UpdateDate   int64  `json:"UpdateDate"`
	UpdatedBy    string `json:"UpdatedBy"`
	Namespace    string `json:"Namespace"`
	GitConfig    *struct {
		URL            string `json:"URL"`
		ReferenceName  string `json:"ReferenceName"`
		ConfigFilePath string `json:"ConfigFilePath"`
		ConfigHash     string `json:"ConfigHash"`
		Authentication *struct {
			Username string `json:"Username"`
		} `json:"Authentication"`
	} `json:"GitConfig"`
	AutoUpdate *struct {
		Interval string `json:"Interval"`
		Webhook  string `json:"Webhook"`
	} `json:"AutoUpdate"`
}

var ptTypes = map[int]string{1: "swarm", 2: "compose", 3: "kubernetes"}

func (p *portainer) Verify(ctx context.Context) (Server, error) {
	var s Server
	var v struct {
		ServerVersion string `json:"ServerVersion"`
		ServerEdition string `json:"ServerEdition"`
	}
	if err := p.c.do(ctx, "GET", "/api/system/version", nil, nil, &v); err != nil {
		// before 2.18 there is only the public status
		var st struct{ Version string }
		if err2 := p.c.do(ctx, "GET", "/api/status", nil, nil, &st); err2 != nil {
			return s, err
		}
		v.ServerVersion = st.Version
	}
	s.Version = strings.TrimSpace(v.ServerVersion + " " + v.ServerEdition)
	var me struct {
		Username string `json:"Username"`
		Role     int    `json:"Role"`
	}
	if err := p.c.do(ctx, "GET", "/api/users/me", nil, nil, &me); err != nil {
		return s, err
	}
	s.User = me.Username
	if me.Role == 1 {
		s.User += " (administrator)"
	}
	return s, nil
}

// endpointName names an environment (looked up once).
func (p *portainer) endpointName(ctx context.Context, id int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.endpoints == nil {
		var eps []struct {
			ID   int    `json:"Id"`
			Name string `json:"Name"`
		}
		if p.c.do(ctx, "GET", "/api/endpoints", url.Values{"excludeSnapshots": {"true"}}, nil, &eps) == nil {
			p.endpoints = map[int]string{}
			for _, e := range eps {
				p.endpoints[e.ID] = e.Name
			}
		}
	}
	if n, ok := p.endpoints[id]; ok {
		return n
	}
	return fmt.Sprintf("environment %d", id)
}

func (p *portainer) target(ctx context.Context, s ptStack) Target {
	t := Target{ID: strconv.Itoa(s.ID), Name: s.Name, Type: ptTypes[s.Type], Where: p.endpointName(ctx, s.EndpointID),
		Updated: unixTime(max(s.UpdateDate, s.CreationDate)),
		URL:     p.c.link(fmt.Sprintf("/#!/%d/docker/stacks/%s?id=%d&type=%d&regular=true", s.EndpointID, url.PathEscape(s.Name), s.ID, s.Type))}
	if t.Type == "" {
		t.Type = "stack"
	}
	switch s.Status {
	case 1:
		t.Status, t.Health = "active", HealthOK
	case 2:
		t.Status, t.Health = "inactive", HealthWarn
	case 3:
		t.Status, t.Health = "deploying", HealthBusy
	case 4:
		t.Status, t.Health = "error", HealthErr
	default:
		t.Status, t.Health = "unknown", HealthUnknown
	}
	if g := s.GitConfig; g != nil {
		t.Revision = g.ConfigHash
		t.Source = g.URL
		if g.ReferenceName != "" {
			t.Source += " @ " + strings.TrimPrefix(g.ReferenceName, "refs/heads/")
		}
		if g.ConfigFilePath != "" {
			t.Source += " : " + g.ConfigFilePath
		}
	} else {
		t.Source = firstNonEmpty(s.EntryPoint, "docker-compose.yml") + " (stored in Portainer)"
	}
	if s.Type == 3 && s.Namespace != "" {
		t.Where += " / " + s.Namespace
	}
	return t
}

func (p *portainer) Targets(ctx context.Context) ([]Target, error) {
	var stacks []ptStack
	if err := p.c.do(ctx, "GET", "/api/stacks", nil, nil, &stacks); err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(stacks))
	for _, s := range stacks {
		out = append(out, p.target(ctx, s))
	}
	return out, nil
}

func (p *portainer) stack(ctx context.Context, id string) (ptStack, error) {
	var s ptStack
	if _, err := strconv.Atoi(id); err != nil {
		return s, fmt.Errorf("invalid Portainer stack id %q", id)
	}
	err := p.c.do(ctx, "GET", "/api/stacks/"+id, nil, nil, &s)
	return s, err
}

func (p *portainer) Detail(ctx context.Context, id string) (*Detail, error) {
	s, err := p.stack(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &Detail{Target: p.target(ctx, s), Git: s.GitConfig != nil, AutoSync: s.AutoUpdate != nil && (s.AutoUpdate.Interval != "" || s.AutoUpdate.Webhook != "")}
	var f struct {
		StackFileContent string `json:"StackFileContent"`
	}
	if err := p.c.do(ctx, "GET", "/api/stacks/"+id+"/file", nil, nil, &f); err == nil {
		d.File = f.StackFileContent
	}
	if s.Type != 3 && s.Status != 2 {
		cs, err := p.containers(ctx, s)
		if err != nil {
			d.Message = "containers: " + err.Error()
		}
		down := 0
		for _, c := range cs {
			svc := Service{Name: c.service(), Image: c.Image, Status: c.State, Health: HealthOK, Message: c.Status, Container: c.ID}
			switch {
			case c.State == "running" && strings.Contains(c.Status, "(unhealthy)"):
				svc.Health = HealthErr
				down++
			case c.State == "running" && strings.Contains(c.Status, "(health: starting)"):
				svc.Health = HealthBusy
			case c.State == "restarting":
				svc.Health = HealthErr
				down++
			case c.State != "running":
				svc.Health = HealthWarn
				down++
			}
			d.Services = append(d.Services, svc)
		}
		if down > 0 && d.Health == HealthOK {
			d.Health = HealthWarn
			d.Message = firstNonEmpty(d.Message, fmt.Sprintf("%d of %d containers are not running or unhealthy", down, len(cs)))
		}
	}
	return d, nil
}

type ptContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
}

func (c ptContainer) service() string {
	for _, k := range []string{"com.docker.compose.service", "com.docker.swarm.service.name"} {
		if v := c.Labels[k]; v != "" {
			return v
		}
	}
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID[:min(12, len(c.ID))]
}

// containers lists a stack's containers through the Docker proxy (compose
// projects are lower-case; swarm stacks label their tasks' containers).
func (p *portainer) containers(ctx context.Context, s ptStack) ([]ptContainer, error) {
	label := "com.docker.compose.project=" + strings.ToLower(s.Name)
	if s.Type == 1 {
		label = "com.docker.stack.namespace=" + s.Name
	}
	filters, _ := json.Marshal(map[string][]string{"label": {label}})
	var cs []ptContainer
	err := p.c.do(ctx, "GET", fmt.Sprintf("/api/endpoints/%d/docker/containers/json", s.EndpointID), url.Values{"all": {"1"}, "filters": {string(filters)}}, nil, &cs)
	sort.Slice(cs, func(i, j int) bool { return cs[i].service() < cs[j].service() })
	return cs, err
}

func (p *portainer) Logs(ctx context.Context, d *Detail, o LogOptions) (string, error) {
	s, err := p.stack(ctx, d.ID)
	if err != nil {
		return "", err
	}
	if s.Type == 3 {
		return "", errors.New("logs of Kubernetes stacks: use the cluster (kubernetes tool or the Kubernetes page)")
	}
	cs, err := p.containers(ctx, s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	n := 0
	for _, c := range cs {
		if o.Service != "" && c.service() != o.Service && c.ID != o.Service {
			continue
		}
		n++
		q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {strconv.Itoa(max(o.Tail, 1))}, "timestamps": {"1"}}
		raw, err := p.c.raw(ctx, "GET", fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/logs", s.EndpointID, c.ID), q, nil)
		fmt.Fprintf(&b, "==> %s (%s, %s)\n", c.service(), strings.TrimPrefix(firstOf(c.Names), "/"), c.State)
		if err != nil {
			fmt.Fprintf(&b, "(could not read: %v)\n\n", err)
			continue
		}
		b.WriteString(dockerLog(raw))
		b.WriteString("\n")
	}
	if n == 0 {
		if o.Service != "" {
			return "", fmt.Errorf("%s has no container for service %q", d.Name, o.Service)
		}
		return fmt.Sprintf("%s has no containers on %s.", d.Name, d.Where), nil
	}
	return b.String(), nil
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// dockerLog decodes Docker's multiplexed log stream (8-byte frame headers:
// stream type, 3 zero bytes, big-endian size); a TTY container's log is
// the raw text. stderr lines are marked.
func dockerLog(raw []byte) string {
	if len(raw) < 8 || raw[0] > 2 || raw[1] != 0 || raw[2] != 0 || raw[3] != 0 {
		return string(raw)
	}
	var b strings.Builder
	for len(raw) >= 8 {
		typ := raw[0]
		n := int(binary.BigEndian.Uint32(raw[4:8]))
		raw = raw[8:]
		if n > len(raw) {
			n = len(raw)
		}
		chunk := raw[:n]
		raw = raw[n:]
		if typ == 2 {
			for _, l := range strings.SplitAfter(string(chunk), "\n") {
				if l != "" {
					b.WriteString("[stderr] " + l)
				}
			}
			continue
		}
		b.Write(chunk)
	}
	return b.String()
}

func (p *portainer) Diff(ctx context.Context, d *Detail) (string, error) {
	return "", ErrNoDiff
}

func (p *portainer) Run(ctx context.Context, d *Detail, a Action) (*Result, error) {
	s, err := p.stack(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	ep := url.Values{"endpointId": {strconv.Itoa(s.EndpointID)}}
	env := []map[string]string{}
	for _, e := range s.Env {
		env = append(env, map[string]string{"name": e.Name, "value": e.Value})
	}
	switch a.ID {
	case "deploy":
		if g := s.GitConfig; g != nil {
			body := map[string]any{"RepositoryReferenceName": g.ReferenceName, "Env": env, "Prune": false,
				"PullImage": a.Pull, "RepullImageAndRedeploy": a.Pull, "StackName": s.Name}
			// keep the stored Git credentials: without a password the
			// server uses the saved one
			if g.Authentication != nil {
				body["RepositoryAuthentication"] = true
				body["RepositoryUsername"] = g.Authentication.Username
			}
			if err := p.c.do(ctx, "PUT", "/api/stacks/"+d.ID+"/git/redeploy", ep, body, nil); err != nil {
				return nil, err
			}
			return &Result{OK: true, Message: fmt.Sprintf("Redeployed %s from %s (pulled the repository)", s.Name, g.URL)}, nil
		}
		file := d.File
		if file == "" {
			var f struct{ StackFileContent string }
			if err := p.c.do(ctx, "GET", "/api/stacks/"+d.ID+"/file", nil, nil, &f); err != nil {
				return nil, err
			}
			file = f.StackFileContent
		}
		if err := p.update(ctx, d.ID, ep, file, env, false, a.Pull); err != nil {
			return nil, err
		}
		msg := "Redeployed " + s.Name
		if a.Pull {
			msg += " with freshly pulled images"
		}
		return &Result{OK: true, Message: msg}, nil
	case "update":
		if err := p.update(ctx, d.ID, ep, a.File, env, a.Prune, a.Pull); err != nil {
			return nil, err
		}
		return &Result{OK: true, Message: "Updated the stack file of " + s.Name + " and redeployed"}, nil
	case "start", "stop":
		if err := p.c.do(ctx, "POST", "/api/stacks/"+d.ID+"/"+a.ID, ep, nil, nil); err != nil {
			return nil, err
		}
		verb := map[string]string{"start": "Started", "stop": "Stopped"}[a.ID]
		return &Result{OK: true, Message: verb + " " + s.Name}, nil
	case "restart":
		if s.Type == 3 {
			return nil, errors.New("restart Kubernetes stacks on the cluster (kubernetes tool)")
		}
		cs, err := p.containers(ctx, s)
		if err != nil {
			return nil, err
		}
		var done, failed []string
		for _, c := range cs {
			if a.Service != "" && c.service() != a.Service {
				continue
			}
			if err := p.c.do(ctx, "POST", fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/restart", s.EndpointID, c.ID), nil, nil, nil); err != nil {
				failed = append(failed, c.service()+": "+err.Error())
				continue
			}
			done = append(done, c.service())
		}
		if len(done)+len(failed) == 0 {
			return nil, fmt.Errorf("%s has no containers to restart", s.Name)
		}
		r := &Result{OK: len(failed) == 0, Message: fmt.Sprintf("Restarted %d containers of %s (%s)", len(done), s.Name, strings.Join(done, ", "))}
		if len(failed) > 0 {
			r.Message += fmt.Sprintf("; %d failed", len(failed))
			r.Output = strings.Join(failed, "\n")
		}
		return r, nil
	}
	return nil, fmt.Errorf("Portainer has no action %q", a.ID)
}

// update replaces a stack's file (and redeploys it). Env is sent back as
// it is: the update replaces it.
func (p *portainer) update(ctx context.Context, id string, ep url.Values, file string, env []map[string]string, prune, pull bool) error {
	body := map[string]any{"StackFileContent": file, "Env": env, "Prune": prune, "PullImage": pull, "RepullImageAndRedeploy": pull}
	return p.c.do(ctx, "PUT", "/api/stacks/"+id, ep, body, nil)
}
