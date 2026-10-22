package kube

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Object is any Kubernetes object as JSON.
type Object = map[string]any

// Get returns an object (managedFields removed).
func (c *Conn) Get(ctx context.Context, rt ResourceType, ns, name string) (Object, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	b, err := c.raw().Get().AbsPath(rt.ObjectPath(nsFor(rt, ns), name)).Do(ctx).Raw()
	if apierrors.IsNotFound(err) {
		return nil, apierrors.NewNotFound(rt.GVR().GroupResource(), name)
	}
	if err != nil {
		return nil, err
	}
	var o Object
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	if md, ok := o["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
	}
	return o, nil
}

func nsFor(rt ResourceType, ns string) string {
	if rt.Namespaced {
		return ns
	}
	return ""
}

func validName(name string) error {
	if name == "" || strings.ContainsAny(name, "/?#") || name == "." || name == ".." || len(name) > 253 {
		return fmt.Errorf("invalid object name %q", name)
	}
	return nil
}

// MaskSecret replaces a Secret's values with a short fingerprint, so the
// object can be shown (and diffed) without revealing them.
func MaskSecret(o Object) Object {
	if o["kind"] != "Secret" {
		return o
	}
	o = deepCopy(o)
	for _, field := range []string{"data", "stringData"} {
		d, ok := o[field].(map[string]any)
		if !ok {
			continue
		}
		masked := map[string]any{}
		for k, v := range d {
			s := fmt.Sprint(v)
			raw := []byte(s)
			if field == "data" {
				if dec, err := base64.StdEncoding.DecodeString(s); err == nil {
					raw = dec
				}
			}
			sum := sha256.Sum256(raw)
			masked[k] = fmt.Sprintf("<hidden: %d bytes, sha256 %s>", len(raw), hex.EncodeToString(sum[:4]))
		}
		o[field] = masked
	}
	if md, ok := o["metadata"].(map[string]any); ok {
		if a, ok := md["annotations"].(map[string]any); ok {
			delete(a, "kubectl.kubernetes.io/last-applied-configuration")
		}
	}
	return o
}

// Display is an object as shown to people and the agent: without the
// last-applied annotation, which repeats the whole object as JSON.
func Display(o Object) Object {
	md, _ := o["metadata"].(map[string]any)
	a, _ := md["annotations"].(map[string]any)
	if _, ok := a["kubectl.kubernetes.io/last-applied-configuration"]; !ok {
		return o
	}
	o = deepCopy(o)
	a = o["metadata"].(map[string]any)["annotations"].(map[string]any)
	delete(a, "kubectl.kubernetes.io/last-applied-configuration")
	if len(a) == 0 {
		delete(o["metadata"].(map[string]any), "annotations")
	}
	return o
}

// YAML renders an object; Secrets are masked unless reveal.
func YAMLOf(o Object, reveal bool) (string, error) {
	if !reveal {
		o = MaskSecret(o)
	}
	b, err := yaml.Marshal(o)
	return string(b), err
}

// SecretData decodes a Secret's values.
func (c *Conn) SecretData(ctx context.Context, ns, name string) (map[string]string, error) {
	s, err := c.CS.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range s.Data {
		out[k] = string(v)
	}
	return out, nil
}

// Delete removes an object (dependents in the background, like kubectl).
func (c *Conn) Delete(ctx context.Context, rt ResourceType, ns, name string, force bool) error {
	if err := validName(name); err != nil {
		return err
	}
	if !rt.Can("delete") {
		return fmt.Errorf("%s cannot be deleted", rt.ID)
	}
	prop := metav1.DeletePropagationBackground
	opts := metav1.DeleteOptions{PropagationPolicy: &prop}
	if force {
		zero := int64(0)
		opts.GracePeriodSeconds = &zero
	}
	body, _ := json.Marshal(opts)
	return c.raw().Delete().AbsPath(rt.ObjectPath(nsFor(rt, ns), name)).Body(body).SetHeader("Content-Type", "application/json").Do(ctx).Error()
}

