package kube

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKubeHelpers(t *testing.T) {
	replicas := int32(2)
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "x"}}},
			Spec: corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "web", Image: "nginx:1.27"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
				{Name: "web", Ready: false, RestartCount: 7, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"},
			Spec:   appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx:1.27"}}}}},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1}},
	)
	ctx := context.Background()
	pods, err := List(ctx, cs, "pods", "shop")
	if err != nil {
		t.Fatal(err)
	}
	if pods[0]["status"] != "CrashLoopBackOff" || pods[0]["restarts"] != int32(7) || pods[0]["ready"] != "0/1" || pods[0]["containers"] != "web" {
		t.Fatalf("pod row: %v", pods[0])
	}
	deps, _ := List(ctx, cs, "deployments", "")
	if deps[0]["ready"] != "1/2" {
		t.Fatalf("deployment row: %v", deps[0])
	}
	for _, typ := range Types {
		if _, err := List(ctx, cs, typ, ""); err != nil {
			t.Fatalf("list %s: %v", typ, err)
		}
	}
	y, err := YAML(ctx, cs, "pods", "shop", "web-1")
	if err != nil || !strings.Contains(y, "kind: Pod") || strings.Contains(y, "managedFields") {
		t.Fatalf("yaml: %v\n%s", err, y)
	}
	if err := RestartDeployment(ctx, cs, "shop", "web"); err != nil {
		t.Fatal(err)
	}
	d, _ := cs.AppsV1().Deployments("shop").Get(ctx, "web", metav1.GetOptions{})
	if d.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] == "" {
		t.Fatal("restart annotation missing")
	}
	if err := DeletePod(ctx, cs, "shop", "web-1"); err != nil {
		t.Fatal(err)
	}
	if s, err := RolloutStatus(ctx, cs, "shop", "web"); err != nil || !strings.Contains(s, "wanted=2") {
		t.Fatalf("rollout: %q %v", s, err)
	}
}
