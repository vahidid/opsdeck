package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/docker"
	"opsdeck/internal/llm"
)

// dockerRisk is the risk class of each docker tool action.
var dockerRisk = map[string]string{
	"ps": RiskRead, "images": RiskRead, "stats": RiskRead, "inspect": RiskRead, "logs": RiskRead, "top": RiskRead, "info": RiskRead,
	"df": RiskRead, "networks": RiskRead, "volumes": RiskRead, "volume_ls": RiskRead, "volume_read": RiskRead,
	"compose_ls": RiskRead, "compose_ps": RiskRead, "compose_logs": RiskRead, "compose_file": RiskRead,
	"services": RiskRead, "service_ps": RiskRead, "service_logs": RiskRead, "stacks": RiskRead, "stack_ps": RiskRead, "nodes": RiskRead,

	"start": RiskChange, "stop": RiskChange, "restart": RiskChange, "pause": RiskChange, "unpause": RiskChange, "kill": RiskChange,
	"compose_up": RiskChange, "compose_recreate": RiskChange, "compose_pull": RiskChange, "compose_restart": RiskChange,
	"compose_stop": RiskChange, "compose_start": RiskChange, "compose_write": RiskChange,
	"service_scale": RiskChange, "service_redeploy": RiskChange, "service_rollback": RiskChange, "service_image": RiskChange,
	"stack_deploy": RiskChange, "image_pull": RiskChange, "volume_write": RiskChange, "node_availability": RiskChange,

	"rm": RiskDestructive, "compose_down": RiskDestructive, "compose_down_volumes": RiskDestructive, "service_rm": RiskDestructive,
	"stack_rm": RiskDestructive, "volume_rm": RiskDestructive, "volume_delete": RiskDestructive, "image_rm": RiskDestructive,
	"network_rm": RiskDestructive, "prune": RiskDestructive,
}

func dockerActions() []string {
	order := []string{"ps", "images", "stats", "inspect", "logs", "top", "info", "df", "networks", "volumes", "volume_ls", "volume_read",
		"compose_ls", "compose_ps", "compose_logs", "compose_file", "services", "service_ps", "service_logs", "stacks", "stack_ps", "nodes",
		"start", "stop", "restart", "pause", "unpause", "kill", "compose_up", "compose_recreate", "compose_pull", "compose_restart",
		"compose_stop", "compose_start", "compose_write", "service_scale", "service_redeploy", "service_rollback", "service_image",
		"stack_deploy", "image_pull", "volume_write", "node_availability",
		"rm", "compose_down", "compose_down_volumes", "service_rm", "stack_rm", "volume_rm", "volume_delete", "image_rm", "network_rm", "prune"}
	return order
}

