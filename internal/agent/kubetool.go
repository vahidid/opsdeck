package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"opsdeck/internal/kube"
	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// kubeRisk is the risk class of each kubernetes tool action (exec is
// classified per command).
var kubeRisk = map[string]string{
	"get": RiskRead, "describe": RiskRead, "yaml": RiskRead, "logs": RiskRead, "events": RiskRead, "top": RiskRead,
	"overview": RiskRead, "resources": RiskRead, "history": RiskRead, "rollout_status": RiskRead, "helm": RiskRead,
	"apply": RiskChange, "restart": RiskChange, "scale": RiskChange, "set_image": RiskChange, "pause": RiskChange, "resume": RiskChange,
	"undo": RiskChange, "suspend": RiskChange, "unsuspend": RiskChange, "trigger": RiskChange, "cordon": RiskChange, "uncordon": RiskChange,
	"exec":   RiskChange,
	"delete": RiskDestructive, "delete_pod": RiskDestructive, "drain": RiskDestructive,
}

func kubeActions() []string {
	return []string{"get", "describe", "yaml", "logs", "events", "top", "overview", "resources", "history", "rollout_status", "helm",
		"apply", "restart", "scale", "set_image", "pause", "resume", "undo", "suspend", "unsuspend", "trigger", "cordon", "uncordon", "exec",
		"delete", "delete_pod", "drain"}
}

func toolKubernetes() Tool {
	return Tool{
		Def: llm.Tool{Name: "kubernetes", Description: `Work with a workspace Kubernetes cluster (any resource type, custom resources included; type is a plural, kind, short name or resource.group, e.g. pods, deploy, ingresses, certificates.cert-manager.io).
Read: get (a table like kubectl get; name for one object; selector; wide), describe (object, its pods' states and its events), yaml (Secrets are masked), logs (a pod, or every pod of a deployment/statefulset/daemonset/job/service; container, tail, since, previous), events (namespace, or of one object), top (pods or nodes), overview (cluster health: problems, warnings, nodes), resources (the types the cluster serves), history (rollout revisions), rollout_status, helm (releases; with name: values, notes and revisions).
Change: apply (a YAML/JSON manifest, possibly several documents: server-side apply; the approval shows the server's dry-run diff), restart, scale (replicas), set_image (container, image), pause / resume (deployment rollout), undo (rollout back to revision, default the previous one), suspend / unsuspend / trigger (cronjob), cordon / uncordon (node), exec (command in a pod's container, classified like exec on hosts).
Destructive: delete (any object), delete_pod, drain (cordon and evict a node's pods).`,
			Schema: obj(map[string]any{
				"cluster":   str("KubernetesCluster resource name"),
				"action":    enum("Action", kubeActions()...),
				"type":      str("Resource type: pods, deployments, statefulsets, daemonsets, services, ingresses, configmaps, secrets, nodes, jobs, cronjobs, pvc, … or a custom resource"),
				"namespace": str("Namespace (default: the cluster's default; '*' = all namespaces for get, events, top, overview, helm)"),
				"name":      str("Object name"),
				"selector":  str("get: label selector, e.g. app=web"),
				"wide":      map[string]any{"type": "boolean", "description": "get: also the -o wide columns"},
				"container": str("logs, exec, set_image: container name"),
				"tail":      num("logs: lines per container (default 200)"),
				"since":     str("logs: 15m, 2h, 1d or an RFC3339 time"),
				"previous":  map[string]any{"type": "boolean", "description": "logs: the previous (crashed) container instance"},
				"replicas":  num("scale: replicas"),
				"image":     str("set_image: new image"),
				"revision":  num("undo: revision to roll back to (default: the previous one); helm: revision"),
				"manifest":  str("apply: YAML or JSON manifest"),
				"command":   str("exec: shell command, run with sh -c"),
			}, "cluster", "action")},
		Prepare: func(ctx context.Context, env *Env, input json.RawMessage) (*Call, error) {
			var in struct {
				Cluster, Action, Type, Namespace, Name, Selector, Container, Since, Image, Manifest, Command string
				Wide, Previous                                                                               bool
				Tail, Replicas, Revision                                                                     int
			}
			if err := decode(input, &in); err != nil {
				return nil, err
			}
			c := env.WS.Find("KubernetesCluster", strings.TrimPrefix(in.Cluster, "cluster/"))
			if c == nil {
				var names []string
				for _, r := range env.WS.Resources {
					if r.Kind == "KubernetesCluster" {
						names = append(names, r.Name)
					}
				}
				return nil, fmt.Errorf("cluster %q not found; clusters: %s", in.Cluster, strings.Join(names, ", "))
			}
			risk, ok := kubeRisk[in.Action]
			if !ok {
				return nil, fmt.Errorf("unknown action %q", in.Action)
			}
			switch in.Action {
			case "delete_pod":
				in.Action, in.Type = "delete", "pods"
			case "cordon", "uncordon", "drain":
				in.Type = "nodes"
			case "exec":
				in.Type = "pods"
			}
			needName := map[string]bool{"describe": true, "yaml": true, "logs": true, "history": true, "rollout_status": true, "restart": true,
				"scale": true, "set_image": true, "pause": true, "resume": true, "undo": true, "suspend": true, "unsuspend": true, "trigger": true,
				"cordon": true, "uncordon": true, "exec": true, "delete": true, "drain": true}
			if needName[in.Action] && in.Name == "" {
				return nil, fmt.Errorf("%s needs name", in.Action)
			}
			if (needName[in.Action] || in.Action == "get") && in.Type == "" {
				return nil, fmt.Errorf("%s needs type", in.Action)
			}
			var reasons []string
			if in.Action == "exec" {
				if strings.TrimSpace(in.Command) == "" {
					return nil, errors.New("exec needs command")
				}
				risk, reasons = ClassifyCommand(in.Command)
			}
			envName, protected := targetInfo(env.WS, c)
			subject := strings.TrimSpace(in.Type + " " + nsName(in.Namespace, in.Name))
			preview := fmt.Sprintf("kubernetes %s %s on cluster %s", in.Action, subject, c.Name)
			switch in.Action {
			case "scale":
				preview += fmt.Sprintf(" → %d replicas", in.Replicas)
			case "set_image":
				preview += fmt.Sprintf("\n%s → %s", firstNonEmpty(in.Container, "(its container)"), in.Image)
			case "undo":
				if in.Revision > 0 {
					preview += fmt.Sprintf(" to revision %d", in.Revision)
				}
			case "exec":
				preview += "\n$ " + in.Command
				if in.Container != "" {
					preview += "\ncontainer: " + in.Container
				}
			case "apply":
				if strings.TrimSpace(in.Manifest) == "" {
					return nil, errors.New("apply needs manifest")
				}
				preview = "kubernetes apply on cluster " + c.Name + "\n\n" + applyPreview(ctx, env, c, in.Namespace, in.Manifest)
			}
			return &Call{Risk: risk, Reasons: reasons, Target: "cluster/" + c.Name, Environment: envName, Protected: protected,
				Match: in.Action + " " + subject + " " + in.Command, Preview: truncate(preview, 30000), Credentials: append(env.creds(c), c.Str("kubeconfig")),
				Run: func(ctx context.Context) (string, error) {
					timeout := 2 * time.Minute
					if in.Action == "drain" || in.Action == "apply" {
						timeout = 10 * time.Minute
					}
					ctx, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()
					return runKube(ctx, env, c, in.Action, kubeInput{Type: in.Type, Namespace: in.Namespace, Name: in.Name, Selector: in.Selector,
						Container: in.Container, Since: in.Since, Image: in.Image, Manifest: in.Manifest, Command: in.Command, Wide: in.Wide,
						Previous: in.Previous, Tail: in.Tail, Replicas: in.Replicas, Revision: in.Revision})
				}}, nil
		},
	}
}

