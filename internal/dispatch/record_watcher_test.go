package dispatch

import (
	"context"
	"testing"
	"time"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func newFakeDynClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "deploy-monitor.io",
		Version: "v1alpha1",
		Kind:    "RolloutRecord",
	}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "deploy-monitor.io",
		Version: "v1alpha1",
		Kind:    "RolloutRecordList",
	}, &unstructured.UnstructuredList{})

	return dynamicfake.NewSimpleDynamicClient(scheme, objects...)
}

func makeRecord(name, namespace string, phase v1alpha1.RolloutPhase, createdAt time.Time) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "deploy-monitor.io",
		Version: "v1alpha1",
		Kind:    "RolloutRecord",
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetCreationTimestamp(metav1.NewTime(createdAt))
	u.SetResourceVersion("1000")
	u.Object["spec"] = map[string]interface{}{
		"clusterID":       "cluster-1",
		"namespace":       "default",
		"deployment":      "web-app",
		"oldTemplateHash": "abc123",
		"newTemplateHash": "def456",
		"detectedAt":      createdAt.Format(time.RFC3339),
	}
	u.Object["status"] = map[string]interface{}{
		"phase": string(phase),
	}
	return u
}

func TestRecordWatcher_HandleRecord_SkipsNonDetected(t *testing.T) {
	dynClient := newFakeDynClient()
	dispatcher := NewStandaloneDispatcher([]Target{&LogTarget{}})

	rw := NewRecordWatcher(dynClient, dispatcher, "rollout-monitor")

	// Create a record with phase Dispatched (should be skipped)
	record := makeRecord("test-1", "rollout-monitor", v1alpha1.PhaseDispatched, time.Now())

	// This should not panic or attempt to claim
	rw.handleRecord(context.Background(), record)
}

func TestRecordWatcher_ToRolloutEvent(t *testing.T) {
	dynClient := newFakeDynClient()
	dispatcher := NewStandaloneDispatcher([]Target{&LogTarget{}})

	rw := NewRecordWatcher(dynClient, dispatcher, "rollout-monitor")

	now := time.Now().Truncate(time.Second)
	record := makeRecord("test-1", "rollout-monitor", v1alpha1.PhaseDetected, now)
	record.Object["spec"].(map[string]interface{})["oldImages"] = []interface{}{"app:v1"}
	record.Object["spec"].(map[string]interface{})["newImages"] = []interface{}{"app:v2"}

	event := rw.toRolloutEvent(record)

	if event.ClusterID != "cluster-1" {
		t.Errorf("expected clusterID=cluster-1, got %s", event.ClusterID)
	}
	if event.Namespace != "default" {
		t.Errorf("expected namespace=default, got %s", event.Namespace)
	}
	if event.DeploymentName != "web-app" {
		t.Errorf("expected deployment=web-app, got %s", event.DeploymentName)
	}
	if len(event.OldImages) != 1 || event.OldImages[0] != "app:v1" {
		t.Errorf("unexpected oldImages: %v", event.OldImages)
	}
	if len(event.NewImages) != 1 || event.NewImages[0] != "app:v2" {
		t.Errorf("unexpected newImages: %v", event.NewImages)
	}
	if !event.Timestamp.Equal(now) {
		t.Errorf("expected timestamp=%v, got %v", now, event.Timestamp)
	}
}

func TestRecordWatcher_ClaimRecord_Success(t *testing.T) {
	record := makeRecord("test-1", "rollout-monitor", v1alpha1.PhaseDetected, time.Now())
	dynClient := newFakeDynClient(record)
	dispatcher := NewStandaloneDispatcher([]Target{&LogTarget{}})

	rw := NewRecordWatcher(dynClient, dispatcher, "rollout-monitor")

	claimed := rw.claimRecord(context.Background(), record)
	if !claimed {
		t.Error("expected claim to succeed")
	}
}

func TestGetNestedString(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": map[string]interface{}{
				"phase": "Detected",
			},
		},
	}

	got := getNestedString(u, "status", "phase")
	if got != "Detected" {
		t.Errorf("expected Detected, got %s", got)
	}

	got = getNestedString(u, "status", "missing")
	if got != "" {
		t.Errorf("expected empty, got %s", got)
	}

	got = getNestedString(u, "nonexistent", "phase")
	if got != "" {
		t.Errorf("expected empty, got %s", got)
	}
}