// ---------------------------------------------------------------------------
// Apply

// ApplyResult is the outcome for one object of a manifest.
type ApplyResult struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Action    string `json:"action"` // created | configured | unchanged | failed
	Diff      string `json:"diff,omitempty"`
	Error     string `json:"error,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Docs splits a YAML or JSON manifest into objects (List kinds expanded).
func Docs(manifest string) ([]Object, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	var out []Object
	for {
		var o Object
		if err := dec.Decode(&o); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		if len(o) == 0 {
			continue
		}
		if k, _ := o["kind"].(string); strings.HasSuffix(k, "List") {
			if items, ok := o["items"].([]any); ok {
				for _, it := range items {
					if m, ok := it.(map[string]any); ok {
						out = append(out, m)
					}
				}
				continue
			}
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil, errors.New("the manifest has no objects")
	}
	return out, nil
}

// Apply server-side applies every object of a manifest (field manager
// "opsdeck", conflicts forced like kubectl apply --server-side
// --force-conflicts). With dryRun nothing is stored: the API server
// validates and defaults the objects (admission webhooks included) and the
// diff shows what would change.
func (c *Conn) Apply(ctx context.Context, manifest, defaultNS string, dryRun bool) ([]ApplyResult, error) {
	docs, err := Docs(manifest)
	if err != nil {
		return nil, err
	}
	var out []ApplyResult
	newNS := map[string]bool{} // namespaces this manifest creates (dry run: they do not exist yet)
	for _, o := range docs {
		r := c.applyOne(ctx, o, defaultNS, dryRun, newNS)
		if r.Kind == "Namespace" && r.Action == "created" {
			newNS[r.Name] = true
		}
		out = append(out, r)
	}
	return out, nil
}

func (c *Conn) applyOne(ctx context.Context, o Object, defaultNS string, dryRun bool, newNS map[string]bool) ApplyResult {
	apiVersion, _ := o["apiVersion"].(string)
	kind, _ := o["kind"].(string)
	md, _ := o["metadata"].(map[string]any)
	name, _ := md["name"].(string)
	ns, _ := md["namespace"].(string)
	r := ApplyResult{Kind: kind, Name: name}
	fail := func(err error) ApplyResult {
		r.Action, r.Error = "failed", err.Error()
		return r
	}
	if apiVersion == "" || kind == "" || md == nil {
		return fail(errors.New("apiVersion, kind and metadata are required"))
	}
	if name == "" {
		return fail(errors.New("metadata.name is required (generateName is not supported by apply)"))
	}
	rt, err := c.ForKind(ctx, apiVersion, kind)
	if err != nil {
		return fail(err)
	}
	if rt.Namespaced {
		if ns == "" {
			ns = defaultNS
			md["namespace"] = ns
		}
		r.Namespace = ns
	} else {
		delete(md, "namespace")
		ns = ""
	}
	if err := validName(name); err != nil {
		return fail(err)
	}
	before, err := c.Get(ctx, rt, ns, name)
	if err != nil && !apierrors.IsNotFound(err) {
		return fail(err)
	}
	if apierrors.IsNotFound(err) {
		before = nil
	}
	body, _ := json.Marshal(o)
	req := c.raw().Patch(types.ApplyPatchType).AbsPath(rt.ObjectPath(ns, name)).
		Param("fieldManager", "opsdeck").Param("force", "true").Body(body)
	if dryRun {
		req = req.Param("dryRun", "All")
	}
	b, err := req.Do(ctx).Raw()
	if err != nil && dryRun && newNS[ns] && apierrors.IsNotFound(err) {
		r.Action, r.Diff = "created", Diff(nil, o)
		r.Note = "its namespace is created by this manifest, so the server could not check it in a dry run"
		return r
	}
	if err != nil {
		return fail(err)
	}
	var after Object
	json.Unmarshal(b, &after)
	r.Diff = Diff(before, after)
	switch {
	case before == nil:
		r.Action = "created"
	case r.Diff == "":
		r.Action = "unchanged"
	default:
		r.Action = "configured"
	}
	return r
}

// Diff is a unified diff of two objects without the fields the server
// maintains (status, resourceVersion, managedFields…); Secrets are masked.
func Diff(before, after Object) string {
	a, b := "", ""
	if before != nil {
		a, _ = YAMLOf(Clean(before), false)
	}
	if after != nil {
		b, _ = YAMLOf(Clean(after), false)
	}
	if a == b {
		return ""
	}
	d := difflib.UnifiedDiff{A: difflib.SplitLines(a), B: difflib.SplitLines(b), FromFile: "live", ToFile: "applied", Context: 3}
	s, _ := difflib.GetUnifiedDiffString(d)
	return s
}

// Clean drops status and server-set metadata, leaving what an apply needs.
func Clean(o Object) Object {
	c := deepCopy(o)
	delete(c, "status")
	if md, ok := c["metadata"].(map[string]any); ok {
		for _, k := range []string{"managedFields", "resourceVersion", "uid", "generation", "creationTimestamp", "selfLink"} {
			delete(md, k)
		}
		if a, ok := md["annotations"].(map[string]any); ok {
			delete(a, "kubectl.kubernetes.io/last-applied-configuration")
			delete(a, "deployment.kubernetes.io/revision")
			if len(a) == 0 {
				delete(md, "annotations")
			}
		}
	}
	return c
}

func deepCopy(o Object) Object {
	b, _ := json.Marshal(o)
	var c Object
	json.Unmarshal(b, &c)
	return c
}

// ---------------------------------------------------------------------------
// Actions on workloads and nodes

func (c *Conn) patch(ctx context.Context, rt ResourceType, ns, name string, pt types.PatchType, body []byte, sub ...string) error {
	if err := validName(name); err != nil {
		return err
	}
	req := c.raw().Patch(pt).AbsPath(rt.ObjectPath(nsFor(rt, ns), name)).Body(body).Param("fieldManager", "opsdeck")
	if len(sub) > 0 {
		req = req.SubResource(sub...)
	}
	return req.Do(ctx).Error()
}

func isWorkload(rt ResourceType) bool {
	switch rt.ID {
	case "deployments.apps", "statefulsets.apps", "daemonsets.apps":
		return true
	}
	return false
}

// Scale sets the replicas of anything with a scale subresource
// (deployments, statefulsets, replicasets, custom resources).
func (c *Conn) Scale(ctx context.Context, rt ResourceType, ns, name string, replicas int32) error {
	if replicas < 0 {
		return errors.New("replicas must be 0 or more")
	}
	body := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	return c.patch(ctx, rt, ns, name, types.MergePatchType, []byte(body), "scale")
}

// Restart does what kubectl rollout restart does (deployments,
// statefulsets, daemonsets).
func (c *Conn) Restart(ctx context.Context, rt ResourceType, ns, name string) error {
	if !isWorkload(rt) {
		return fmt.Errorf("%s cannot be restarted (deployments, statefulsets and daemonsets can)", rt.ID)
	}
	body := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`, time.Now().Format(time.RFC3339))
	return c.patch(ctx, rt, ns, name, types.StrategicMergePatchType, []byte(body))
}