func nsName(ns, name string) string {
	switch {
	case ns != "" && name != "":
		return ns + "/" + name
	case name != "":
		return name
	case ns != "":
		return "in " + ns
	}
	return ""
}

// applyPreview runs a server-side dry run, so the approval shows what the
// API server would actually change (defaults, admission webhooks included).
func applyPreview(ctx context.Context, env *Env, c *workspace.Resource, ns, manifest string) string {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := env.Kube.Conn(ctx, env.WS, c)
	if err != nil {
		return "(dry run not possible: " + err.Error() + ")\n\n" + manifest
	}
	if ns == "" || ns == "*" {
		ns = conn.NS
	}
	res, err := conn.Apply(ctx, manifest, ns, true)
	if err != nil {
		return "(dry run failed: " + err.Error() + ")\n\n" + manifest
	}
	return formatApply(res, true)
}

func formatApply(res []kube.ApplyResult, dry bool) string {
	var b strings.Builder
	for _, r := range res {
		verb := r.Action
		if dry && r.Action != "failed" && r.Action != "unchanged" {
			verb += " (dry run)"
		}
		fmt.Fprintf(&b, "%s/%s", strings.ToLower(r.Kind), r.Name)
		if r.Namespace != "" {
			fmt.Fprintf(&b, " in %s", r.Namespace)
		}
		fmt.Fprintf(&b, ": %s\n", verb)
		if r.Error != "" {
			b.WriteString("  error: " + r.Error + "\n")
		}
		if r.Diff != "" {
			b.WriteString(r.Diff + "\n")
		}
	}
	return b.String()
}

