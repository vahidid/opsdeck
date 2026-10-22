// Package docker manages containers on workspace hosts over SSH using the
// docker CLI already installed there (no daemon socket exposure needed).
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"opsdeck/internal/connect"
	"opsdeck/internal/workspace"
)

type Client struct {
	hub *connect.Hub

	mu    sync.Mutex
	sudo  map[string]bool // ws/host -> needs "sudo -n"
	known map[string]bool // ws/host -> sudo need was probed
	vol   map[string]volAccess
}

func New(hub *connect.Hub) *Client {
	return &Client{hub: hub, sudo: map[string]bool{}, known: map[string]bool{}, vol: map[string]volAccess{}}
}

// Hub is the connection hub the client runs commands through.
func (c *Client) Hub() *connect.Hub { return c.hub }

var idRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.\-]{0,127}$`)

// ValidID checks a container/image reference so it is safe to put in a command.
func ValidID(id string) bool { return idRe.MatchString(id) }

var refRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.\-/:@]{0,254}$`)

// ValidRef checks an image reference (registry/name:tag@digest).
func ValidRef(ref string) bool { return refRe.MatchString(ref) && !strings.Contains(ref, "..") }

func key(w *workspace.Workspace, host *workspace.Resource) string { return w.Name() + "/" + host.Name }

// Prefix returns "docker " or "sudo -n docker " for the host, probing once
// whether the SSH user may talk to the daemon directly.
func (c *Client) Prefix(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) (string, error) {
	k := key(w, host)
	c.mu.Lock()
	known, sudo := c.known[k], c.sudo[k]
	c.mu.Unlock()
	if known {
		return prefix(sudo), nil
	}
	res, err := c.hub.Exec(ctx, w, host, "docker version --format '{{.Server.Version}}'", nil, 64<<10)
	if err != nil {
		return "", err
	}
	switch {
	case res.ExitCode == 127:
		return "", fmt.Errorf("docker is not installed on %s", host.Name)
	case res.ExitCode != 0 && strings.Contains(strings.ToLower(res.Stderr), "permission denied"):
		r2, err := c.hub.Exec(ctx, w, host, "sudo -n docker version --format '{{.Server.Version}}'", nil, 64<<10)
		if err != nil {
			return "", err
		}
		if r2.ExitCode != 0 {
			return "", fmt.Errorf("the SSH user on %s may not use docker (not in the docker group, and sudo -n failed: %s)", host.Name, strings.TrimSpace(r2.Stderr))
		}
		sudo = true
	case res.ExitCode != 0:
		// daemon down or another error: report it, probe again next time
		return "", fmt.Errorf("docker on %s: %s", host.Name, strings.TrimSpace(res.Stderr+" "+res.Stdout))
	}
	c.mu.Lock()
	c.known[k], c.sudo[k] = true, sudo
	c.mu.Unlock()
	return prefix(sudo), nil
}

func prefix(sudo bool) string {
	if sudo {
		return "sudo -n docker "
	}
	return "docker "
}

// Run executes "docker <args>" on the host (with sudo -n when needed).
func (c *Client) Run(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, args string) (connect.ExecResult, error) {
	return c.RunInput(ctx, w, host, args, nil)
}

// RunInput is Run with stdin (e.g. a compose file for docker stack deploy -c -).
func (c *Client) RunInput(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, args string, stdin io.Reader) (connect.ExecResult, error) {
	p, err := c.Prefix(ctx, w, host)
	if err != nil {
		return connect.ExecResult{}, err
	}
	return c.hub.Exec(ctx, w, host, p+args, stdin, 4<<20)
}

func (c *Client) runOK(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, args string) (string, error) {
	res, err := c.Run(ctx, w, host, args)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("docker %s: %s", strings.Fields(args)[0], firstNonEmpty(strings.TrimSpace(res.Stderr), strings.TrimSpace(res.Stdout), fmt.Sprintf("exit code %d", res.ExitCode)))
	}
	return res.Stdout, nil
}

