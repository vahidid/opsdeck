package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/workspace"
)

// ComposeProject is a Docker Compose project on a host.
type ComposeProject struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"` // running(2), exited(1)…
	ConfigFiles []string `json:"configFiles"`
	WorkingDir  string   `json:"workingDir"`
}

func (c *Client) ComposeProjects(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) ([]ComposeProject, error) {
	out, err := c.runOK(ctx, w, host, "compose ls -a --format json")
	if err != nil {
		if strings.Contains(err.Error(), "is not a docker command") || strings.Contains(err.Error(), "unknown command") {
			return nil, fmt.Errorf("the Docker Compose plugin (docker compose) is not installed on %s", host.Name)
		}
		return nil, err
	}
	var raw []struct{ Name, Status, ConfigFiles string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return nil, fmt.Errorf("parse docker compose ls: %w", err)
	}
	list := []ComposeProject{}
	for _, r := range raw {
		p := ComposeProject{Name: r.Name, Status: r.Status, ConfigFiles: []string{}}
		for _, f := range strings.Split(r.ConfigFiles, ",") {
			if f = strings.TrimSpace(f); f != "" {
				p.ConfigFiles = append(p.ConfigFiles, f)
			}
		}
		if len(p.ConfigFiles) > 0 {
			p.WorkingDir = path.Dir(p.ConfigFiles[0])
		}
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

func (c *Client) composeProject(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, name string) (ComposeProject, error) {
	if !ValidID(name) {
		return ComposeProject{}, fmt.Errorf("invalid compose project")
	}
	list, err := c.ComposeProjects(ctx, w, host)
	if err != nil {
		return ComposeProject{}, err
	}
	for _, p := range list {
		if p.Name == name {
			return p, nil
		}
	}
	return ComposeProject{}, fmt.Errorf("compose project %s not found on %s", name, host.Name)
}

// ComposeContainer is a container of a compose project.
type ComposeContainer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Service  string `json:"service"`
	Image    string `json:"image"`
	State    string `json:"state"`
	Status   string `json:"status"`
	Health   string `json:"health,omitempty"`
	ExitCode int    `json:"exitCode"`
	Ports    string `json:"ports,omitempty"`
}

// ComposePS lists a project's containers (stopped ones too).
func (c *Client) ComposePS(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, project string) ([]ComposeContainer, error) {
	if !ValidID(project) {
		return nil, fmt.Errorf("invalid compose project")
	}
	out, err := c.runOK(ctx, w, host, "compose --ansi never -p "+project+" ps -a --format json")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	var raw []map[string]any
	if strings.HasPrefix(out, "[") { // older versions print one array
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			return nil, fmt.Errorf("parse docker compose ps: %w", err)
		}
	} else if raw, err = jsonLines[map[string]any](out); err != nil {
		return nil, err
	}
	list := []ComposeContainer{}
	for _, r := range raw {
		id := str(r["ID"])
		if len(id) > 12 {
			id = id[:12]
		}
		code := 0
		if f, ok := r["ExitCode"].(float64); ok {
			code = int(f)
		}
		ct := ComposeContainer{ID: id, Name: str(r["Name"]), Service: str(r["Service"]), Image: str(r["Image"]), State: str(r["State"]),
			Status: str(r["Status"]), Health: str(r["Health"]), ExitCode: code}
		if ct.Health == "" {
			ct.Health = health(ct.Status)
		}
		if pubs, ok := r["Publishers"].([]any); ok {
			var ports []string
			for _, p := range pubs {
				m, _ := p.(map[string]any)
				if pp, _ := m["PublishedPort"].(float64); pp > 0 {
					ports = append(ports, fmt.Sprintf("%s:%v->%v/%s", str(m["URL"]), pp, m["TargetPort"], str(m["Protocol"])))
				}
			}
			ct.Ports = strings.Join(dedupe(ports), ", ")
		}
		if ct.Ports == "" {
			ct.Ports = str(r["Ports"])
		}
		list = append(list, ct)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Service+list[i].Name < list[j].Service+list[j].Name })
	return list, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ComposeActions are the actions on a project (or some of its services).
var ComposeActions = []string{"up", "recreate", "pull", "restart", "stop", "start", "down", "down-volumes"}

