package watcher

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

func makeDeployment(name, namespace, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: name, Image: image},
					},
				},
			},
		},
	}
}

func TestClusterWatcher_DetectsRollout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deploy := makeDeployment("myapp", "default", "myapp:v1")
	clientset := fake.NewSimpleClientset(deploy)

	eventCh := make(chan models.RolloutEvent, 10)
	debouncer := NewDebouncer(100*time.Millisecond, eventCh)
	defer debouncer.Stop()

	w := NewClusterWatcher("test-cluster", "test-cluster", clientset, debouncer, func(string) bool { return true }, nil)
	if err := w.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	defer w.Stop()

	// Wait for cache sync to complete and baseline to be seeded
	time.Sleep(200 * time.Millisecond)

	// Update the deployment image — should trigger rollout detection
	deploy.Spec.Template.Spec.Containers[0].Image = "myapp:v2"
	_, err := clientset.AppsV1().Deployments("default").Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to update deployment: %v", err)
	}

	select {
	case event := <-eventCh:
		if event.DeploymentName != "myapp" {
			t.Errorf("expected myapp, got %s", event.DeploymentName)
		}
		if len(event.NewImages) == 0 || event.NewImages[0] != "myapp:v2" {
			t.Errorf("expected myapp:v2, got %v", event.NewImages)
		}
		if event.ClusterID != "test-cluster" {
			t.Errorf("expected test-cluster, got %s", event.ClusterID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for rollout event")
	}
}

func TestClusterWatcher_IgnoresStatusUpdates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deploy := makeDeployment("myapp", "default", "myapp:v1")
	clientset := fake.NewSimpleClientset(deploy)

	eventCh := make(chan models.RolloutEvent, 10)
	debouncer := NewDebouncer(50*time.Millisecond, eventCh)
	defer debouncer.Stop()

	w := NewClusterWatcher("test-cluster", "test-cluster", clientset, debouncer, func(string) bool { return true }, nil)
	if err := w.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	defer w.Stop()

	time.Sleep(200 * time.Millisecond)

	// Update only status (not template) — should NOT trigger rollout
	deploy.Status.ReadyReplicas = 3
	_, err := clientset.AppsV1().Deployments("default").UpdateStatus(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to update deployment status: %v", err)
	}

	select {
	case event := <-eventCh:
		t.Fatalf("unexpected rollout event for status-only update: %+v", event)
	case <-time.After(500 * time.Millisecond):
		// expected — no event
	}
}

func TestClusterWatcher_NamespaceFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deploy := makeDeployment("myapp", "kube-system", "myapp:v1")
	clientset := fake.NewSimpleClientset(deploy)

	eventCh := make(chan models.RolloutEvent, 10)
	debouncer := NewDebouncer(50*time.Millisecond, eventCh)
	defer debouncer.Stop()

	// Filter out kube-system
	nsFilter := func(ns string) bool { return ns != "kube-system" }

	w := NewClusterWatcher("test-cluster", "test-cluster", clientset, debouncer, nsFilter, nil)
	if err := w.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	defer w.Stop()

	time.Sleep(200 * time.Millisecond)

	deploy.Spec.Template.Spec.Containers[0].Image = "myapp:v2"
	_, err := clientset.AppsV1().Deployments("kube-system").Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to update deployment: %v", err)
	}

	select {
	case event := <-eventCh:
		t.Fatalf("unexpected event for filtered namespace: %+v", event)
	case <-time.After(500 * time.Millisecond):
		// expected — filtered out
	}
}

func TestTemplateHash_DifferentImages(t *testing.T) {
	d1 := makeDeployment("app", "default", "app:v1")
	d2 := makeDeployment("app", "default", "app:v2")

	h1 := templateHash(d1)
	h2 := templateHash(d2)

	if h1 == h2 {
		t.Error("different images should produce different hashes")
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex SHA256, got len=%d", len(h1))
	}
}

func TestTemplateHash_SameImage(t *testing.T) {
	d1 := makeDeployment("app", "default", "app:v1")
	d2 := makeDeployment("app", "default", "app:v1")

	if templateHash(d1) != templateHash(d2) {
		t.Error("identical templates should produce identical hashes")
	}
}

func TestExtractImages(t *testing.T) {
	d := makeDeployment("app", "default", "app:v1")
	d.Spec.Template.Spec.Containers = append(d.Spec.Template.Spec.Containers,
		corev1.Container{Name: "sidecar", Image: "sidecar:latest"})

	images := extractImages(d)
	if len(images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(images))
	}
	if images[0] != "app:v1" || images[1] != "sidecar:latest" {
		t.Errorf("unexpected images: %v", images)
	}
}

func TestExtractImages_Nil(t *testing.T) {
	images := extractImages(nil)
	if images != nil {
		t.Errorf("expected nil for nil deployment, got %v", images)
	}
}
