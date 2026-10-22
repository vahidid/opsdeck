package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"opsdeck/internal/workspace"
)

// SwarmInfo is the host's view of Docker Swarm.
type SwarmInfo struct {
	State    string `json:"state"`   // inactive | active | pending | error | locked
	Manager  bool   `json:"manager"` // this node can manage the swarm
	NodeID   string `json:"nodeId,omitempty"`
	NodeAddr string `json:"nodeAddr,omitempty"`
	Nodes    int    `json:"nodes"`
	Managers int    `json:"managers"`
	Error    string `json:"error,omitempty"`
}

func (c *Client) Swarm(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) (SwarmInfo, error) {
	out, err := c.runOK(ctx, w, host, "info --format '{{json .Swarm}}'")
	if err != nil {
		return SwarmInfo{}, err
	}
	var raw struct {
		NodeID, NodeAddr, LocalNodeState, Error string
		ControlAvailable                        bool
		Nodes, Managers                         int
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return SwarmInfo{}, fmt.Errorf("parse docker info: %w", err)
	}
	return SwarmInfo{State: raw.LocalNodeState, Manager: raw.ControlAvailable, NodeID: raw.NodeID, NodeAddr: raw.NodeAddr,
		Nodes: raw.Nodes, Managers: raw.Managers, Error: raw.Error}, nil
}

// Service is a swarm service.
type Service struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	Replicas string `json:"replicas"` // "2/3"
	Running  int    `json:"running"`
	Desired  int    `json:"desired"`
	Image    string `json:"image"`
	Ports    string `json:"ports,omitempty"`
	Stack    string `json:"stack,omitempty"`
}

var replRe = regexp.MustCompile(`^(\d+)/(\d+)`)

func (c *Client) Services(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) ([]Service, error) {
	out, err := c.runOK(ctx, w, host, "service ls --format '{{json .}}'")
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	stacks, _ := c.Stacks(ctx, w, host)
	list := []Service{}
	for _, r := range raw {
		s := Service{ID: str(r["ID"]), Name: str(r["Name"]), Mode: str(r["Mode"]), Replicas: str(r["Replicas"]), Image: str(r["Image"]), Ports: str(r["Ports"])}
		if m := replRe.FindStringSubmatch(s.Replicas); m != nil {
			s.Running, _ = strconv.Atoi(m[1])
			s.Desired, _ = strconv.Atoi(m[2])
		}
		for _, st := range stacks {
			if strings.HasPrefix(s.Name, st.Name+"_") {
				s.Stack = st.Name
			}
		}
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Stack+"/"+list[i].Name < list[j].Stack+"/"+list[j].Name })
	return list, nil
}

// Task is a swarm task (a replica of a service on a node).
type Task struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Image        string `json:"image"`
	Node         string `json:"node"`
	DesiredState string `json:"desiredState"`
	CurrentState string `json:"currentState"`
	Error        string `json:"error,omitempty"`
	Ports        string `json:"ports,omitempty"`
}

// Tasks lists the tasks of a service or (stack=true) of a stack.
func (c *Client) Tasks(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, name string, stack bool) ([]Task, error) {
	if !ValidID(name) {
		return nil, fmt.Errorf("invalid name")
	}
	args := "service ps --no-trunc --format '{{json .}}' " + name
	if stack {
		args = "stack ps --no-trunc --format '{{json .}}' " + name
	}
	out, err := c.runOK(ctx, w, host, args)
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	list := []Task{}
	for _, r := range raw {
		id := str(r["ID"])
		if len(id) > 12 {
			id = id[:12]
		}
		list = append(list, Task{ID: id, Name: strings.TrimLeft(str(r["Name"]), `\_ `), Image: str(r["Image"]), Node: str(r["Node"]),
			DesiredState: str(r["DesiredState"]), CurrentState: str(r["CurrentState"]), Error: str(r["Error"]), Ports: str(r["Ports"])})
	}
	return list, nil
}