// composeBase is "docker compose -p P -f f1 -f f2 --project-directory D".
func composeBase(prefix string, p ComposeProject, envFile string) string {
	cmd := prefix + "compose --ansi never -p " + p.Name
	if len(p.ConfigFiles) > 0 {
		cmd += " --project-directory " + shq(p.WorkingDir)
		for _, f := range p.ConfigFiles {
			cmd += " -f " + shq(f)
		}
	}
	if envFile != "" {
		cmd += " --env-file " + envFile
	}
	return cmd
}

// ComposeAction runs an action on a project, or on some of its services,
// and returns compose's output.
func (c *Client) ComposeAction(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, project, action string, services []string) (string, error) {
	if !contains(ComposeActions, action) {
		return "", fmt.Errorf("unsupported compose action %q", action)
	}
	for _, s := range services {
		if !ValidID(s) {
			return "", fmt.Errorf("invalid service %q", s)
		}
	}
	p, err := c.composeProject(ctx, w, host, project)
	if err != nil {
		return "", err
	}
	pre, err := c.Prefix(ctx, w, host)
	if err != nil {
		return "", err
	}
	if (action == "up" || action == "recreate" || action == "pull") && len(p.ConfigFiles) == 0 {
		return "", fmt.Errorf("compose project %s has no known compose file on %s", project, host.Name)
	}
	args := map[string]string{
		"up": "up -d --remove-orphans", "recreate": "up -d --force-recreate --remove-orphans", "pull": "pull",
		"restart": "restart", "stop": "stop", "start": "start", "down": "down --remove-orphans", "down-volumes": "down -v --remove-orphans",
	}[action]
	if len(services) > 0 && !strings.HasPrefix(action, "down") {
		args += " " + strings.Join(services, " ")
	}
	res, err := c.hub.Exec(ctx, w, host, composeBase(pre, p, "")+" "+args+" 2>&1", nil, 4<<20)
	if err != nil {
		return res.Stdout, err
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("docker compose %s failed (exit %d):\n%s", action, res.ExitCode, tailLines(res.Stdout, 40))
	}
	return res.Stdout, nil
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ComposeFile is a file of a project that can be edited: its compose files
// and the .env next to them.
type ComposeFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"` // compose | env
	Exists bool   `json:"exists"`
}

func (c *Client) ComposeFiles(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, project string) ([]ComposeFile, error) {
	p, err := c.composeProject(ctx, w, host, project)
	if err != nil {
		return nil, err
	}
	out := []ComposeFile{}
	for _, f := range p.ConfigFiles {
		out = append(out, ComposeFile{Path: f, Kind: "compose", Exists: true})
	}
	if p.WorkingDir != "" {
		env := path.Join(p.WorkingDir, ".env")
		r, err := c.hub.Exec(ctx, w, host, "test -e "+shq(env)+" || sudo -n test -e "+shq(env), nil, 1024)
		out = append(out, ComposeFile{Path: env, Kind: "env", Exists: err == nil && r.ExitCode == 0})
	}
	return out, nil
}

func (c *Client) composeFile(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, project, file string) (ComposeProject, ComposeFile, error) {
	p, err := c.composeProject(ctx, w, host, project)
	if err != nil {
		return p, ComposeFile{}, err
	}
	files, err := c.ComposeFiles(ctx, w, host, project)
	if err != nil {
		return p, ComposeFile{}, err
	}
	for _, f := range files {
		if f.Path == file {
			return p, f, nil
		}
	}
	return p, ComposeFile{}, fmt.Errorf("%s is not a file of compose project %s", file, project)
}

// ReadHostFile reads a text file on the host, with sudo -n when the SSH
// user may not read it.
func (c *Client) ReadHostFile(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, file string, max int64) (string, error) {
	content, trunc, err := c.hub.ReadFile(ctx, w, host, file, max)
	if err != nil {
		res, err2 := c.hub.Exec(ctx, w, host, "sudo -n head -c "+fmt.Sprint(max+1)+" -- "+shq(file), nil, int(max)+1024)
		if err2 != nil || res.ExitCode != 0 {
			return "", err
		}
		content, trunc = res.Stdout, int64(len(res.Stdout)) > max
	}
	if trunc {
		return "", fmt.Errorf("%s is larger than %d bytes", file, max)
	}
	return content, nil
}

// ComposeReadFile returns a file of a project ("" when a .env does not exist).
func (c *Client) ComposeReadFile(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, project, file string) (string, error) {
	_, f, err := c.composeFile(ctx, w, host, project, file)
	if err != nil {
		return "", err
	}
	if !f.Exists {
		return "", nil
	}
	return c.ReadHostFile(ctx, w, host, file, 1<<20)
}

