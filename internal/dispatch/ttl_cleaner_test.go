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
	k8stesting "k8s.io/client-go/testing"
)

func newFakeDynClientForTTL(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
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

func TestTTLCleaner_DeletesOldDispatchedRecords(t *testing.T) {
	oldTime := time.Now().Add(-10 * 24 * time.Hour) // 10 days ago
	record := makeRecord("old-dispatched", "rollout-monitor", v1alpha1.PhaseDispatched, oldTime)

	dynClient := newFakeDynClientForTTL(record)
	cleaner := NewTTLCleaner(dynClient, "rollout-monitor", 7)

	cleaner.cleanup(context.Background())

	// Verify delete was called
	actions := dynClient.Actions()
	var deleteFound bool
	for _, action := range actions {
		if action.GetVerb() == "delete" {
			deleteAction := action.(k8stesting.DeleteAction)
			if deleteAction.GetName() == "old-dispatched" {
				deleteFound = true
			}
		}
	}
	if !deleteFound {
		t.Error("expected old dispatched record to be deleted")
	}
}

func TestTTLCleaner_DeletesOldFailedRecords(t *testing.T) {
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	record := makeRecord("old-failed", "rollout-monitor", v1alpha1.PhaseFailed, oldTime)

	dynClient := newFakeDynClientForTTL(record)
	cleaner := NewTTLCleaner(dynClient, "rollout-monitor", 7)

	cleaner.cleanup(context.Background())

	actions := dynClient.Actions()
	var deleteFound bool
	for _, action := range actions {
		if action.GetVerb() == "delete" {
			deleteAction := action.(k8stesting.DeleteAction)
			if deleteAction.GetName() == "old-failed" {
				deleteFound = true
			}
		}
	}
	if !deleteFound {
		t.Error("expected old failed record to be deleted")
	}
}

func TestTTLCleaner_KeepsRecentRecords(t *testing.T) {
	recentTime := time.Now().Add(-2 * 24 * time.Hour) // 2 days ago
	record := makeRecord("recent-dispatched", "rollout-monitor", v1alpha1.PhaseDispatched, recentTime)

	dynClient := newFakeDynClientForTTL(record)
	cleaner := NewTTLCleaner(dynClient, "rollout-monitor", 7)

	cleaner.cleanup(context.Background())

	// Verify no delete was called
	actions := dynClient.Actions()
	for _, action := range actions {
		if action.GetVerb() == "delete" {
			t.Error("recent record should not be deleted")
		}
	}
}

func TestTTLCleaner_KeepsDetectedRecords(t *testing.T) {
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	record := makeRecord("old-detected", "rollout-monitor", v1alpha1.PhaseDetected, oldTime)

	dynClient := newFakeDynClientForTTL(record)
	cleaner := NewTTLCleaner(dynClient, "rollout-monitor", 7)

	cleaner.cleanup(context.Background())

	// Verify no delete was called for Detected records even if old
	actions := dynClient.Actions()
	for _, action := range actions {
		if action.GetVerb() == "delete" {
			t.Error("detected record should not be deleted regardless of age")
		}
	}
}

func TestTTLCleaner_KeepsProcessingRecords(t *testing.T) {
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	record := &unstructured.Unstructured{}
	record.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "deploy-monitor.io",
		Version: "v1alpha1",
		Kind:    "RolloutRecord",
	})
	record.SetName("old-processing")
	record.SetNamespace("rollout-monitor")
	record.SetCreationTimestamp(metav1.NewTime(oldTime))
	record.SetResourceVersion("1000")
	record.Object["spec"] = map[string]interface{}{
		"clusterID":       "cluster-1",
		"clusterName":     "prod",
		"namespace":       "default",
		"deployment":      "web-app",
		"oldTemplateHash": "abc",
		"newTemplateHash": "def",
		"detectedAt":      oldTime.Format(time.RFC3339),
	}
	record.Object["status"] = map[string]interface{}{
		"phase": string(v1alpha1.PhaseProcessing),
	}

	dynClient := newFakeDynClientForTTL(record)
	cleaner := NewTTLCleaner(dynClient, "rollout-monitor", 7)

	cleaner.cleanup(context.Background())

	actions := dynClient.Actions()
	for _, action := range actions {
		if action.GetVerb() == "delete" {
			t.Error("processing record should not be deleted")
		}
	}
}