// ServiceAction changes a service: scale (arg = replicas), redeploy (update
// --force), rollback, image (arg = new image) or rm.
func (c *Client) ServiceAction(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, name, action, arg string) (string, error) {
	if !ValidID(name) {
		return "", fmt.Errorf("invalid service")
	}
	var args string
	switch action {
	case "scale":
		n, err := strconv.Atoi(arg)
		if err != nil || n < 0 || n > 1000 {
			return "", fmt.Errorf("replicas must be a number between 0 and 1000")
		}
		args = "service scale --detach " + name + "=" + strconv.Itoa(n)
	case "redeploy":
		args = "service update --detach --force " + name
	case "rollback":
		args = "service rollback --detach " + name
	case "image":
		if !ValidRef(arg) {
			return "", fmt.Errorf("invalid image reference")
		}
		args = "service update --detach --with-registry-auth --image " + arg + " " + name
	case "rm":
		args = "service rm " + name
	default:
		return "", fmt.Errorf("unsupported service action %q", action)
	}
	res, err := c.Run(ctx, w, host, args+" 2>&1")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("docker %s: %s", action, strings.TrimSpace(res.Stdout))
	}
	return res.Stdout, nil
}

// Stack is a swarm stack.
type Stack struct {
	Name     string `json:"name"`
	Services int    `json:"services"`
}

func (c *Client) Stacks(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) ([]Stack, error) {
	out, err := c.runOK(ctx, w, host, "stack ls --format '{{json .}}'")
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	list := []Stack{}
	for _, r := range raw {
		n, _ := strconv.Atoi(str(r["Services"]))
		list = append(list, Stack{Name: str(r["Name"]), Services: n})
	}
	return list, nil
}

// StackDeploy deploys (or updates) a stack from a compose file on the host
// (file) or from content sent over stdin.
func (c *Client) StackDeploy(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, name, file string, content io.Reader, prune bool) (string, error) {
	if !ValidID(name) {
		return "", fmt.Errorf("invalid stack name")
	}
	src := "-"
	if file != "" {
		if !strings.HasPrefix(file, "/") || strings.Contains(file, "..") {
			return "", fmt.Errorf("the compose file must be an absolute path")
		}
		src, content = shq(file), nil
	}
	args := "stack deploy --detach=true --with-registry-auth -c " + src
	if prune {
		args += " --prune"
	}
	res, err := c.RunInput(ctx, w, host, args+" "+name+" 2>&1", content)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 && strings.Contains(res.Stdout, "unknown flag: --detach") {
		res, err = c.RunInput(ctx, w, host, strings.Replace(args, " --detach=true", "", 1)+" "+name+" 2>&1", content)
		if err != nil {
			return "", err
		}
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("docker stack deploy failed:\n%s", tailLines(res.Stdout, 40))
	}
	return res.Stdout, nil
}

// StackRemove removes a stack and its services and networks.
func (c *Client) StackRemove(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, name string) (string, error) {
	if !ValidID(name) {
		return "", fmt.Errorf("invalid stack name")
	}
	res, err := c.Run(ctx, w, host, "stack rm "+name+" 2>&1")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("docker stack rm: %s", strings.TrimSpace(res.Stdout))
	}
	return res.Stdout, nil
}

// Node is a swarm node.
type Node struct {
	ID            string `json:"id"`
	Hostname      string `json:"hostname"`
	Status        string `json:"status"`
	Availability  string `json:"availability"`
	ManagerStatus string `json:"managerStatus,omitempty"`
	EngineVersion string `json:"engineVersion,omitempty"`
	Self          bool   `json:"self,omitempty"`
}

func (c *Client) Nodes(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) ([]Node, error) {
	out, err := c.runOK(ctx, w, host, "node ls --format '{{json .}}'")
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	list := []Node{}
	for _, r := range raw {
		self, _ := r["Self"].(bool)
		list = append(list, Node{ID: str(r["ID"]), Hostname: str(r["Hostname"]), Status: str(r["Status"]), Availability: str(r["Availability"]),
			ManagerStatus: str(r["ManagerStatus"]), EngineVersion: str(r["EngineVersion"]), Self: self})
	}
	return list, nil
}

// NodeAvailability sets a node to active, pause or drain.
func (c *Client) NodeAvailability(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, id, availability string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid node")
	}
	switch availability {
	case "active", "pause", "drain":
	default:
		return fmt.Errorf("availability must be active, pause or drain")
	}
	_, err := c.runOK(ctx, w, host, "node update --availability "+availability+" "+id)
	return err
}