type kubeInput struct {
	Type, Namespace, Name, Selector, Container, Since, Image, Manifest, Command string
	Wide, Previous                                                              bool
	Tail, Replicas, Revision                                                    int
}

func runKube(ctx context.Context, env *Env, c *workspace.Resource, action string, in kubeInput) (string, error) {
	conn, err := env.Kube.Conn(ctx, env.WS, c)
	if err != nil {
		return "", err
	}
	ns := in.Namespace
	all := ns == "*"
	if ns == "" {
		ns = conn.NS
	}
	if all {
		ns = ""
	}
	asJSON := func(v any) (string, error) {
		b, _ := json.MarshalIndent(v, "", " ")
		return truncate(string(b), 40000), nil
	}
	var rt kube.ResourceType
	if in.Type != "" {
		if rt, err = conn.Resolve(ctx, in.Type); err != nil {
			return "", err
		}
	}
	switch action {
	case "resources":
		types, err := conn.Resources(ctx, true)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, t := range types {
			scope := "cluster"
			if t.Namespaced {
				scope = "namespaced"
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", t.ID, t.Kind, scope, t.Category)
		}
		return b.String(), nil
	case "overview":
		o, err := conn.Overview(ctx, ns)
		if err != nil {
			return "", err
		}
		return asJSON(o)
	case "get":
		opts := kube.TableOptions{Namespace: ns, LabelSelector: in.Selector}
		if in.Name != "" {
			opts.FieldSelector = "metadata.name=" + in.Name
		}
		t, err := conn.List(ctx, rt, opts)
		if err != nil {
			return "", err
		}
		return truncate(t.Text(in.Wide, 300), 40000), nil
	case "yaml":
		o, err := conn.Get(ctx, rt, ns, in.Name)
		if err != nil {
			return "", err
		}
		y, err := kube.YAMLOf(kube.Display(o), false)
		return truncate(y, 40000), err
	case "describe":
		o, err := conn.Get(ctx, rt, ns, in.Name)
		if err != nil {
			return "", err
		}
		y, _ := kube.YAMLOf(kube.Display(o), false)
		var b strings.Builder
		b.WriteString(truncate(y, 25000))
		if rt.ID != "pods" && rt.ID != "secrets" && rt.ID != "configmaps" {
			if pods, err := conn.PodsFor(ctx, rt, ns, in.Name); err == nil && len(pods) > 0 {
				b.WriteString("\n--- pods ---\n")
				for i, p := range pods {
					if i >= 30 {
						fmt.Fprintf(&b, "… %d more\n", len(pods)-30)
						break
					}
					fmt.Fprintf(&b, "%s %s node=%s", p.Name, p.Status.Phase, p.Spec.NodeName)
					for _, s := range p.Status.ContainerStatuses {
						state := "running"
						switch {
						case s.State.Waiting != nil:
							state = "waiting: " + s.State.Waiting.Reason
						case s.State.Terminated != nil:
							state = "terminated: " + s.State.Terminated.Reason
						}
						fmt.Fprintf(&b, " [%s %s restarts=%d]", s.Name, state, s.RestartCount)
					}
					b.WriteString("\n")
				}
			}
		}
		if md, _ := o["metadata"].(map[string]any); md != nil {
			if uid, _ := md["uid"].(string); uid != "" {
				if evs, err := conn.Events(ctx, ns, uid); err == nil {
					b.WriteString("\n--- events ---\n")
					if len(evs) == 0 {
						b.WriteString("(none)\n")
					}
					for _, e := range evs {
						fmt.Fprintf(&b, "%s %s %s (x%d): %s\n", e.Time, e.Type, e.Reason, e.Count, e.Message)
					}
				}
			}
		}
		return b.String(), nil
	case "logs":
		tail := int64(in.Tail)
		if tail <= 0 || tail > 5000 {
			tail = 200
		}
		var b strings.Builder
		err := conn.StreamLogs(ctx, rt, ns, in.Name, kube.LogOptions{Container: in.Container, Tail: tail, Since: in.Since, Previous: in.Previous},
			func(l kube.LogLine) {
				if l.Src != "" {
					b.WriteString(l.Src + " | ")
				}
				b.WriteString(l.TS + " " + l.Line + "\n")
			})
		if err != nil {
			return "", err
		}
		if b.Len() == 0 {
			return "(no log lines)", nil
		}
		return truncate(b.String(), 60000), nil
	case "events":
		var evs []kube.Event
		if in.Name != "" && rt.ID != "" {
			o, err := conn.Get(ctx, rt, ns, in.Name)
			if err != nil {
				return "", err
			}
			md, _ := o["metadata"].(map[string]any)
			uid, _ := md["uid"].(string)
			if evs, err = conn.Events(ctx, ns, uid); err != nil {
				return "", err
			}
		} else {
			ectx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			conn.WatchEvents(ectx, ns, func(e kube.Event) { evs = append(evs, e) })
			sort.Slice(evs, func(i, j int) bool { return evs[i].Time > evs[j].Time })
		}
		var b strings.Builder
		for i, e := range evs {
			if i >= 200 {
				break
			}
			fmt.Fprintf(&b, "%s %s %s %s %s (x%d): %s\n", e.Time, e.Type, e.Namespace, e.Object, e.Reason, e.Count, e.Message)
		}
		if b.Len() == 0 {
			return "(no events in the last hour)", nil
		}
		return b.String(), nil
	case "top":
		if rt.ID == "nodes" {
			m, err := conn.NodeMetrics(ctx)
			if err != nil {
				return "", err
			}
			return formatUsage(m), nil
		}
		m, err := conn.PodMetrics(ctx, ns)
		if err != nil {
			return "", err
		}
		pods := map[string]kube.Usage{}
		for k, v := range m {
			if strings.Count(k, "/") == 1 {
				pods[k] = v
			}
		}
		return formatUsage(pods), nil
	case "history":
		h, err := conn.History(ctx, rt, ns, in.Name)
		if err != nil {
			return "", err
		}
		return asJSON(h)
	case "rollout_status":
		if rt.ID != "deployments.apps" {
			return "", errors.New("rollout_status is for deployments; use describe for others")
		}
		return kube.RolloutStatus(ctx, conn.CS, ns, in.Name)
	case "helm":
		if in.Name != "" {
			d, err := conn.HelmRelease(ctx, ns, in.Name, in.Revision)
			if err != nil {
				return "", err
			}
			d.Manifest = truncate(d.Manifest, 15000)
			return asJSON(d)
		}
		r, err := conn.HelmReleases(ctx, ns)
		if err != nil {
			return "", err
		}
		return asJSON(r)
	case "apply":
		res, err := conn.Apply(ctx, in.Manifest, ns, false)
		if err != nil {
			return "", err
		}
		out := formatApply(res, false)
		for _, r := range res {
			if r.Action == "failed" {
				return "", errors.New(out)
			}
		}
		return truncate(out, 30000), nil
	case "restart":
		return done("restarted "+in.Name+"; follow with rollout_status or describe", conn.Restart(ctx, rt, ns, in.Name))
	case "scale":
		return done(fmt.Sprintf("scaled %s to %d", in.Name, in.Replicas), conn.Scale(ctx, rt, ns, in.Name, int32(in.Replicas)))
	case "set_image":
		return done("image of "+in.Name+" set to "+in.Image, conn.SetImage(ctx, rt, ns, in.Name, in.Container, in.Image))
	case "pause", "resume":
		return done(action+"d "+in.Name, conn.SetPaused(ctx, rt, ns, in.Name, action == "pause"))
	case "suspend", "unsuspend":
		return done(action+"ed "+in.Name, conn.SetSuspended(ctx, rt, ns, in.Name, action == "suspend"))
	case "trigger":
		job, err := conn.TriggerCronJob(ctx, ns, in.Name)
		return done("started job "+job, err)
	case "undo":
		rev, err := conn.Undo(ctx, rt, ns, in.Name, int64(in.Revision))
		return done(fmt.Sprintf("rolled %s back to revision %d", in.Name, rev), err)
	case "cordon", "uncordon":
		return done(action+"ed "+in.Name, conn.Cordon(ctx, in.Name, action == "cordon"))
	case "drain":
		report, err := conn.Drain(ctx, in.Name)
		if err != nil {
			return "", err
		}
		return "cordoned " + in.Name + "\n" + strings.Join(report, "\n"), nil
	case "delete":
		return done("deleted "+rt.Resource+"/"+in.Name, conn.Delete(ctx, rt, ns, in.Name, false))
	case "exec":
		stdout, stderr, code, err := conn.Exec(ctx, ns, in.Name, in.Container, []string{"sh", "-c", in.Command}, nil, 1<<20)
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("exit code: %d\n", code)
		if stdout != "" {
			out += "stdout:\n" + truncate(stdout, 30000) + "\n"
		}
		if stderr != "" {
			out += "stderr:\n" + truncate(stderr, 8000) + "\n"
		}
		return out, nil
	}
	return "", fmt.Errorf("unknown action %q", action)
}

func done(msg string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return msg, nil
}

func formatUsage(m map[string]kube.Usage) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]].CPU > m[keys[j]].CPU })
	var b strings.Builder
	b.WriteString("NAME\tCPU(m)\tMEMORY(Mi)\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%d\t%d\n", k, m[k].CPU, m[k].Memory>>20)
	}
	return b.String()
}
