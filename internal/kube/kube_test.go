package kube

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDocsDiffAndMask(t *testing.T) {
	docs, err := Docs(`apiVersion: v1
kind: ConfigMap
metadata: {name: a}
data: {x: "1"}
---
# comment only
---
apiVersion: v1
kind: List
items:
- {apiVersion: v1, kind: Secret, metadata: {name: s}, data: {password: aHVudGVyMg==}}
- {apiVersion: v1, kind: Service, metadata: {name: svc}}
`)
	if err != nil || len(docs) != 3 || docs[1]["kind"] != "Secret" {
		t.Fatalf("docs: %v %v", err, docs)
	}
	if _, err := Docs("# nothing"); err == nil {
		t.Fatal("empty manifest accepted")
	}

	// secrets never show their values, but changes still show up
	before := Object{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "s", "resourceVersion": "7", "uid": "u",
		"managedFields": []any{map[string]any{"manager": "x"}}}, "data": map[string]any{"password": "aHVudGVyMg=="}}
	after := deepCopy(before)
	after["data"].(map[string]any)["password"] = base64.StdEncoding.EncodeToString([]byte("hunter3"))
	d := Diff(before, after)
	if d == "" || strings.Contains(d, "hunter") || strings.Contains(d, "aHVudGVy") || strings.Contains(d, "resourceVersion") || !strings.Contains(d, "<hidden: 7 bytes") {
		t.Fatalf("diff:\n%s", d)
	}
	if Diff(before, deepCopy(before)) != "" {
		t.Fatal("no-op diff not empty")
	}
	// masking works on a copy: rendering twice reports the real size
	y, _ := YAMLOf(before, false)
	if y2, _ := YAMLOf(before, false); y2 != y || !strings.Contains(y, "<hidden: 7 bytes") || MaskSecret(before)["data"].(map[string]any)["password"] != "<hidden: 7 bytes, sha256 f52fbd32>" {
		t.Fatalf("masking changed the object:\n%s", y2)
	}
	if strings.Contains(y, "aHVudGVy") {
		t.Fatalf("masked yaml shows the value:\n%s", y)
	}
	if y, _ := YAMLOf(deepCopy(before), true); !strings.Contains(y, "aHVudGVyMg==") {
		t.Fatal("revealed yaml hides the value")
	}

	// the last-applied annotation is dropped from what is shown, on a copy
	la := Object{"kind": "ConfigMap", "metadata": map[string]any{"name": "c", "annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "{}"}}}
	if md := Display(la)["metadata"].(map[string]any); md["annotations"] != nil {
		t.Fatalf("display: %v", md)
	}
	if la["metadata"].(map[string]any)["annotations"] == nil {
		t.Fatal("display changed the object")
	}
}

func TestSelectorAndSince(t *testing.T) {
	sel, err := Selector(Object{"kind": "Deployment", "spec": map[string]any{"selector": map[string]any{
		"matchLabels":      map[string]any{"app": "web"},
		"matchExpressions": []any{map[string]any{"key": "tier", "operator": "In", "values": []any{"a", "b"}}}}}})
	if err != nil || sel != "app=web,tier in (a,b)" {
		t.Fatalf("selector %q %v", sel, err)
	}
	if sel, _ := Selector(Object{"kind": "Service", "spec": map[string]any{"selector": map[string]any{"b": "2", "a": "1"}}}); sel != "a=1,b=2" {
		t.Fatalf("service selector %q", sel)
	}
	var o corev1.PodLogOptions
	if applySince(&o, "2d") != nil || *o.SinceSeconds != 172800 {
		t.Fatal("since 2d")
	}
	o = corev1.PodLogOptions{}
	if applySince(&o, "2026-10-22T19:10:01.5Z") != nil || o.SinceTime == nil {
		t.Fatal("since time")
	}
	if applySince(&o, "yesterday") == nil {
		t.Fatal("bad since accepted")
	}
	ts, line := splitTS("2026-10-22T19:10:01.5Z hello world")
	if ts != "2026-10-22T19:10:01.500000000Z" || line != "hello world" {
		t.Fatalf("splitTS %q %q", ts, line)
	}
	if ts, line := splitTS("no time here"); ts != "" || line != "no time here" {
		t.Fatal("splitTS without time")
	}
}

func TestFindResource(t *testing.T) {
	types := []ResourceType{
		{ID: "deployments.apps", Group: "apps", Resource: "deployments", Kind: "Deployment", ShortNames: []string{"deploy"}},
		{ID: "deployments.example.com", Group: "example.com", Resource: "deployments", Kind: "Deployment"},
		{ID: "pods", Resource: "pods", Kind: "Pod", ShortNames: []string{"po"}},
	}
	for in, want := range map[string]string{"deploy": "deployments.apps", "Deployment": "deployments.apps", "deployments": "deployments.apps",
		"deployments.example.com": "deployments.example.com", "pod": "pods", "po": "pods"} {
		if rt, ok := find(types, in); !ok || rt.ID != want {
			t.Errorf("find(%q) = %v %v, want %s", in, rt.ID, ok, want)
		}
	}
	if (ResourceType{Group: "apps", Version: "v1", Resource: "deployments", Namespaced: true}).ObjectPath("shop", "web") != "/apis/apps/v1/namespaces/shop/deployments/web" ||
		(ResourceType{Version: "v1", Resource: "nodes"}).ObjectPath("ignored", "n1") != "/api/v1/nodes/n1" {
		t.Fatal("paths")
	}
}

func TestPodProblems(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	crash := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", CreationTimestamp: old},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", RestartCount: 9,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m0s"}}}}}}
	if p := podProblems(crash); len(p) != 1 || p[0].Reason != "CrashLoopBackOff" || p[0].Severity != "error" {
		t.Fatalf("crash: %+v", p)
	}
	crash.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}
	if p := podProblems(crash); len(p) != 1 || p[0].Reason != "CrashLoopBackOff" || !strings.Contains(p[0].Message, "code 1") {
		t.Fatalf("crash between back-offs: %+v", p)
	}
	pending := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "db-0", CreationTimestamp: old}, Status: corev1.PodStatus{Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Message: "0/3 nodes are available: insufficient memory"}}}}
	if p := podProblems(pending); len(p) != 1 || !strings.Contains(p[0].Message, "insufficient memory") {
		t.Fatalf("pending: %+v", p)
	}
	ok := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "fine", CreationTimestamp: old}, Status: corev1.PodStatus{Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{Name: "a", Ready: true}}}}
	if p := podProblems(ok); len(p) != 0 {
		t.Fatalf("healthy pod reported: %+v", p)
	}
}

func TestHelmDecode(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(`{"name":"shop","namespace":"apps","version":3,"info":{"status":"deployed","notes":"hi"},
		"chart":{"metadata":{"name":"shop","version":"1.2.0","appVersion":"2.0"}},"config":{"replicas":2},"manifest":"kind: Service"}`))
	zw.Close()
	h, err := decodeHelm([]byte(base64.StdEncoding.EncodeToString(gz.Bytes())))
	if err != nil || h.Name != "shop" || h.Version != 3 || h.Chart.Metadata.Version != "1.2.0" || h.Config["replicas"] != float64(2) {
		t.Fatalf("helm: %v %+v", err, h)
	}
}
