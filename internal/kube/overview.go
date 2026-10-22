package kube

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Usage is CPU (millicores) and memory (bytes).
type Usage struct {
	CPU    int64 `json:"cpu"`
	Memory int64 `json:"memory"`
}

func parseUsage(m map[string]any) Usage {
	var u Usage
	if q, err := resource.ParseQuantity(fmt.Sprint(m["cpu"])); err == nil {
		u.CPU = q.MilliValue()
	}
	if q, err := resource.ParseQuantity(fmt.Sprint(m["memory"])); err == nil {
		u.Memory = q.Value()
	}
	return u
}

// ErrNoMetrics means the cluster has no metrics API (metrics-server).
var ErrNoMetrics = errors.New("no metrics: metrics-server is not installed or not ready")

// PodMetrics returns the usage of each pod ("ns/name"), and of each
// container ("ns/name/container").
func (c *Conn) PodMetrics(ctx context.Context, ns string) (map[string]Usage, error) {
	path := "/apis/metrics.k8s.io/v1beta1/pods"
	if ns != "" {
		path = "/apis/metrics.k8s.io/v1beta1/namespaces/" + ns + "/pods"
	}
	b, err := c.raw().Get().AbsPath(path).Do(ctx).Raw()
	if err != nil {
		return nil, ErrNoMetrics
	}
	var l struct {
		Items []struct {
			Metadata   metav1.ObjectMeta `json:"metadata"`
			Containers []struct {
				Name  string         `json:"name"`
				Usage map[string]any `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	out := map[string]Usage{}
	for _, it := range l.Items {
		key := it.Metadata.Namespace + "/" + it.Metadata.Name
		var sum Usage
		for _, ct := range it.Containers {
			u := parseUsage(ct.Usage)
			out[key+"/"+ct.Name] = u
			sum.CPU += u.CPU
			sum.Memory += u.Memory
		}
		out[key] = sum
	}
	return out, nil
}

// NodeMetrics returns the usage of each node.
func (c *Conn) NodeMetrics(ctx context.Context) (map[string]Usage, error) {
	b, err := c.raw().Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").Do(ctx).Raw()
	if err != nil {
		return nil, ErrNoMetrics
	}
	var l struct {
		Items []struct {
			Metadata metav1.ObjectMeta `json:"metadata"`
			Usage    map[string]any    `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	out := map[string]Usage{}
	for _, it := range l.Items {
		out[it.Metadata.Name] = parseUsage(it.Usage)
	}
	return out, nil
}

// ---------------------------------------------------------------------------

// Overview is a cluster's health at a glance.
type Overview struct {
	Version    string        `json:"version"`
	Platform   string        `json:"platform"`
	Namespace  string        `json:"namespace,omitempty"` // "" = all namespaces
	Namespaces int           `json:"namespaces"`
	Nodes      []NodeSummary `json:"nodes"`
	Pods       PodCounts     `json:"pods"`
	Workloads  WorkloadCount `json:"workloads"`
	Problems   []Problem     `json:"problems"`
	Warnings   []Event       `json:"warnings"`
	Metrics    bool          `json:"metrics"`
}

type NodeSummary struct {
	Name          string   `json:"name"`
	Ready         bool     `json:"ready"`
	Unschedulable bool     `json:"unschedulable,omitempty"`
	Roles         []string `json:"roles"`
	Version       string   `json:"version"`
	InternalIP    string   `json:"internalIP,omitempty"`
	OS            string   `json:"os,omitempty"`
	Runtime       string   `json:"runtime,omitempty"`
	Capacity      Usage    `json:"capacity"` // allocatable
	Used          *Usage   `json:"used,omitempty"`
	Requested     Usage    `json:"requested"` // sum of pod requests
	Pods          int      `json:"pods"`
	MaxPods       int64    `json:"maxPods"`
	Pressure      []string `json:"pressure,omitempty"` // MemoryPressure, DiskPressure…
	Age           string   `json:"created"`
}

type PodCounts struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Pending   int `json:"pending"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Unknown   int `json:"unknown"`
	Restarts  int `json:"restarts"`
}

type WorkloadCount struct {
	Deployments  int `json:"deployments"`
	StatefulSets int `json:"statefulsets"`
	DaemonSets   int `json:"daemonsets"`
	Jobs         int `json:"jobs"`
	CronJobs     int `json:"cronjobs"`
	Services     int `json:"services"`
}

// Problem is something that needs attention.
type Problem struct {
	Severity  string `json:"severity"` // error | warning
	Kind      string `json:"kind"`     // pods, deployments.apps, nodes…
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
	Message   string `json:"message,omitempty"`
}

var badWaiting = map[string]bool{"CrashLoopBackOff": true, "ImagePullBackOff": true, "ErrImagePull": true, "CreateContainerConfigError": true,
	"InvalidImageName": true, "CreateContainerError": true, "RunContainerError": true, "ContainerCannotRun": true}

func (c *Conn) Overview(ctx context.Context, ns string) (*Overview, error) {
	o := &Overview{Namespace: ns, Nodes: []NodeSummary{}, Problems: []Problem{}, Warnings: []Event{}}
	if v, err := c.CS.Discovery().ServerVersion(); err == nil {
		o.Version, o.Platform = v.GitVersion, v.Platform
	} else {
		return nil, err
	}
	if l, err := c.CS.CoreV1().Namespaces().List(ctx, metav1.ListOptions{}); err == nil {
		o.Namespaces = len(l.Items)
	}
	pods, err := c.CS.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	perNode := map[string][]corev1.Pod{}
	for _, p := range pods.Items {
		o.Pods.Total++
		switch p.Status.Phase {
		case corev1.PodRunning:
			o.Pods.Running++
		case corev1.PodPending:
			o.Pods.Pending++
		case corev1.PodSucceeded:
			o.Pods.Succeeded++
		case corev1.PodFailed:
			o.Pods.Failed++
		default:
			o.Pods.Unknown++
		}
		perNode[p.Spec.NodeName] = append(perNode[p.Spec.NodeName], p)
		o.Problems = append(o.Problems, podProblems(&p)...)
		for _, s := range p.Status.ContainerStatuses {
			o.Pods.Restarts += int(s.RestartCount)
		}
	}
	nodeUse, err := c.NodeMetrics(ctx)
	o.Metrics = err == nil
	if nodes, err := c.CS.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
		allPods := perNode
		if ns != "" { // requests need every pod of the node
			if l, err := c.CS.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase!=Succeeded,status.phase!=Failed"}); err == nil {
				allPods = map[string][]corev1.Pod{}
				for _, p := range l.Items {
					allPods[p.Spec.NodeName] = append(allPods[p.Spec.NodeName], p)
				}
			}
		}
		for _, n := range nodes.Items {
			s := nodeSummary(&n, allPods[n.Name])
			if u, ok := nodeUse[n.Name]; ok {
				s.Used = &u
			}
			if !s.Ready {
				o.Problems = append(o.Problems, Problem{Severity: "error", Kind: "nodes", Name: n.Name, Reason: "NotReady", Message: nodeMessage(&n)})
			}
			for _, pr := range s.Pressure {
				o.Problems = append(o.Problems, Problem{Severity: "warning", Kind: "nodes", Name: n.Name, Reason: pr})
			}
			o.Nodes = append(o.Nodes, s)
		}
	}
	if l, err := c.CS.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{}); err == nil {
		o.Workloads.Deployments = len(l.Items)
		for _, d := range l.Items {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			if d.Status.AvailableReplicas < want && time.Since(d.CreationTimestamp.Time) > time.Minute {
				o.Problems = append(o.Problems, Problem{Severity: "warning", Kind: "deployments.apps", Namespace: d.Namespace, Name: d.Name,
					Reason: "Unavailable", Message: fmt.Sprintf("%d of %d replicas available", d.Status.AvailableReplicas, want)})
			}
		}
	}
	if l, err := c.CS.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{}); err == nil {
		o.Workloads.StatefulSets = len(l.Items)
		for _, d := range l.Items {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			if d.Status.ReadyReplicas < want && time.Since(d.CreationTimestamp.Time) > time.Minute {
				o.Problems = append(o.Problems, Problem{Severity: "warning", Kind: "statefulsets.apps", Namespace: d.Namespace, Name: d.Name,
					Reason: "NotReady", Message: fmt.Sprintf("%d of %d replicas ready", d.Status.ReadyReplicas, want)})
			}
		}
	}
	if l, err := c.CS.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{}); err == nil {
		o.Workloads.DaemonSets = len(l.Items)
		for _, d := range l.Items {
			if d.Status.NumberUnavailable > 0 {
				o.Problems = append(o.Problems, Problem{Severity: "warning", Kind: "daemonsets.apps", Namespace: d.Namespace, Name: d.Name,
					Reason: "Unavailable", Message: fmt.Sprintf("%d of %d pods unavailable", d.Status.NumberUnavailable, d.Status.DesiredNumberScheduled)})
			}
		}
	}
	if l, err := c.CS.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{}); err == nil {
		o.Workloads.Jobs = len(l.Items)
		for _, j := range l.Items {
			for _, cond := range j.Status.Conditions {
				if cond.Type == "Failed" && cond.Status == corev1.ConditionTrue && time.Since(cond.LastTransitionTime.Time) < 24*time.Hour {
					o.Problems = append(o.Problems, Problem{Severity: "warning", Kind: "jobs.batch", Namespace: j.Namespace, Name: j.Name, Reason: "Failed", Message: cond.Message})
				}
			}
		}
	}
	if l, err := c.CS.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{}); err == nil {
		o.Workloads.CronJobs = len(l.Items)
	}
	if l, err := c.CS.CoreV1().Services(ns).List(ctx, metav1.ListOptions{}); err == nil {
		o.Workloads.Services = len(l.Items)
	}
	if l, err := c.CS.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{}); err == nil {
		for _, p := range l.Items {
			if p.Status.Phase == corev1.ClaimPending && time.Since(p.CreationTimestamp.Time) > time.Minute {
				o.Problems = append(o.Problems, Problem{Severity: "warning", Kind: "persistentvolumeclaims", Namespace: p.Namespace, Name: p.Name, Reason: "Pending", Message: "the claim is not bound"})
			}
		}
	}
	if l, err := c.CS.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "type=Warning", Limit: 500}); err == nil {
		cut := time.Now().Add(-time.Hour)
		for i := range l.Items {
			if evTime(l.Items[i]).After(cut) {
				o.Warnings = append(o.Warnings, toEvent(&l.Items[i]))
			}
		}
		sort.Slice(o.Warnings, func(i, j int) bool { return o.Warnings[i].Time > o.Warnings[j].Time })
		if len(o.Warnings) > 40 {
			o.Warnings = o.Warnings[:40]
		}
	}
	sort.SliceStable(o.Problems, func(i, j int) bool { return o.Problems[i].Severity == "error" && o.Problems[j].Severity != "error" })
	return o, nil
}

func podProblems(p *corev1.Pod) []Problem {
	var out []Problem
	add := func(sev, reason, msg string) {
		out = append(out, Problem{Severity: sev, Kind: "pods", Namespace: p.Namespace, Name: p.Name, Reason: reason, Message: msg})
	}
	if p.DeletionTimestamp != nil && time.Since(p.DeletionTimestamp.Time) > 5*time.Minute {
		add("warning", "StuckTerminating", "deleted "+time.Since(p.DeletionTimestamp.Time).Round(time.Minute).String()+" ago")
		return out
	}
	statuses := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
	for _, s := range statuses {
		if w := s.State.Waiting; w != nil && badWaiting[w.Reason] {
			add("error", w.Reason, s.Name+": "+firstLine(w.Message))
			return out
		}
		// between back-offs a crash-looping container shows as just terminated
		if t := s.State.Terminated; t != nil && t.ExitCode != 0 && s.RestartCount > 0 && p.Spec.RestartPolicy != corev1.RestartPolicyNever {
			add("error", "CrashLoopBackOff", fmt.Sprintf("%s exited with code %d (%s), restarted %d times", s.Name, t.ExitCode, t.Reason, s.RestartCount))
			return out
		}
		if t := s.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" && time.Since(t.FinishedAt.Time) < time.Hour {
			add("error", "OOMKilled", s.Name+" ran out of memory "+time.Since(t.FinishedAt.Time).Round(time.Second).String()+" ago")
			return out
		}
	}
	switch p.Status.Phase {
	case corev1.PodPending:
		if time.Since(p.CreationTimestamp.Time) > 2*time.Minute {
			msg := ""
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
					msg = c.Message
				}
			}
			add("warning", "Pending", firstLine(msg))
		}
	case corev1.PodFailed:
		if ownerKind(p.OwnerReferences) != "Job" {
			add("warning", firstNonEmpty(p.Status.Reason, "Failed"), firstLine(p.Status.Message))
		}
	case corev1.PodRunning:
		for _, s := range p.Status.ContainerStatuses {
			if s.RestartCount >= 5 {
				add("warning", "Restarting", fmt.Sprintf("%s restarted %d times", s.Name, s.RestartCount))
				break
			}
		}
	}
	return out
}

func nodeSummary(n *corev1.Node, pods []corev1.Pod) NodeSummary {
	s := NodeSummary{Name: n.Name, Unschedulable: n.Spec.Unschedulable, Roles: []string{}, Version: n.Status.NodeInfo.KubeletVersion,
		OS: n.Status.NodeInfo.OSImage, Runtime: n.Status.NodeInfo.ContainerRuntimeVersion, Age: n.CreationTimestamp.UTC().Format(time.RFC3339),
		Capacity: Usage{CPU: n.Status.Allocatable.Cpu().MilliValue(), Memory: n.Status.Allocatable.Memory().Value()}, MaxPods: n.Status.Allocatable.Pods().Value()}
	for k := range n.Labels {
		if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && r != "" {
			s.Roles = append(s.Roles, r)
		}
	}
	sort.Strings(s.Roles)
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			s.InternalIP = a.Address
		}
	}
	for _, c := range n.Status.Conditions {
		switch {
		case c.Type == corev1.NodeReady:
			s.Ready = c.Status == corev1.ConditionTrue
		case c.Status == corev1.ConditionTrue && strings.HasSuffix(string(c.Type), "Pressure") || c.Type == corev1.NodeNetworkUnavailable && c.Status == corev1.ConditionTrue:
			s.Pressure = append(s.Pressure, string(c.Type))
		}
	}
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		s.Pods++
		for _, ct := range p.Spec.Containers {
			s.Requested.CPU += ct.Resources.Requests.Cpu().MilliValue()
			s.Requested.Memory += ct.Resources.Requests.Memory().Value()
		}
	}
	return s
}

func nodeMessage(n *corev1.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Message
		}
	}
	return ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Helm releases (read from Helm's release Secrets; no helm binary needed)

type HelmRelease struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Revision     int    `json:"revision"`
	Status       string `json:"status"`
	Chart        string `json:"chart"`
	ChartVersion string `json:"chartVersion"`
	AppVersion   string `json:"appVersion,omitempty"`
	Updated      string `json:"updated"`
	Description  string `json:"description,omitempty"`
}

type HelmDetail struct {
	HelmRelease
	Values   string        `json:"values"`   // user-supplied values (YAML)
	Manifest string        `json:"manifest"` // rendered manifest
	Notes    string        `json:"notes,omitempty"`
	History  []HelmRelease `json:"history"`
}

type helmRaw struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	Info      struct {
		Status       string `json:"status"`
		LastDeployed string `json:"last_deployed"`
		Description  string `json:"description"`
		Notes        string `json:"notes"`
	} `json:"info"`
	Chart struct {
		Metadata struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			AppVersion string `json:"appVersion"`
		} `json:"metadata"`
	} `json:"chart"`
	Config   map[string]any `json:"config"`
	Manifest string         `json:"manifest"`
}

func (h *helmRaw) release() HelmRelease {
	return HelmRelease{Name: h.Name, Namespace: h.Namespace, Revision: h.Version, Status: h.Info.Status, Chart: h.Chart.Metadata.Name,
		ChartVersion: h.Chart.Metadata.Version, AppVersion: h.Chart.Metadata.AppVersion, Updated: h.Info.LastDeployed, Description: h.Info.Description}
}

func decodeHelm(data []byte) (*helmRaw, error) {
	b, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, err
	}
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		if b, err = io.ReadAll(io.LimitReader(zr, 64<<20)); err != nil {
			return nil, err
		}
	}
	var h helmRaw
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func (c *Conn) helmSecrets(ctx context.Context, ns, name string) ([]*helmRaw, error) {
	sel := "owner=helm"
	if name != "" {
		sel += ",name=" + name
	}
	l, err := c.CS.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, err
	}
	var out []*helmRaw
	for _, s := range l.Items {
		if h, err := decodeHelm(s.Data["release"]); err == nil {
			out = append(out, h)
		}
	}
	return out, nil
}

// HelmReleases lists the releases (their latest revision) of a namespace
// ("" = all).
func (c *Conn) HelmReleases(ctx context.Context, ns string) ([]HelmRelease, error) {
	raws, err := c.helmSecrets(ctx, ns, "")
	if err != nil {
		return nil, err
	}
	latest := map[string]*helmRaw{}
	for _, h := range raws {
		k := h.Namespace + "/" + h.Name
		if cur, ok := latest[k]; !ok || h.Version > cur.Version {
			latest[k] = h
		}
	}
	out := []HelmRelease{}
	for _, h := range latest {
		out = append(out, h.release())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name })
	return out, nil
}

// HelmRelease returns a release (revision 0 = latest) with its values,
// manifest, notes and history.
func (c *Conn) HelmRelease(ctx context.Context, ns, name string, revision int) (*HelmDetail, error) {
	raws, err := c.helmSecrets(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	if len(raws) == 0 {
		return nil, fmt.Errorf("helm release %s/%s not found", ns, name)
	}
	sort.Slice(raws, func(i, j int) bool { return raws[i].Version > raws[j].Version })
	pick := raws[0]
	for _, h := range raws {
		if h.Version == revision {
			pick = h
		}
	}
	d := &HelmDetail{HelmRelease: pick.release(), Manifest: pick.Manifest, Notes: pick.Info.Notes}
	if len(pick.Config) > 0 {
		b, _ := yaml.Marshal(pick.Config)
		d.Values = string(b)
	}
	for _, h := range raws {
		d.History = append(d.History, h.release())
	}
	return d, nil
}