// SetImage changes a container's image (pods, workloads, jobs, cronjobs).
func (c *Conn) SetImage(ctx context.Context, rt ResourceType, ns, name, container, image string) error {
	if strings.ContainsAny(image, " \n\t") || image == "" {
		return errors.New("invalid image")
	}
	o, err := c.Get(ctx, rt, ns, name)
	if err != nil {
		return err
	}
	podSpec, prefix := podSpecOf(o)
	if podSpec == nil {
		return fmt.Errorf("%s has no containers", rt.ID)
	}
	if container == "" {
		cs, _ := podSpec["containers"].([]any)
		if len(cs) != 1 {
			return errors.New("the object has several containers: say which one")
		}
		container, _ = cs[0].(map[string]any)["name"].(string)
	}
	list := "containers"
	found := false
	for _, l := range []string{"containers", "initContainers"} {
		cs, _ := podSpec[l].([]any)
		for _, x := range cs {
			if m, _ := x.(map[string]any); m["name"] == container {
				list, found = l, true
			}
		}
	}
	if !found {
		return fmt.Errorf("no container %q", container)
	}
	patch := map[string]any{list: []any{map[string]any{"name": container, "image": image}}}
	for i := len(prefix) - 1; i >= 0; i-- {
		patch = map[string]any{prefix[i]: patch}
	}
	body, _ := json.Marshal(patch)
	return c.patch(ctx, rt, ns, name, types.StrategicMergePatchType, body)
}

