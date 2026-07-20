package diagnostic

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic/testdata"
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
	deploy := yamlToDeploy(t, testdata.DeploymentStable)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	pod := yamlToPod(t, testdata.PodReady)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, reason := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultSuccess {
		t.Errorf("expected SUCCESS, got %s (reason: %s)", result, reason)
	}
}

func TestMonitorRollout_FailsOnDeadlineExceeded(t *testing.T) {
	deploy := yamlToDeploy(t, testdata.DeploymentDeadlineExceeded)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	pod := yamlToPod(t, testdata.PodReady)

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
	deploy := yamlToDeploy(t, testdata.DeploymentProgressing)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)

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

// TestMonitorRollout_AbsoluteTimeoutStall tests the absolute timeout path when
// progress is stale. This is distinct from TestMonitorRollout_StallTimeout which
// exits via the inactivity check (analyzer.go:183). Here the absolute deadline
// check (analyzer.go:130) runs first in the loop body and catches stale progress
// before the inactivity check at the bottom.
func TestMonitorRollout_AbsoluteTimeoutStall(t *testing.T) {
	deploy := yamlToDeploy(t, testdata.DeploymentProgressing)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)

	clientset := fake.NewSimpleClientset(deploy, rs)

	// Return zero replica counts so recordProgress(0,0,0) never records
	// forward movement (initial progressState is also all zeros).
	clientset.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		d := deploy.DeepCopy()
		d.Status.UpdatedReplicas = 0
		d.Status.AvailableReplicas = 0
		d.Status.UnavailableReplicas = 0
		return true, d, nil
	})

	cfg := fastConfig()
	// Both timeouts equal: on the tick when they expire, the absolute check
	// (top of loop) fires before the inactivity check (bottom of loop).
	cfg.AbsoluteTimeout = 10 * time.Millisecond
	cfg.InactivityTimeout = 10 * time.Millisecond
	analyzer := &RolloutAnalyzer{config: cfg}
	event := testEvent()

	result, reason := analyzer.monitorRollout(context.Background(), clientset, event)

	if result != ResultStalled {
		t.Errorf("expected STALLED, got %s (reason: %s)", result, reason)
	}
	if reason != "absolute timeout reached with no recent progress" {
		t.Errorf("expected absolute timeout stall reason, got: %s", reason)
	}
}

func TestMonitorRollout_AbsoluteTimeout(t *testing.T) {
	// Deployment makes progress (replica counts change) but never fully converges
	deploy := yamlToDeploy(t, testdata.DeploymentProgressing)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)

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
	deploy := yamlToDeploy(t, testdata.DeploymentProgressing)
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
	deploy := yamlToDeploy(t, testdata.DeploymentGenerationMismatch)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	pod := yamlToPod(t, testdata.PodReady)

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
		d := yamlToDeploy(t, testdata.DeploymentStable)
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
	deploy := yamlToDeploy(t, testdata.DeploymentProgressing)
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
	deploy := yamlToDeploy(t, testdata.DeploymentPaused)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)

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
	deploy := yamlToDeploy(t, testdata.DeploymentProgressing)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	pod := yamlToPod(t, testdata.PodReady)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	callCount := 0
	clientset.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		callCount++
		if callCount == 1 {
			return true, yamlToDeploy(t, testdata.DeploymentProgressing), nil
		}
		return true, yamlToDeploy(t, testdata.DeploymentStable), nil
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
	deploy := yamlToDeploy(t, testdata.DeploymentProgressing)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	pod := yamlToPod(t, testdata.PodImagePullBackoff)

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
