// Package kube talks to Kubernetes clusters defined in a workspace. The
// kubeconfig stays in the vault (never written to disk) and API traffic goes
// through the cluster's via chain (VPN / bastion) when set.
package kube

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"opsdeck/internal/connect"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

type Manager struct {
	hub   *connect.Hub
	vault *vault.Vault

	mu      sync.Mutex
	clients map[string]cached
}

type cached struct {
	cs  *kubernetes.Clientset
	rev string
	ns  string
}

func New(hub *connect.Hub, v *vault.Vault) *Manager {
	m := &Manager{hub: hub, vault: v, clients: map[string]cached{}}
	v.OnLock(m.Reset)
	return m
}

// Reset drops all cached clients.
func (m *Manager) Reset() {
	m.mu.Lock()
	m.clients = map[string]cached{}
	m.mu.Unlock()
}

// Client returns a clientset and default namespace for a KubernetesCluster resource.
func (m *Manager) Client(ctx context.Context, w *workspace.Workspace, c *workspace.Resource) (*kubernetes.Clientset, string, error) {
	if c.Kind != "KubernetesCluster" {
		return nil, "", fmt.Errorf("%s is not a Kubernetes cluster", c.ID())
	}
	ref := c.Str("kubeconfig")
	it, _, err := m.vault.ResolveFor(w.Name(), ref)
	if err != nil {
		return nil, "", err
	}
	if a := connect.ActorFrom(ctx); a.Kind == "agent" && !it.Policy.AgentUse {
		return nil, "", fmt.Errorf("credential %s is not allowed for agent use", ref)
	}
	key := w.Name() + "/" + c.Name
	rev := fmt.Sprintf("%s:%d:%s:%s:%s", it.ID, it.Revision, c.Str("context"), c.Via, c.Str("namespace"))
	m.mu.Lock()
	if cc, ok := m.clients[key]; ok && cc.rev == rev {
		m.mu.Unlock()
		return cc.cs, cc.ns, nil
	}
	m.mu.Unlock()

	raw, err := clientcmd.Load([]byte(it.Get("kubeconfig")))
	if err != nil {
		return nil, "", fmt.Errorf("parse kubeconfig: %w", err)
	}
	overrides := &clientcmd.ConfigOverrides{}
	if ctxName := c.Str("context"); ctxName != "" {
		overrides.CurrentContext = ctxName
	} else if def := it.Get("context"); def != "" {
		overrides.CurrentContext = def
	}
	cc := clientcmd.NewDefaultClientConfig(*raw, overrides)
	rc, err := cc.ClientConfig()
	if err != nil {
		return nil, "", err
	}
	ns := c.Str("namespace")
	if ns == "" {
		ns, _, _ = cc.Namespace()
	}
	if ns == "" {
		ns = "default"
	}
	if c.Via != "" {
		// Background context: the dialer outlives this request.
		dial, err := m.hub.Dialer(context.Background(), w, c)
		if err != nil {
			return nil, "", err
		}
		rc.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) { return dial(ctx, network, addr) }
	}
	rc.Timeout = 30 * time.Second
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	m.clients[key] = cached{cs: cs, rev: rev, ns: ns}
	m.mu.Unlock()
	return cs, ns, nil
}

// Types supported by List.
var Types = []string{"pods", "deployments", "statefulsets", "daemonsets", "services", "ingresses", "nodes", "events", "configmaps", "jobs", "cronjobs", "namespaces"}

type Row map[string]any

