package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"opsdeck/internal/workspace"
)

// Info is a summary of the docker engine on a host.
type Info struct {
	ServerVersion string    `json:"serverVersion"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	CPUs          int       `json:"cpus"`
	MemTotal      int64     `json:"memTotal"`
	Containers    int       `json:"containers"`
	Running       int       `json:"running"`
	Paused        int       `json:"paused"`
	Stopped       int       `json:"stopped"`
	Images        int       `json:"images"`
	StorageDriver string    `json:"storageDriver"`
	RootDir       string    `json:"rootDir"`
	Swarm         SwarmInfo `json:"swarm"`
	Compose       string    `json:"compose,omitempty"` // plugin version, "" if missing
	Sudo          bool      `json:"sudo"`              // docker runs through sudo -n
	Warnings      []string  `json:"warnings,omitempty"`
}

func (c *Client) Info(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) (Info, error) {
	out, err := c.runOK(ctx, w, host, "info --format '{{json .}}'")
	if err != nil {
		return Info{}, err
	}
	var raw struct {
		ServerVersion, OperatingSystem, Architecture, Driver, DockerRootDir string
		NCPU                                                                int
		MemTotal                                                            int64
		Containers, ContainersRunning, ContainersPaused, ContainersStopped  int
		Images                                                              int
		Warnings                                                            []string
		Swarm                                                               struct {
			NodeID, NodeAddr, LocalNodeState, Error string
			ControlAvailable                        bool
			Nodes, Managers                         int
		}
		ClientInfo struct {
			Plugins []struct{ Name, Version string }
		}
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return Info{}, fmt.Errorf("parse docker info: %w", err)
	}
	in := Info{ServerVersion: raw.ServerVersion, OS: raw.OperatingSystem, Arch: raw.Architecture, CPUs: raw.NCPU, MemTotal: raw.MemTotal,
		Containers: raw.Containers, Running: raw.ContainersRunning, Paused: raw.ContainersPaused, Stopped: raw.ContainersStopped,
		Images: raw.Images, StorageDriver: raw.Driver, RootDir: raw.DockerRootDir, Warnings: raw.Warnings,
		Swarm: SwarmInfo{State: raw.Swarm.LocalNodeState, Manager: raw.Swarm.ControlAvailable, NodeID: raw.Swarm.NodeID, NodeAddr: raw.Swarm.NodeAddr,
			Nodes: raw.Swarm.Nodes, Managers: raw.Swarm.Managers, Error: raw.Swarm.Error}}
	for _, p := range raw.ClientInfo.Plugins {
		if p.Name == "compose" {
			in.Compose = p.Version
		}
	}
	if in.Compose == "" {
		if r, err := c.Run(ctx, w, host, "compose version --short"); err == nil && r.ExitCode == 0 {
			in.Compose = strings.TrimSpace(r.Stdout)
		}
	}
	c.mu.Lock()
	in.Sudo = c.sudo[key(w, host)]
	c.mu.Unlock()
	return in, nil
}

// Usage is a row of docker system df.
type Usage struct {
	Type        string `json:"type"`
	Total       string `json:"total"`
	Active      string `json:"active"`
	Size        string `json:"size"`
	Reclaimable string `json:"reclaimable"`
}

func (c *Client) DiskUsage(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) ([]Usage, error) {
	out, err := c.runOK(ctx, w, host, "system df --format '{{json .}}'")
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	list := []Usage{}
	for _, r := range raw {
		list = append(list, Usage{Type: str(r["Type"]), Total: str(r["TotalCount"]), Active: str(r["Active"]), Size: str(r["Size"]), Reclaimable: str(r["Reclaimable"])})
	}
	return list, nil
}

// PruneKinds are what Prune can clean up.
var PruneKinds = map[string]string{
	"containers":    "container prune -f", // stopped containers
	"images":        "image prune -f",     // dangling images
	"images-unused": "image prune -af",    // every image no container uses
	"build-cache":   "builder prune -f",   // build cache
	"networks":      "network prune -f",   // unused networks
	"volumes":       "volume prune -af",   // every unused volume (data!)
	"system":        "system prune -f",    // stopped containers, unused networks, dangling images, cache
}

// Prune removes unused objects and returns docker's report.
func (c *Client) Prune(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, kind string) (string, error) {
	args, ok := PruneKinds[kind]
	if !ok {
		return "", fmt.Errorf("unknown prune kind %q", kind)
	}
	res, err := c.Run(ctx, w, host, args+" 2>&1")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 && kind == "volumes" && strings.Contains(res.Stdout, "unknown shorthand flag: 'a'") {
		// older engines: volume prune always meant every unused volume
		if res, err = c.Run(ctx, w, host, "volume prune -f 2>&1"); err != nil {
			return "", err
		}
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("docker prune: %s", strings.TrimSpace(res.Stdout))
	}
	return res.Stdout, nil
}

// ImagePull pulls an image and returns docker's output.
func (c *Client) ImagePull(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, ref string) (string, error) {
	if !ValidRef(ref) {
		return "", fmt.Errorf("invalid image reference")
	}
	res, err := c.Run(ctx, w, host, "pull -q "+ref+" 2>&1")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("docker pull: %s", strings.TrimSpace(res.Stdout))
	}
	return res.Stdout, nil
}

// ImageRemove removes an image (force also untags it from every repository).
func (c *Client) ImageRemove(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, ref string, force bool) error {
	if !ValidRef(ref) {
		return fmt.Errorf("invalid image reference")
	}
	args := "rmi " + ref
	if force {
		args = "rmi -f " + ref
	}
	_, err := c.runOK(ctx, w, host, args)
	return err
}

// Network is a docker network.
type Network struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Scope    string `json:"scope"`
	Internal bool   `json:"internal,omitempty"`
	Builtin  bool   `json:"builtin,omitempty"`
	Project  string `json:"project,omitempty"`
	Stack    string `json:"stack,omitempty"`
	Created  string `json:"created"`
}

func (c *Client) Networks(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) ([]Network, error) {
	out, err := c.runOK(ctx, w, host, "network ls --format '{{json .}}'")
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	list := []Network{}
	for _, r := range raw {
		l := labels(str(r["Labels"]))
		n := Network{ID: str(r["ID"]), Name: str(r["Name"]), Driver: str(r["Driver"]), Scope: str(r["Scope"]), Internal: str(r["Internal"]) == "true",
			Project: l["com.docker.compose.project"], Stack: l["com.docker.stack.namespace"], Created: str(r["CreatedAt"])}
		switch n.Name {
		case "bridge", "host", "none", "ingress", "docker_gwbridge":
			n.Builtin = true
		}
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Builtin != list[j].Builtin {
			return !list[i].Builtin
		}
		return list[i].Name < list[j].Name
	})
	return list, nil
}

// NetworkRemove removes a network.
func (c *Client) NetworkRemove(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid network")
	}
	_, err := c.runOK(ctx, w, host, "network rm "+id)
	return err
}