// podSpecOf finds the pod template spec of an object and its path.
func podSpecOf(o Object) (map[string]any, []string) {
	get := func(path ...string) map[string]any {
		var cur any = o
		for _, p := range path {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[p]
		}
		m, _ := cur.(map[string]any)
		return m
	}
	for _, p := range [][]string{{"spec", "template", "spec"}, {"spec", "jobTemplate", "spec", "template", "spec"}, {"spec"}} {
		if s := get(p...); s != nil && s["containers"] != nil {
			return s, p
		}
	}
	return nil, nil
}

// Pause and resume a deployment rollout.
func (c *Conn) SetPaused(ctx context.Context, rt ResourceType, ns, name string, paused bool) error {
	if rt.ID != "deployments.apps" {
		return errors.New("only deployments can be paused")
	}
	return c.patch(ctx, rt, ns, name, types.MergePatchType, []byte(fmt.Sprintf(`{"spec":{"paused":%t}}`, paused)))
}

// SetSuspended suspends or resumes a CronJob.
func (c *Conn) SetSuspended(ctx context.Context, rt ResourceType, ns, name string, suspend bool) error {
	if rt.ID != "cronjobs.batch" {
		return errors.New("only cronjobs can be suspended")
	}
	return c.patch(ctx, rt, ns, name, types.MergePatchType, []byte(fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend)))
}

// TriggerCronJob starts a Job from a CronJob now (kubectl create job --from).
func (c *Conn) TriggerCronJob(ctx context.Context, ns, name string) (string, error) {
	cj, err := c.CS.BatchV1().CronJobs(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	jobName := fmt.Sprintf("%s-manual-%d", name, time.Now().Unix()%100000)
	if len(jobName) > 63 {
		jobName = jobName[len(jobName)-63:]
	}
	ctrl := true
	job := cj.Spec.JobTemplate
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: ns, Labels: job.Labels,
		Annotations:     map[string]string{"cronjob.kubernetes.io/instantiate": "manual"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID, Controller: &ctrl}}},
		Spec: job.Spec}
	_, err = c.CS.BatchV1().Jobs(ns).Create(ctx, j, metav1.CreateOptions{FieldManager: "opsdeck"})
	return jobName, err
}

// Cordon marks a node (un)schedulable.
func (c *Conn) Cordon(ctx context.Context, node string, on bool) error {
	_, err := c.CS.CoreV1().Nodes().Patch(ctx, node, types.MergePatchType, []byte(fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, on)), metav1.PatchOptions{FieldManager: "opsdeck"})
	return err
}

// Drain cordons a node and evicts its pods (not DaemonSet or static pods),
// respecting PodDisruptionBudgets. It returns what happened to each pod.
func (c *Conn) Drain(ctx context.Context, node string) ([]string, error) {
	if err := c.Cordon(ctx, node, true); err != nil {
		return nil, err
	}
	pods, err := c.CS.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pods.Items {
		ref := p.Namespace + "/" + p.Name
		if _, mirror := p.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
			out = append(out, ref+": static pod, skipped")
			continue
		}
		if ownerKind(p.OwnerReferences) == "DaemonSet" {
			out = append(out, ref+": DaemonSet pod, skipped")
			continue
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		ev := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace}}
		if err := c.CS.CoreV1().Pods(p.Namespace).EvictV1(ctx, ev); err != nil {
			if apierrors.IsTooManyRequests(err) {
				out = append(out, ref+": blocked by a PodDisruptionBudget, try again later")
			} else {
				out = append(out, ref+": "+err.Error())
			}
			continue
		}
		out = append(out, ref+": evicted")
	}
	return out, nil
}