func age(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t.Time)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// List returns table rows for a resource type. namespace "" = all namespaces.
func List(ctx context.Context, cs kubernetes.Interface, typ, ns string) ([]Row, error) {
	opts := metav1.ListOptions{Limit: 2000}
	rows := []Row{}
	switch typ {
	case "namespaces":
		l, err := cs.CoreV1().Namespaces().List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range l.Items {
			rows = append(rows, Row{"name": x.Name, "status": string(x.Status.Phase), "age": age(x.CreationTimestamp)})
		}
	case "pods":
		l, err := cs.CoreV1().Pods(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, p := range l.Items {
			ready, restarts := 0, int32(0)
			for _, s := range p.Status.ContainerStatuses {
				if s.Ready {
					ready++
				}
				restarts += s.RestartCount
			}
			status := string(p.Status.Phase)
			for _, s := range p.Status.ContainerStatuses {
				if s.State.Waiting != nil && s.State.Waiting.Reason != "" {
					status = s.State.Waiting.Reason
				} else if s.State.Terminated != nil && s.State.Terminated.Reason != "" && p.Status.Phase != corev1.PodSucceeded {
					status = s.State.Terminated.Reason
				}
			}
			if p.DeletionTimestamp != nil {
				status = "Terminating"
			}
			containers := []string{}
			for _, c := range p.Spec.Containers {
				containers = append(containers, c.Name)
			}
			rows = append(rows, Row{"name": p.Name, "namespace": p.Namespace, "status": status,
				"ready": fmt.Sprintf("%d/%d", ready, len(p.Spec.Containers)), "restarts": restarts,
				"node": p.Spec.NodeName, "ip": p.Status.PodIP, "age": age(p.CreationTimestamp), "containers": strings.Join(containers, ",")})
		}
	case "deployments":
		l, err := cs.AppsV1().Deployments(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, d := range l.Items {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			rows = append(rows, Row{"name": d.Name, "namespace": d.Namespace, "ready": fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, want),
				"upToDate": d.Status.UpdatedReplicas, "available": d.Status.AvailableReplicas, "replicas": want,
				"image": images(d.Spec.Template.Spec.Containers), "age": age(d.CreationTimestamp)})
		}
	case "statefulsets":
		l, err := cs.AppsV1().StatefulSets(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, d := range l.Items {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			rows = append(rows, Row{"name": d.Name, "namespace": d.Namespace, "ready": fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, want),
				"replicas": want, "image": images(d.Spec.Template.Spec.Containers), "age": age(d.CreationTimestamp)})
		}
	case "daemonsets":
		l, err := cs.AppsV1().DaemonSets(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, d := range l.Items {
			rows = append(rows, Row{"name": d.Name, "namespace": d.Namespace, "desired": d.Status.DesiredNumberScheduled,
				"ready": d.Status.NumberReady, "available": d.Status.NumberAvailable, "image": images(d.Spec.Template.Spec.Containers), "age": age(d.CreationTimestamp)})
		}
	case "services":
		l, err := cs.CoreV1().Services(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, s := range l.Items {
			ports := []string{}
			for _, p := range s.Spec.Ports {
				pp := fmt.Sprintf("%d/%s", p.Port, p.Protocol)
				if p.NodePort != 0 {
					pp = fmt.Sprintf("%d:%d/%s", p.Port, p.NodePort, p.Protocol)
				}
				ports = append(ports, pp)
			}
			ext := []string{}
			for _, in := range s.Status.LoadBalancer.Ingress {
				ext = append(ext, in.IP+in.Hostname)
			}
			rows = append(rows, Row{"name": s.Name, "namespace": s.Namespace, "type": string(s.Spec.Type), "clusterIP": s.Spec.ClusterIP,
				"externalIP": strings.Join(ext, ","), "ports": strings.Join(ports, ","), "age": age(s.CreationTimestamp)})
		}
	case "ingresses":
		l, err := cs.NetworkingV1().Ingresses(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, in := range l.Items {
			hosts := []string{}
			for _, r := range in.Spec.Rules {
				hosts = append(hosts, r.Host)
			}
			class := ""
			if in.Spec.IngressClassName != nil {
				class = *in.Spec.IngressClassName
			}
			rows = append(rows, Row{"name": in.Name, "namespace": in.Namespace, "class": class, "hosts": strings.Join(hosts, ","), "age": age(in.CreationTimestamp)})
		}
	case "nodes":
		l, err := cs.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, n := range l.Items {
			status := "NotReady"
			for _, c := range n.Status.Conditions {
				if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
					status = "Ready"
				}
			}
			if n.Spec.Unschedulable {
				status += ",SchedulingDisabled"
			}
			roles := []string{}
			for k := range n.Labels {
				if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok {
					roles = append(roles, r)
				}
			}
			ip := ""
			for _, a := range n.Status.Addresses {
				if a.Type == corev1.NodeInternalIP {
					ip = a.Address
				}
			}
			rows = append(rows, Row{"name": n.Name, "status": status, "roles": strings.Join(roles, ","), "version": n.Status.NodeInfo.KubeletVersion,
				"internalIP": ip, "cpu": n.Status.Capacity.Cpu().String(), "memory": n.Status.Capacity.Memory().String(), "age": age(n.CreationTimestamp)})
		}
	case "events":
		l, err := cs.CoreV1().Events(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		sort.Slice(l.Items, func(i, j int) bool { return evTime(l.Items[i]).After(evTime(l.Items[j])) })
		for _, e := range l.Items {
			rows = append(rows, Row{"name": e.Name, "namespace": e.Namespace, "type": e.Type, "reason": e.Reason,
				"object": strings.ToLower(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name, "message": e.Message,
				"count": e.Count, "age": age(metav1.NewTime(evTime(e)))})
		}
	case "configmaps":
		l, err := cs.CoreV1().ConfigMaps(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, c := range l.Items {
			rows = append(rows, Row{"name": c.Name, "namespace": c.Namespace, "keys": len(c.Data) + len(c.BinaryData), "age": age(c.CreationTimestamp)})
		}
	case "jobs":
		l, err := cs.BatchV1().Jobs(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, j := range l.Items {
			comp := int32(1)
			if j.Spec.Completions != nil {
				comp = *j.Spec.Completions
			}
			rows = append(rows, Row{"name": j.Name, "namespace": j.Namespace, "completions": fmt.Sprintf("%d/%d", j.Status.Succeeded, comp),
				"failed": j.Status.Failed, "active": j.Status.Active, "age": age(j.CreationTimestamp)})
		}
	case "cronjobs":
		l, err := cs.BatchV1().CronJobs(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, j := range l.Items {
			last := ""
			if j.Status.LastScheduleTime != nil {
				last = age(*j.Status.LastScheduleTime)
			}
			suspended := j.Spec.Suspend != nil && *j.Spec.Suspend
			rows = append(rows, Row{"name": j.Name, "namespace": j.Namespace, "schedule": j.Spec.Schedule, "suspended": suspended,
				"active": len(j.Status.Active), "lastSchedule": last, "age": age(j.CreationTimestamp)})
		}
	default:
		return nil, fmt.Errorf("unsupported type %q (supported: %s)", typ, strings.Join(Types, ", "))
	}
	return rows, nil
}

func evTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func images(cs []corev1.Container) string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Image)
	}
	return strings.Join(out, ",")
}

// YAML returns the manifest of one object (managedFields stripped).
func YAML(ctx context.Context, cs kubernetes.Interface, typ, ns, name string) (string, error) {
	var obj runtime.Object
	var err error
	g := metav1.GetOptions{}
	switch typ {
	case "pods":
		obj, err = cs.CoreV1().Pods(ns).Get(ctx, name, g)
	case "deployments":
		obj, err = cs.AppsV1().Deployments(ns).Get(ctx, name, g)
	case "statefulsets":
		obj, err = cs.AppsV1().StatefulSets(ns).Get(ctx, name, g)
	case "daemonsets":
		obj, err = cs.AppsV1().DaemonSets(ns).Get(ctx, name, g)
	case "services":
		obj, err = cs.CoreV1().Services(ns).Get(ctx, name, g)
	case "ingresses":
		obj, err = cs.NetworkingV1().Ingresses(ns).Get(ctx, name, g)
	case "nodes":
		obj, err = cs.CoreV1().Nodes().Get(ctx, name, g)
	case "configmaps":
		obj, err = cs.CoreV1().ConfigMaps(ns).Get(ctx, name, g)
	case "jobs":
		obj, err = cs.BatchV1().Jobs(ns).Get(ctx, name, g)
	case "cronjobs":
		obj, err = cs.BatchV1().CronJobs(ns).Get(ctx, name, g)
	case "namespaces":
		obj, err = cs.CoreV1().Namespaces().Get(ctx, name, g)
	default:
		return "", fmt.Errorf("unsupported type %q", typ)
	}
	if err != nil {
		return "", err
	}
	if acc, ok := obj.(metav1.ObjectMetaAccessor); ok {
		acc.GetObjectMeta().SetManagedFields(nil)
	}
	if gvks, _, err := scheme.Scheme.ObjectKinds(obj); err == nil && len(gvks) > 0 {
		obj.GetObjectKind().SetGroupVersionKind(gvks[0])
	}
	b, err := yaml.Marshal(obj)
	return string(b), err
}

// Logs returns (or follows) pod logs line by line.
func Logs(ctx context.Context, cs kubernetes.Interface, ns, pod, container string, tail int64, follow bool, onLine func(string)) error {
	opts := &corev1.PodLogOptions{Container: container, Follow: follow, Timestamps: true}
	if tail > 0 {
		opts.TailLines = &tail
	}
	stream, err := cs.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		onLine(sc.Text())
	}
	if ctx.Err() != nil {
		return nil
	}
	return sc.Err()
}

// RestartDeployment does what `kubectl rollout restart` does.
func RestartDeployment(ctx context.Context, cs kubernetes.Interface, ns, name string) error {
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`, time.Now().Format(time.RFC3339))
	_, err := cs.AppsV1().Deployments(ns).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// ScaleDeployment sets the replica count.
func ScaleDeployment(ctx context.Context, cs kubernetes.Interface, ns, name string, replicas int32) error {
	s, err := cs.AppsV1().Deployments(ns).GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	s.Spec.Replicas = replicas
	_, err = cs.AppsV1().Deployments(ns).UpdateScale(ctx, name, s, metav1.UpdateOptions{})
	return err
}

func DeletePod(ctx context.Context, cs kubernetes.Interface, ns, name string) error {
	return cs.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{})
}

// RolloutStatus summarizes a deployment's progress.
func RolloutStatus(ctx context.Context, cs kubernetes.Interface, ns, name string) (string, error) {
	d, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return rolloutText(d), nil
}

func rolloutText(d *appsv1.Deployment) string {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	var conds []string
	for _, c := range d.Status.Conditions {
		conds = append(conds, fmt.Sprintf("%s=%s (%s)", c.Type, c.Status, c.Reason))
	}
	return fmt.Sprintf("replicas wanted=%d updated=%d ready=%d available=%d; generation=%d observed=%d; %s",
		want, d.Status.UpdatedReplicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas,
		d.Generation, d.Status.ObservedGeneration, strings.Join(conds, ", "))
}
