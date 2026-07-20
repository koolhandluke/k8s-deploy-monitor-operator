package diagnostic

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// fastConfig returns an AnalyzerConfig with very short timeouts for testing.
func fastConfig() AnalyzerConfig {
	return AnalyzerConfig{
		PollInterval:      1 * time.Millisecond,
		InactivityTimeout: 50 * time.Millisecond,
		AbsoluteTimeout:   200 * time.Millisecond,
		SoakPeriod:        1 * time.Millisecond,
		RestartThreshold:  3,
		RestartWindow:     5 * time.Minute,
		ConfigErrorWindow: 50 * time.Millisecond,
		LogTailLines:      100,
	}
}

func TestMonitorRollout_SuccessImmediate(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := readyPod("myapp-abc123-p1", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, reason := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultSuccess {
		t.Errorf("expected SUCCESS, got %s (reason: %s)", result, reason)
	}
}

func TestMonitorRollout_FailsOnDeadlineExceeded(t *testing.T) {
	deploy := deadlineExceededDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := readyPod("myapp-abc123-p1", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, reason := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultFailed {
		t.Errorf("expected FAILED, got %s (reason: %s)", result, reason)
	}
	if reason != "ProgressDeadlineExceeded" {
		t.Errorf("expected ProgressDeadlineExceeded reason, got: %s", reason)
	}
}

func TestMonitorRollout_StallTimeout(t *testing.T) {
	// Deployment never converges, never makes progress
	deploy := progressingDeployment()
	rs := newReplicaSet(deploy.UID)

	clientset := fake.NewSimpleClientset(deploy, rs)

	cfg := fastConfig()
	cfg.InactivityTimeout = 10 * time.Millisecond
	analyzer := &RolloutAnalyzer{config: cfg}
	event := testEvent()

	result, _ := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultStalled {
		t.Errorf("expected STALLED, got %s", result)
	}
}

func TestMonitorRollout_AbsoluteTimeout(t *testing.T) {
	// Deployment makes progress (replica counts change) but never fully converges
	deploy := progressingDeployment()
	rs := newReplicaSet(deploy.UID)

	clientset := fake.NewSimpleClientset(deploy, rs)

	callCount := 0
	clientset.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		callCount++
		d := deploy.DeepCopy()
		// Simulate slow progress: updated replicas increase over time
		d.Status.UpdatedReplicas = int32(1 + (callCount % 3))
		d.Status.AvailableReplicas = int32(1 + (callCount % 2))
		d.Status.UnavailableReplicas = 3 - d.Status.UpdatedReplicas
		return true, d, nil
	})

	cfg := fastConfig()
	cfg.AbsoluteTimeout = 30 * time.Millisecond
	cfg.InactivityTimeout = 200 * time.Millisecond // longer than absolute to get INCONCLUSIVE
	analyzer := &RolloutAnalyzer{config: cfg}
	event := testEvent()

	result, _ := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultInconclusive {
		t.Errorf("expected INCONCLUSIVE, got %s", result)
	}
}

func TestMonitorRollout_ContextCancelled(t *testing.T) {
	deploy := progressingDeployment()
	clientset := fake.NewSimpleClientset(deploy)

	cfg := fastConfig()
	analyzer := &RolloutAnalyzer{config: cfg}
	event := testEvent()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result, _ := analyzer.monitorRollout(ctx, clientset, event)

	if result != ResultInconclusive {
		t.Errorf("expected INCONCLUSIVE, got %s", result)
	}
}

func TestMonitorRollout_GenerationGate(t *testing.T) {
	// Start with generation mismatch, then resolve to stable
	deploy := progressingDeployment()
	deploy.Generation = 3
	deploy.Status.ObservedGeneration = 2
	rs := newReplicaSet(deploy.UID)
	pod := readyPod("myapp-abc123-p1", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	callCount := 0
	clientset.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		callCount++
		if callCount <= 2 {
			// First 2 polls: generation mismatch
			d := deploy.DeepCopy()
			return true, d, nil
		}
		// After that: stable
		d := stableDeployment()
		d.Generation = 3
		d.Status.ObservedGeneration = 3
		return true, d, nil
	})

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, _ := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultSuccess {
		t.Errorf("expected SUCCESS after generation caught up, got %s", result)
	}
	if callCount < 3 {
		t.Errorf("expected at least 3 polls (2 gen mismatch + 1 stable), got %d", callCount)
	}
}

func TestMonitorRollout_Deleted(t *testing.T) {
	deploy := progressingDeployment()
	clientset := fake.NewSimpleClientset(deploy)

	clientset.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "myapp")
	})

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, reason := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultDeleted {
		t.Errorf("expected DELETED, got %s (reason: %s)", result, reason)
	}
}

func TestMonitorRollout_Paused(t *testing.T) {
	deploy := progressingDeployment()
	deploy.Spec.Paused = true
	rs := newReplicaSet(deploy.UID)

	clientset := fake.NewSimpleClientset(deploy, rs)

	cfg := fastConfig()
	cfg.AbsoluteTimeout = 20 * time.Millisecond
	analyzer := &RolloutAnalyzer{config: cfg}
	event := testEvent()

	result, reason := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultPaused {
		t.Errorf("expected PAUSED, got %s (reason: %s)", result, reason)
	}
}

func TestMonitorRollout_SuccessAfterProgressing(t *testing.T) {
	// Deployment starts progressing, then converges
	deploy := progressingDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := readyPod("myapp-abc123-p1", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	callCount := 0
	clientset.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		callCount++
		if callCount == 1 {
			return true, progressingDeployment(), nil
		}
		return true, stableDeployment(), nil
	})

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, _ := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultSuccess {
		t.Errorf("expected SUCCESS, got %s", result)
	}
}

func TestMonitorRollout_GenericWaitingFails(t *testing.T) {
	// Pod stuck in ImagePullBackOff with 0 restarts, should fail after config error window
	deploy := progressingDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "myapp-abc123-imgpull",
			Namespace: "default",
			Labels:    map[string]string{"app": "myapp"},
			OwnerReferences: []metav1.OwnerReference{
				{UID: rs.UID, Name: "myapp-abc123", Kind: "ReplicaSet"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "myapp",
					Ready:        false,
					RestartCount: 0,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
					},
				},
			},
		},
	}

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	cfg := fastConfig()
	cfg.ConfigErrorWindow = 5 * time.Millisecond
	analyzer := &RolloutAnalyzer{config: cfg}
	event := testEvent()

	result, reason := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultFailed {
		t.Errorf("expected FAILED, got %s (reason: %s)", result, reason)
	}
}
