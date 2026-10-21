// Package docker manages containers on workspace hosts over SSH using the
// docker CLI already installed there (no daemon socket exposure needed).
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"opsdeck/internal/connect"
	"opsdeck/internal/workspace"
)

type Client struct {
	hub *connect.Hub

	mu   sync.Mutex
	sudo map[string]bool // ws/host -> needs "sudo -n"
}

func New(hub *connect.Hub) *Client { return &Client{hub: hub, sudo: map[string]bool{}} }

var idRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.\-]{0,127}$`)

// ValidID checks a container/image reference so it is safe to put in a command.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Run executes "docker <args>" on the host, retrying with sudo -n on permission errors.
func (c *Client) Run(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, args string) (connect.ExecResult, error) {
	key := w.Name() + "/" + host.Name
	c.mu.Lock()
	sudo := c.sudo[key]
	c.mu.Unlock()
	prefix := "docker "
	if sudo {
		prefix = "sudo -n docker "
	}
	res, err := c.hub.Exec(ctx, w, host, prefix+args, nil, 4<<20)
	if err != nil {
		return res, err
	}
	if !sudo && res.ExitCode != 0 && strings.Contains(res.Stderr, "permission denied") {
		res2, err := c.hub.Exec(ctx, w, host, "sudo -n docker "+args, nil, 4<<20)
		if err == nil && res2.ExitCode == 0 {
			c.mu.Lock()
			c.sudo[key] = true
			c.mu.Unlock()
			return res2, nil
		}
	}
	if res.ExitCode == 127 {
		return res, fmt.Errorf("docker is not installed on %s", host.Name)
	}
	return res, nil
}

func (c *Client) runOK(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, args string) (string, error) {
	res, err := c.Run(ctx, w, host, args)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("docker %s: %s", strings.Fields(args)[0], strings.TrimSpace(res.Stderr+" "+res.Stdout))
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

type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Ports   string `json:"ports"`
	Created string `json:"created"`
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
		list = append(list, Container{ID: id, Name: str(r["Names"]), Image: str(r["Image"]), State: str(r["State"]),
			Status: str(r["Status"]), Ports: str(r["Ports"]), Created: str(r["CreatedAt"])})
	}
	return list, nil
}

type Image struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Size       string `json:"size"`
	Created    string `json:"created"`
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
		list = append(list, Image{ID: str(r["ID"]), Repository: str(r["Repository"]), Tag: str(r["Tag"]),
			Size: str(r["Size"]), Created: str(r["CreatedSince"])})
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
}

func (c *Client) Stats(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) ([]Stat, error) {
	out, err := c.runOK(ctx, w, host, "stats --no-stream --format '{{json .}}'")
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
			MemPct: str(r["MemPerc"]), Net: str(r["NetIO"]), Block: str(r["BlockIO"])})
	}
	return list, nil
}

func (c *Client) Inspect(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, id string) (json.RawMessage, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid container id")
	}
	out, err := c.runOK(ctx, w, host, "inspect "+id)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// Action runs start|stop|restart|rm on a container.
func (c *Client) Action(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, id, action string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid container id")
	}
	switch action {
	case "start", "stop", "restart", "rm", "pause", "unpause":
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
	_, err := c.runOK(ctx, w, host, action+" "+id)
	return err
}

// LogsCommand returns the shell command that prints container logs.
func (c *Client) LogsCommand(w *workspace.Workspace, host *workspace.Resource, id string, lines int, follow bool) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid container id")
	}
	c.mu.Lock()
	sudo := c.sudo[w.Name()+"/"+host.Name]
	c.mu.Unlock()
	cmd := fmt.Sprintf("docker logs --timestamps --tail %d", lines)
	if sudo {
		cmd = "sudo -n " + cmd
	}
	if follow {
		cmd += " -f"
	}
	return cmd + " " + id + " 2>&1", nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