func jsonLines[T any](out string) ([]T, error) {
	list := []T{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			return nil, fmt.Errorf("parse docker output: %w", err)
		}
		list = append(list, v)
	}
	return list, nil
}

// Container is one row of docker ps.
type Container struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	State    string `json:"state"`
	Status   string `json:"status"`
	Health   string `json:"health,omitempty"` // healthy | unhealthy | starting
	Ports    string `json:"ports"`
	Created  string `json:"created"`
	Command  string `json:"command,omitempty"`
	Mounts   string `json:"mounts,omitempty"`
	Networks string `json:"networks,omitempty"`
	Project  string `json:"project,omitempty"` // compose project
	Service  string `json:"service,omitempty"` // compose service or swarm service
	Stack    string `json:"stack,omitempty"`   // swarm stack
}

// labels parses docker's "k=v,k2=v2" label column.
func labels(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func health(status string) string {
	switch {
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	}
	return ""
}

func (c *Client) Containers(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, all bool) ([]Container, error) {
	args := "ps --no-trunc --format '{{json .}}'"
	if all {
		args = "ps -a --no-trunc --format '{{json .}}'"
	}
	out, err := c.runOK(ctx, w, host, args)
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	list := []Container{}
	for _, r := range raw {
		id := str(r["ID"])
		if len(id) > 12 {
			id = id[:12]
		}
		l := labels(str(r["Labels"]))
		ct := Container{ID: id, Name: str(r["Names"]), Image: str(r["Image"]), State: str(r["State"]),
			Status: str(r["Status"]), Health: health(str(r["Status"])), Ports: str(r["Ports"]), Created: str(r["CreatedAt"]),
			Command: strings.Trim(str(r["Command"]), `"`), Mounts: str(r["Mounts"]), Networks: str(r["Networks"]),
			Project: l["com.docker.compose.project"], Service: l["com.docker.compose.service"], Stack: l["com.docker.stack.namespace"]}
		if ct.Service == "" {
			ct.Service = l["com.docker.swarm.service.name"]
		}
		list = append(list, ct)
	}
	return list, nil
}

type Image struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Size       string `json:"size"`
	Created    string `json:"created"`
	Containers string `json:"containers,omitempty"`
}

func (c *Client) Images(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) ([]Image, error) {
	out, err := c.runOK(ctx, w, host, "images --format '{{json .}}'")
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	list := []Image{}
	for _, r := range raw {
		n := str(r["Containers"])
		if n == "N/A" {
			n = ""
		}
		list = append(list, Image{ID: str(r["ID"]), Repository: str(r["Repository"]), Tag: str(r["Tag"]),
			Size: str(r["Size"]), Created: str(r["CreatedSince"]), Containers: n})
	}
	return list, nil
}

type Stat struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	CPU    string `json:"cpu"`
	Mem    string `json:"mem"`
	MemPct string `json:"memPct"`
	Net    string `json:"net"`
	Block  string `json:"block"`
	PIDs   string `json:"pids,omitempty"`
}

// Stats samples resource usage; ids limits it to some containers.
func (c *Client) Stats(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, ids ...string) ([]Stat, error) {
	args := "stats --no-stream --format '{{json .}}'"
	for _, id := range ids {
		if !ValidID(id) {
			return nil, fmt.Errorf("invalid container id")
		}
		args += " " + id
	}
	out, err := c.runOK(ctx, w, host, args)
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	list := []Stat{}
	for _, r := range raw {
		list = append(list, Stat{ID: str(r["ID"]), Name: str(r["Name"]), CPU: str(r["CPUPerc"]), Mem: str(r["MemUsage"]),
			MemPct: str(r["MemPerc"]), Net: str(r["NetIO"]), Block: str(r["BlockIO"]), PIDs: str(r["PIDs"])})
	}
	return list, nil
}