// ComposeWriteFile validates new content for a project file with
// "docker compose config" (together with the project's other files) and
// only then replaces the file, keeping a backup next to it. With deploy it
// then runs "up -d" for the project.
func (c *Client) ComposeWriteFile(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, project, file, content string, deploy bool) (string, error) {
	p, f, err := c.composeFile(ctx, w, host, project, file)
	if err != nil {
		return "", err
	}
	out, err := c.writeValidated(ctx, w, host, p, f, content)
	if err != nil || !deploy {
		return out, err
	}
	up, err := c.ComposeAction(ctx, w, host, project, "up", nil)
	return out + up, err
}

// ComposeCreate creates a new project: dir/compose.yaml (validated), then up -d.
func (c *Client) ComposeCreate(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, name, dir, content string, start bool) (string, error) {
	if !ValidID(name) || strings.ToLower(name) != name {
		return "", fmt.Errorf("invalid project name (lowercase letters, digits, - and _)")
	}
	if !strings.HasPrefix(dir, "/") || strings.Contains(dir, "..") {
		return "", errors.New("the directory must be an absolute path")
	}
	dir = path.Clean(dir)
	if list, err := c.ComposeProjects(ctx, w, host); err == nil {
		for _, p := range list {
			if p.Name == name {
				return "", fmt.Errorf("a compose project %s already exists on %s", name, host.Name)
			}
		}
	}
	file := path.Join(dir, "compose.yaml")
	if r, err := c.hub.Exec(ctx, w, host, "test -e "+shq(file)+" || sudo -n test -e "+shq(file), nil, 1024); err == nil && r.ExitCode == 0 {
		return "", fmt.Errorf("%s already exists", file)
	}
	p := ComposeProject{Name: name, ConfigFiles: []string{file}, WorkingDir: dir}
	out, err := c.writeValidated(ctx, w, host, p, ComposeFile{Path: file, Kind: "compose"}, content)
	if err != nil || !start {
		return out, err
	}
	pre, err := c.Prefix(ctx, w, host)
	if err != nil {
		return out, err
	}
	res, err := c.hub.Exec(ctx, w, host, composeBase(pre, p, "")+" up -d --remove-orphans 2>&1", nil, 4<<20)
	if err != nil {
		return out, err
	}
	if res.ExitCode != 0 {
		return out + res.Stdout, fmt.Errorf("docker compose up failed (exit %d):\n%s", res.ExitCode, tailLines(res.Stdout, 40))
	}
	return out + res.Stdout, nil
}

// writeValidated writes content to f through a temporary file in the same
// directory, validated with docker compose config; the file keeps its owner
// and mode, and the old version stays as <file>.opsdeck-bak-<time>.
func (c *Client) writeValidated(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, p ComposeProject, f ComposeFile, content string) (string, error) {
	pre, err := c.Prefix(ctx, w, host)
	if err != nil {
		return "", err
	}
	dir := path.Dir(f.Path)
	check := "docker compose --ansi never -p " + p.Name + " --project-directory " + shq(p.WorkingDir)
	for _, cf := range p.ConfigFiles {
		if cf == f.Path {
			check += ` -f "$tmp"`
		} else {
			check += " -f " + shq(cf)
		}
	}
	if f.Kind == "env" {
		check += ` --env-file "$tmp"`
	}
	check += " config -q"
	script := `set -e
mkdir -p ` + shq(dir) + `
tmp=$(mktemp ` + shq(dir) + `/.opsdeck-compose.XXXXXX)
trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
` + check + `
f=` + shq(f.Path) + `
if [ -e "$f" ]; then cp -p "$f" "$f.opsdeck-bak-` + time.Now().Format("20060102-150405") + `"; cat "$tmp" > "$f"; else chmod 644 "$tmp"; mv "$tmp" "$f"; fi
echo "saved $f"`
	run := func(sudo bool) (connect.ExecResult, error) {
		cmd := "sh -c " + shq(script)
		if sudo {
			cmd = "sudo -n " + cmd
		}
		return c.hub.Exec(ctx, w, host, cmd, strings.NewReader(content), 1<<20)
	}
	res, err := run(pre != "docker ")
	if err == nil && res.ExitCode != 0 && pre == "docker " && strings.Contains(res.Stderr, "Permission denied") {
		res, err = run(true)
	}
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("not saved: %s", strings.TrimSpace(res.Stderr+"\n"+res.Stdout))
	}
	return res.Stdout, nil
}
