package diagnostic

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSoak_NoRegression(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := readyPod("myapp-abc123-p1", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, reason := analyzer.soak(context.Background(), clientset, event, deploy)

	if result != ResultSuccess {
		t.Errorf("expected SUCCESS, got %s (reason: %s)", result, reason)
	}
}

func TestSoak_RestartCountIncreases(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := readyPod("myapp-abc123-p1", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	callCount := 0
	clientset.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		callCount++
		p := readyPod("myapp-abc123-p1", rs.UID)
		if callCount > 1 {
			// After soak: restarts increased
			p.Status.ContainerStatuses[0].RestartCount = 1
		}
		return true, &corev1.PodList{Items: []corev1.Pod{*p}}, nil
	})

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, reason := analyzer.soak(context.Background(), clientset, event, deploy)

	if result != ResultUnstable {
		t.Errorf("expected UNSTABLE, got %s (reason: %s)", result, reason)
	}
}

func TestSoak_PodDropsReady(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := readyPod("myapp-abc123-p1", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	callCount := 0
	clientset.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		callCount++
		p := readyPod("myapp-abc123-p1", rs.UID)
		if callCount > 1 {
			// After soak: pod dropped out of Ready
			p.Status.Conditions = []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			}
		}
		return true, &corev1.PodList{Items: []corev1.Pod{*p}}, nil
	})

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, reason := analyzer.soak(context.Background(), clientset, event, deploy)

	if result != ResultUnstable {
		t.Errorf("expected UNSTABLE, got %s (reason: %s)", result, reason)
	}
}

func TestSoak_ReplicaCountRegresses(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := readyPod("myapp-abc123-p1", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	// soak() re-fetches the deployment once after the soak period.
	// Return regressed replica counts on that re-fetch.
	clientset.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		d := stableDeployment()
		d.Status.AvailableReplicas = 2
		d.Status.UnavailableReplicas = 1
		return true, d, nil
	})

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, reason := analyzer.soak(context.Background(), clientset, event, deploy)

	if result != ResultUnstable {
		t.Errorf("expected UNSTABLE, got %s (reason: %s)", result, reason)
	}
}

func TestSoak_ContextCancelled(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)

	clientset := fake.NewSimpleClientset(deploy, rs)

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, _ := analyzer.soak(ctx, clientset, event, deploy)

	if result != ResultInconclusive {
		t.Errorf("expected INCONCLUSIVE, got %s", result)
	}
}

func TestSoak_RefetchFails(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := readyPod("myapp-abc123-p1", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)

	clientset.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("internal server error")
	})

	analyzer := &RolloutAnalyzer{config: fastConfig()}
	event := testEvent()

	result, _ := analyzer.soak(context.Background(), clientset, event, deploy)

	if result != ResultInconclusive {
		t.Errorf("expected INCONCLUSIVE, got %s", result)
	}
}