// Inspect returns docker inspect output for a container, image, volume,
// network, service or node (kind "" lets docker decide).
func (c *Client) Inspect(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, id string) (json.RawMessage, error) {
	return c.InspectKind(ctx, w, host, "", id)
}

func (c *Client) InspectKind(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, kind, id string) (json.RawMessage, error) {
	if !ValidRef(id) {
		return nil, fmt.Errorf("invalid id")
	}
	args := "inspect " + id
	switch kind {
	case "":
	case "container", "image", "volume", "network", "service", "node":
		args = kind + " inspect " + id
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	out, err := c.runOK(ctx, w, host, args)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// ContainerActions are the actions on a container.
var ContainerActions = []string{"start", "stop", "restart", "kill", "pause", "unpause", "rm"}

// Action runs start|stop|restart|kill|pause|unpause|rm on a container.
func (c *Client) Action(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, id, action string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid container id")
	}
	if !contains(ContainerActions, action) {
		return fmt.Errorf("unsupported action %q", action)
	}
	args := action + " " + id
	if action == "rm" {
		args = "rm -f " + id
	}
	_, err := c.runOK(ctx, w, host, args)
	return err
}

// Process is a row of docker top.
type Process struct {
	PID     string `json:"pid"`
	User    string `json:"user"`
	CPU     string `json:"cpu"`
	Mem     string `json:"mem"`
	Started string `json:"started"`
	Time    string `json:"time"`
	Command string `json:"command"`
}

// Top lists the processes of a running container.
func (c *Client) Top(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, id string) ([]Process, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid container id")
	}
	out, err := c.runOK(ctx, w, host, "top "+id+" -eo pid,user,pcpu,pmem,lstart,time,args")
	if err != nil {
		// minimal ps (busybox) does not know -o: fall back to docker's default columns
		if out2, err2 := c.runOK(ctx, w, host, "top "+id); err2 == nil {
			return parseTopDefault(out2), nil
		}
		return nil, err
	}
	list := []Process{}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 11 {
			continue
		}
		// lstart is 5 fields: Thu Oct 22 17:28:32 2026
		list = append(list, Process{PID: f[0], User: f[1], CPU: f[2], Mem: f[3], Started: strings.Join(f[4:9], " "), Time: f[9], Command: strings.Join(f[10:], " ")})
	}
	return list, nil
}

func parseTopDefault(out string) []Process {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	list := []Process{}
	if len(lines) == 0 {
		return list
	}
	head := strings.Fields(lines[0])
	col := func(name string) int {
		for i, h := range head {
			if strings.EqualFold(h, name) {
				return i
			}
		}
		return -1
	}
	pid, user, cmdCol := col("PID"), col("UID"), col("CMD")
	if cmdCol < 0 {
		cmdCol = col("COMMAND")
	}
	if user < 0 {
		user = col("USER")
	}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		p := Process{}
		if pid >= 0 && pid < len(f) {
			p.PID = f[pid]
		}
		if user >= 0 && user < len(f) {
			p.User = f[user]
		}
		if cmdCol >= 0 && cmdCol < len(f) {
			p.Command = strings.Join(f[cmdCol:], " ")
		}
		list = append(list, p)
	}
	return list
}

// ExecCommand is the command for an interactive shell in a container:
// bash when the image has it, else sh.
func (c *Client) ExecCommand(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, id, user, shell string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid container id")
	}
	p, err := c.Prefix(ctx, w, host)
	if err != nil {
		return "", err
	}
	cmd := p + "exec -it -e TERM=xterm-256color"
	if user != "" {
		if !ValidID(user) {
			return "", fmt.Errorf("invalid user")
		}
		cmd += " -u " + user
	}
	switch shell {
	case "", "auto":
		return cmd + " " + id + ` sh -c 'if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi'`, nil
	case "sh", "bash", "ash", "zsh":
		return cmd + " " + id + " " + shell, nil
	}
	return "", fmt.Errorf("unsupported shell %q", shell)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// shq quotes a value for the remote shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