func ownerKind(refs []metav1.OwnerReference) string {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			return r.Kind
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Rollout history

// Revision is an earlier version of a workload's pod template.
type Revision struct {
	Revision int64    `json:"revision"`
	Name     string   `json:"name"`
	Created  string   `json:"created"`
	Images   []string `json:"images"`
	Cause    string   `json:"cause,omitempty"`
	Current  bool     `json:"current,omitempty"`
	Replicas int32    `json:"replicas"`
}

// History lists the revisions of a deployment (its ReplicaSets) or of a
// statefulset/daemonset (its ControllerRevisions), newest first.
func (c *Conn) History(ctx context.Context, rt ResourceType, ns, name string) ([]Revision, error) {
	out := []Revision{}
	switch rt.ID {
	case "deployments.apps":
		d, err := c.CS.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
		if err != nil {
			return nil, err
		}
		rss, err := c.CS.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
		if err != nil {
			return nil, err
		}
		var newest int64
		for _, rs := range rss.Items {
			if !ownedBy(rs.OwnerReferences, d.UID) {
				continue
			}
			rev, _ := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64)
			r := Revision{Revision: rev, Name: rs.Name, Created: rs.CreationTimestamp.UTC().Format(time.RFC3339), Cause: rs.Annotations["kubernetes.io/change-cause"],
				Replicas: rs.Status.Replicas}
			if rev > newest {
				newest = rev
			}
			for _, ct := range rs.Spec.Template.Spec.Containers {
				r.Images = append(r.Images, ct.Image)
			}
			out = append(out, r)
		}
		// the newest revision is the current template (a rollback copies an
		// old one into a new revision, like kubectl rollout undo)
		for i := range out {
			out[i].Current = out[i].Revision == newest
		}
	case "statefulsets.apps", "daemonsets.apps":
		o, err := c.Get(ctx, rt, ns, name)
		if err != nil {
			return nil, err
		}
		md, _ := o["metadata"].(map[string]any)
		uid, _ := md["uid"].(string)
		status, _ := o["status"].(map[string]any)
		curName, _ := status["currentRevision"].(string)
		if rt.ID == "daemonsets.apps" {
			curName = ""
		}
		crs, err := c.CS.AppsV1().ControllerRevisions(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		var newest int64
		for _, cr := range crs.Items {
			if !ownedBy(cr.OwnerReferences, types.UID(uid)) {
				continue
			}
			r := Revision{Revision: cr.Revision, Name: cr.Name, Created: cr.CreationTimestamp.UTC().Format(time.RFC3339),
				Cause: cr.Annotations["kubernetes.io/change-cause"], Current: cr.Name == curName}
			var data struct {
				Spec struct {
					Template struct {
						Spec struct {
							Containers []struct{ Image string } `json:"containers"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
			}
			json.Unmarshal(cr.Data.Raw, &data)
			for _, ct := range data.Spec.Template.Spec.Containers {
				r.Images = append(r.Images, ct.Image)
			}
			if cr.Revision > newest {
				newest = cr.Revision
			}
			out = append(out, r)
		}
		if curName == "" {
			for i := range out {
				out[i].Current = out[i].Revision == newest
			}
		}
	default:
		return nil, fmt.Errorf("%s has no rollout history", rt.ID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
	return out, nil
}

func ownedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, r := range refs {
		if r.UID == uid {
			return true
		}
	}
	return false
}

// Undo rolls a workload back to a revision (0 = the previous one), like
// kubectl rollout undo.
func (c *Conn) Undo(ctx context.Context, rt ResourceType, ns, name string, revision int64) (int64, error) {
	hist, err := c.History(ctx, rt, ns, name)
	if err != nil {
		return 0, err
	}
	var target *Revision
	if revision == 0 {
		for i := range hist {
			if !hist[i].Current {
				target = &hist[i]
				break
			}
		}
	} else {
		for i := range hist {
			if hist[i].Revision == revision {
				target = &hist[i]
			}
		}
	}
	if target == nil {
		return 0, errors.New("no earlier revision to roll back to")
	}
	if target.Current {
		return 0, fmt.Errorf("revision %d is the current one", target.Revision)
	}
	switch rt.ID {
	case "deployments.apps":
		rs, err := c.CS.AppsV1().ReplicaSets(ns).Get(ctx, target.Name, metav1.GetOptions{})
		if err != nil {
			return 0, err
		}
		tpl := rs.Spec.Template.DeepCopy()
		delete(tpl.Labels, "pod-template-hash")
		patch, _ := json.Marshal([]map[string]any{{"op": "replace", "path": "/spec/template", "value": tpl}})
		return target.Revision, c.patch(ctx, rt, ns, name, types.JSONPatchType, patch)
	default:
		cr, err := c.CS.AppsV1().ControllerRevisions(ns).Get(ctx, target.Name, metav1.GetOptions{})
		if err != nil {
			return 0, err
		}
		return target.Revision, c.patch(ctx, rt, ns, name, types.StrategicMergePatchType, cr.Data.Raw)
	}
}

// ---------------------------------------------------------------------------
// Related objects

// Selector returns the label selector of a workload or service, for its pods.
func Selector(o Object) (string, error) {
	spec, _ := o["spec"].(map[string]any)
	if spec == nil {
		return "", errors.New("no spec")
	}
	if kind, _ := o["kind"].(string); kind == "Service" {
		sel, _ := spec["selector"].(map[string]any)
		if len(sel) == 0 {
			return "", errors.New("the service has no selector")
		}
		var parts []string
		for k, v := range sel {
			parts = append(parts, k+"="+fmt.Sprint(v))
		}
		sort.Strings(parts)
		return strings.Join(parts, ","), nil
	}
	raw, ok := spec["selector"]
	if !ok {
		return "", errors.New("no selector")
	}
	b, _ := json.Marshal(raw)
	var ls metav1.LabelSelector
	if err := json.Unmarshal(b, &ls); err != nil {
		return "", err
	}
	sel, err := metav1.LabelSelectorAsSelector(&ls)
	if err != nil {
		return "", err
	}
	return sel.String(), nil
}

// Events lists the events about one object, newest first.
func (c *Conn) Events(ctx context.Context, ns, uid string) ([]Event, error) {
	l, err := c.CS.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + uid})
	if err != nil {
		return nil, err
	}
	out := []Event{}
	for _, e := range l.Items {
		out = append(out, toEvent(&e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out, nil
}

// Event is a Kubernetes event.
type Event struct {
	Time      string `json:"time"`
	Type      string `json:"type"` // Normal | Warning
	Reason    string `json:"reason"`
	Object    string `json:"object"` // kind/name
	Namespace string `json:"namespace,omitempty"`
	Message   string `json:"message"`
	Count     int32  `json:"count"`
	Source    string `json:"source,omitempty"`
	UID       string `json:"uid"`
}

func toEvent(e *corev1.Event) Event {
	src := e.Source.Component
	if src == "" {
		src = e.ReportingController
	}
	if e.Source.Host != "" {
		src += " " + e.Source.Host
	}
	count := e.Count
	if e.Series != nil {
		count = e.Series.Count
	}
	if count == 0 {
		count = 1
	}
	return Event{Time: evTime(*e).UTC().Format(time.RFC3339), Type: e.Type, Reason: e.Reason, Namespace: e.Namespace,
		Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name, Message: strings.TrimSpace(e.Message), Count: count,
		Source: strings.TrimSpace(src), UID: string(e.UID)}
}