func toolDocker() Tool {
	return Tool{
		Def: llm.Tool{Name: "docker", Description: `Manage Docker on a host over SSH (sudo -n is used when the SSH user is not in the docker group).
Containers: ps, inspect, logs (tail, since), top, stats, start, stop, restart, pause, unpause, kill, rm.
Compose projects: compose_ls, compose_ps, compose_logs (service optional), compose_file (read a compose file or .env: file), compose_up, compose_recreate, compose_pull, compose_restart, compose_stop, compose_start (services optional), compose_write (replace a compose file or .env after validating it with docker compose config; deploy=true then runs up -d), compose_down, compose_down_volumes (also deletes the project's volumes).
Swarm (on a manager): services, service_ps, service_logs, service_scale (replicas), service_redeploy, service_rollback, service_image (image), service_rm, stacks, stack_ps, stack_deploy (stack + file on the host or content), stack_rm, nodes, node_availability (node, availability).
Volumes: volumes, volume_ls / volume_read (path inside the volume), volume_write (path + content), volume_delete (path), volume_rm.
Engine: info, df, images, image_pull, image_rm, networks, network_rm, prune (kind: containers, images, images-unused, build-cache, networks, volumes, system).
For anything else (build, run, exec) use exec with the docker CLI.`,
			Schema: obj(map[string]any{
				"host":         str("Host name"),
				"action":       enum("Action", dockerActions()...),
				"container":    str("Container name or id"),
				"project":      str("Compose project name"),
				"services":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Compose services (default: all)"},
				"service":      str("Swarm service name or id; for compose_logs, one compose service"),
				"stack":        str("Swarm stack name"),
				"volume":       str("Volume name"),
				"path":         str("Path inside the volume (volume_ls, volume_read, volume_write, volume_delete)"),
				"file":         str("compose_file / compose_write: absolute path of the compose file or .env (see compose_ls); stack_deploy: absolute path of a compose file on the host"),
				"content":      str("New content (compose_write, volume_write, stack_deploy without file)"),
				"deploy":       map[string]any{"type": "boolean", "description": "compose_write: run up -d after saving"},
				"image":        str("Image reference (image_pull, image_rm, service_image)"),
				"replicas":     num("service_scale: number of replicas"),
				"node":         str("Swarm node id or hostname"),
				"availability": enum("node_availability", "active", "pause", "drain"),
				"network":      str("Network name or id"),
				"kind":         enum("prune: what to remove", "containers", "images", "images-unused", "build-cache", "networks", "volumes", "system"),
				"tail":         num("Log lines (default 200, max 5000)"),
				"since":        str("Logs since: 15m, 2h, 1d or an RFC3339 time"),
			}, "host", "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Host, Action, Container, Project, Service, Stack, Volume, Path, File, Content, Image, Node, Availability, Network, Kind, Since string
				Services                                                                                                                       []string
				Deploy                                                                                                                         bool
				Replicas, Tail                                                                                                                 int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			risk, ok := dockerRisk[in.Action]
			if !ok {
				return nil, fmt.Errorf("unknown action %q", in.Action)
			}
			h, err := env.host(in.Host)
			if err != nil {
				return nil, err
			}
			need := func(v, what string) error {
				if !docker.ValidRef(v) {
					return fmt.Errorf("%s: a valid %s is required", in.Action, what)
				}
				return nil
			}
			var subject string
			switch {
			case in.Action == "ps" || in.Action == "images" || in.Action == "stats" || in.Action == "info" || in.Action == "df" ||
				in.Action == "networks" || in.Action == "volumes" || in.Action == "compose_ls" || in.Action == "services" ||
				in.Action == "stacks" || in.Action == "nodes":
			case strings.HasPrefix(in.Action, "compose_"):
				err, subject = need(in.Project, "project"), in.Project
			case in.Action == "service_logs" || in.Action == "service_ps" || strings.HasPrefix(in.Action, "service_"):
				err, subject = need(in.Service, "service"), in.Service
			case strings.HasPrefix(in.Action, "stack_"):
				err, subject = need(in.Stack, "stack"), in.Stack
			case strings.HasPrefix(in.Action, "volume_"):
				err, subject = need(in.Volume, "volume"), in.Volume+":/"+strings.TrimPrefix(in.Path, "/")
			case in.Action == "image_pull" || in.Action == "image_rm":
				err, subject = need(in.Image, "image"), in.Image
			case in.Action == "network_rm":
				err, subject = need(in.Network, "network"), in.Network
			case in.Action == "node_availability":
				err, subject = need(in.Node, "node"), in.Node+" "+in.Availability
			case in.Action == "prune":
				if _, ok := docker.PruneKinds[in.Kind]; !ok {
					err = errors.New("prune: kind is required")
				}
				subject = in.Kind
			default: // container actions
				err, subject = need(in.Container, "container name or id"), in.Container
			}
			if err != nil {
				return nil, err
			}
			envName, protected := targetInfo(env.WS, h)
			preview := fmt.Sprintf("docker %s %s on %s", in.Action, subject, h.Name)
			if len(in.Services) > 0 {
				preview += "\nservices: " + strings.Join(in.Services, ", ")
			}
			switch in.Action {
			case "compose_write":
				before, _ := env.Docker.ComposeReadFile(ctx, env.WS, h, in.Project, in.File)
				preview += "\n\n" + unifiedDiff(in.File, before, in.Content)
				if in.Deploy {
					preview += "\nthen: docker compose up -d"
				}
			case "volume_write":
				preview += "\n\n" + truncate(in.Content, 8000)
			case "stack_deploy":
				if in.File != "" {
					preview += "\ncompose file: " + in.File
				} else {
					preview += "\n\n" + truncate(in.Content, 12000)
				}
			case "service_scale":
				preview += fmt.Sprintf(" → %d replicas", in.Replicas)
			case "service_image":
				preview += " → " + in.Image
			}
			return &Call{Risk: risk, Target: "host/" + h.Name, Environment: envName, Protected: protected,
				Match: "docker " + in.Action + " " + subject, Preview: preview, Credentials: env.creds(h),
				Run: func(ctx context.Context) (string, error) {
					timeout := 2 * time.Minute
					if in.Action == "compose_up" || in.Action == "compose_recreate" || in.Action == "compose_pull" || in.Action == "image_pull" ||
						in.Action == "stack_deploy" || in.Action == "compose_write" || in.Action == "prune" {
						timeout = 15 * time.Minute
					}
					ctx, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()
					return runDocker(ctx, env, h.Name, in.Action, dockerInput{
						Container: in.Container, Project: in.Project, Services: in.Services, Service: in.Service, Stack: in.Stack,
						Volume: in.Volume, Path: in.Path, File: in.File, Content: in.Content, Deploy: in.Deploy, Image: in.Image,
						Replicas: in.Replicas, Node: in.Node, Availability: in.Availability, Network: in.Network, Kind: in.Kind,
						Tail: in.Tail, Since: in.Since})
				}}, nil
		},
	}
}

type dockerInput struct {
	Container, Project, Service, Stack, Volume, Path, File, Content, Image, Node, Availability, Network, Kind, Since string
	Services                                                                                                         []string
	Deploy                                                                                                           bool
	Replicas, Tail                                                                                                   int
}

func runDocker(ctx context.Context, env *Env, hostName, action string, in dockerInput) (string, error) {
	h, err := env.host(hostName)
	if err != nil {
		return "", err
	}
	d, w := env.Docker, env.WS
	asJSON := func(v any, err error) (string, error) {
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(v, "", " ")
		return truncate(string(b), 30000), nil
	}
	text := func(out string, err error) (string, error) {
		if err != nil {
			if out != "" {
				return "", fmt.Errorf("%v\n%s", err, truncate(out, 8000))
			}
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			out = "ok"
		}
		return truncate(out, 30000), nil
	}
	logs := func(src docker.LogSource) (string, error) {
		src.Tail = in.Tail
		if src.Tail <= 0 || src.Tail > 5000 {
			src.Tail = 200
		}
		src.Since = in.Since
		cmd, err := d.LogsCommand(ctx, w, h, src)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		err = env.Hub.ExecStreams(ctx, w, h, cmd, func(stderr bool, line string) {
			l := docker.ParseLogLine(src.Kind, line, stderr)
			if l.Src != "" {
				b.WriteString(l.Src + " | ")
			}
			if l.TS != "" {
				b.WriteString(l.TS + " ")
			}
			if l.Stderr {
				b.WriteString("[stderr] ")
			}
			b.WriteString(l.Line + "\n")
		})
		if err != nil {
			return "", err
		}
		if b.Len() == 0 {
			return "(no log lines)", nil
		}
		return truncate(b.String(), 40000), nil
	}
	switch action {
	case "ps":
		return asJSON(d.Containers(ctx, w, h, true))
	case "images":
		return asJSON(d.Images(ctx, w, h))
	case "stats":
		return asJSON(d.Stats(ctx, w, h))
	case "info":
		return asJSON(d.Info(ctx, w, h))
	case "df":
		return asJSON(d.DiskUsage(ctx, w, h))
	case "networks":
		return asJSON(d.Networks(ctx, w, h))
	case "volumes":
		return asJSON(d.Volumes(ctx, w, h))
	case "inspect":
		raw, err := d.Inspect(ctx, w, h, in.Container)
		return truncate(string(raw), 30000), err
	case "top":
		return asJSON(d.Top(ctx, w, h, in.Container))
	case "logs":
		return logs(docker.LogSource{Kind: "container", Target: in.Container})
	case "start", "stop", "restart", "pause", "unpause", "kill", "rm":
		if err := d.Action(ctx, w, h, in.Container, action); err != nil {
			return "", err
		}
		return action + " " + in.Container + ": ok", nil
	case "compose_ls":
		return asJSON(d.ComposeProjects(ctx, w, h))
	case "compose_ps":
		return asJSON(d.ComposePS(ctx, w, h, in.Project))
	case "compose_logs":
		return logs(docker.LogSource{Kind: "compose", Target: in.Project, Service: in.Service})
	case "compose_file":
		files, err := d.ComposeFiles(ctx, w, h, in.Project)
		if err != nil {
			return "", err
		}
		if in.File == "" {
			return asJSON(files, nil)
		}
		return text(d.ComposeReadFile(ctx, w, h, in.Project, in.File))
	case "compose_write":
		return text(d.ComposeWriteFile(ctx, w, h, in.Project, in.File, in.Content, in.Deploy))
	case "compose_up", "compose_recreate", "compose_pull", "compose_restart", "compose_stop", "compose_start", "compose_down", "compose_down_volumes":
		a := strings.ReplaceAll(strings.TrimPrefix(action, "compose_"), "_", "-")
		return text(d.ComposeAction(ctx, w, h, in.Project, a, in.Services))
	case "services":
		return asJSON(d.Services(ctx, w, h))
	case "service_ps":
		return asJSON(d.Tasks(ctx, w, h, in.Service, false))
	case "service_logs":
		return logs(docker.LogSource{Kind: "service", Target: in.Service})
	case "service_scale":
		return text(d.ServiceAction(ctx, w, h, in.Service, "scale", strconv.Itoa(in.Replicas)))
	case "service_redeploy", "service_rollback", "service_rm":
		return text(d.ServiceAction(ctx, w, h, in.Service, strings.TrimPrefix(action, "service_"), ""))
	case "service_image":
		return text(d.ServiceAction(ctx, w, h, in.Service, "image", in.Image))
	case "stacks":
		return asJSON(d.Stacks(ctx, w, h))
	case "stack_ps":
		return asJSON(d.Tasks(ctx, w, h, in.Stack, true))
	case "stack_deploy":
		if in.File == "" && strings.TrimSpace(in.Content) == "" {
			return "", errors.New("stack_deploy needs file (on the host) or content")
		}
		return text(d.StackDeploy(ctx, w, h, in.Stack, in.File, strings.NewReader(in.Content), false))
	case "stack_rm":
		return text(d.StackRemove(ctx, w, h, in.Stack))
	case "nodes":
		return asJSON(d.Nodes(ctx, w, h))
	case "node_availability":
		return text("", d.NodeAvailability(ctx, w, h, in.Node, in.Availability))
	case "volume_ls":
		mode, entries, err := d.VolumeList(ctx, w, h, in.Volume, in.Path)
		if err != nil {
			return "", err
		}
		return asJSON(map[string]any{"access": mode, "path": "/" + strings.TrimPrefix(in.Path, "/"), "entries": entries}, nil)
	case "volume_read":
		var buf bytes.Buffer
		if err := d.VolumeRead(ctx, w, h, in.Volume, in.Path, 200<<10, &buf); err != nil {
			return "", err
		}
		return truncate(buf.String(), 60000), nil
	case "volume_write":
		dir, name := splitVolPath(in.Path)
		return text("written "+in.Volume+":/"+strings.TrimPrefix(in.Path, "/"), d.VolumeWrite(ctx, w, h, in.Volume, dir, name, strings.NewReader(in.Content)))
	case "volume_delete":
		return text("deleted "+in.Volume+":/"+strings.TrimPrefix(in.Path, "/"), d.VolumeDelete(ctx, w, h, in.Volume, in.Path))
	case "volume_rm":
		return text("", d.VolumeRemove(ctx, w, h, in.Volume))
	case "image_pull":
		return text(d.ImagePull(ctx, w, h, in.Image))
	case "image_rm":
		return text("", d.ImageRemove(ctx, w, h, in.Image, false))
	case "network_rm":
		return text("", d.NetworkRemove(ctx, w, h, in.Network))
	case "prune":
		return text(d.Prune(ctx, w, h, in.Kind))
	}
	return "", fmt.Errorf("unknown action %q", action)
}

func splitVolPath(p string) (dir, name string) {
	p = strings.Trim(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}
