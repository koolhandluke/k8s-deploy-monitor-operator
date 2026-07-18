package diagnostic

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

func testEvent() models.RolloutEvent {
	return models.RolloutEvent{
		ClusterID:      "test-cluster",
		ClusterName:    "test-cluster",
		Namespace:      "default",
		DeploymentName: "myapp",
		Timestamp:      time.Now(),
	}
}

func int32Ptr(i int32) *int32 { return &i }

func stableDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "myapp",
			Namespace:  "default",
			UID:        types.UID("deploy-uid"),
			Generation: 2,
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision": "2",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(3),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "myapp"},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  2,
			Replicas:            3,
			UpdatedReplicas:     3,
			AvailableReplicas:   3,
			UnavailableReplicas: 0,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentProgressing,
					Status: corev1.ConditionTrue,
					Reason: "NewReplicaSetAvailable",
				},
			},
		},
	}
}

func newReplicaSet(deployUID types.UID) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "myapp-abc123",
			Namespace: "default",
			UID:       types.UID("rs-uid"),
			Labels:    map[string]string{"app": "myapp"},
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision": "2",
			},
			OwnerReferences: []metav1.OwnerReference{
				{UID: deployUID, Name: "myapp", Kind: "Deployment"},
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "myapp"},
			},
		},
	}
}

func readyPod(name string, rsUID types.UID) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{"app": "myapp"},
			OwnerReferences: []metav1.OwnerReference{
				{UID: rsUID, Name: "myapp-abc123", Kind: "ReplicaSet"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "myapp",
					Ready:        true,
					RestartCount: 0,
					State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		},
	}
}

func TestFilterErrorLines(t *testing.T) {
	input := []byte(`INFO starting server
INFO listening on :8080
ERROR failed to connect to database
INFO request handled
FATAL out of memory
INFO shutdown
ERROR failed to connect to database
ERROR failed to connect to database
`)

	lines := filterErrorLines(input)

	if len(lines) != 2 {
		t.Fatalf("expected 2 unique error lines, got %d: %v", len(lines), lines)
	}

	if lines[0] != "ERROR failed to connect to database (seen 3 times)" {
		t.Errorf("expected deduplicated error line, got: %s", lines[0])
	}
	if lines[1] != "FATAL out of memory" {
		t.Errorf("expected fatal line, got: %s", lines[1])
	}
}

func TestFilterErrorLines_NoMatches(t *testing.T) {
	input := []byte(`INFO all good
INFO everything fine
`)
	lines := filterErrorLines(input)
	if len(lines) != 0 {
		t.Fatalf("expected 0 lines, got %d", len(lines))
	}
}

func TestCountLines(t *testing.T) {
	input := []byte("line1\nline2\nline3\n")
	if n := countLines(input); n != 3 {
		t.Errorf("expected 3 lines, got %d", n)
	}
}

func TestWaitingReason(t *testing.T) {
	tests := []struct {
		name   string
		cs     corev1.ContainerStatus
		expect string
	}{
		{
			name: "waiting reason",
			cs: corev1.ContainerStatus{
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
			},
			expect: "CrashLoopBackOff",
		},
		{
			name: "terminated reason",
			cs: corev1.ContainerStatus{
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"},
				},
			},
			expect: "OOMKilled",
		},
		{
			name: "last terminated",
			cs: corev1.ContainerStatus{
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"},
				},
			},
			expect: "OOMKilled",
		},
		{
			name:   "no reason",
			cs:     corev1.ContainerStatus{},
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := waitingReason(tt.cs)
			if got != tt.expect {
				t.Errorf("expected %q, got %q", tt.expect, got)
			}
		})
	}
}

func TestIsPodFailing(t *testing.T) {
	tests := []struct {
		name   string
		pod    corev1.Pod
		expect bool
	}{
		{
			name: "healthy pod",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{RestartCount: 0, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					},
				},
			},
			expect: false,
		},
		{
			name: "restarted pod",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{RestartCount: 1, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					},
				},
			},
			expect: true,
		},
		{
			name: "crashlooping pod",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
							},
						},
					},
				},
			},
			expect: true,
		},
		{
			name: "image pull backoff",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
							},
						},
					},
				},
			},
			expect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPodFailing(tt.pod)
			if got != tt.expect {
				t.Errorf("expected %v, got %v", tt.expect, got)
			}
		})
	}
}

func TestProgressState_RecordsForwardProgress(t *testing.T) {
	now := time.Now()
	p := &progressState{
		lastUpdated:     1,
		lastAvailable:   1,
		lastUnavailable: 2,
		lastProgressAt:  now,
	}

	// No change — no progress
	p.recordProgress(1, 1, 2, now.Add(time.Second))
	if p.lastProgressAt != now {
		t.Error("expected no progress update for no change")
	}

	// Updated increases — progress
	p.recordProgress(2, 1, 2, now.Add(2*time.Second))
	if p.lastProgressAt != now.Add(2*time.Second) {
		t.Error("expected progress update for updated increase")
	}

	// Available increases — progress
	p.recordProgress(2, 2, 2, now.Add(3*time.Second))
	if p.lastProgressAt != now.Add(3*time.Second) {
		t.Error("expected progress update for available increase")
	}

	// Unavailable decreases — progress
	p.recordProgress(2, 2, 1, now.Add(4*time.Second))
	if p.lastProgressAt != now.Add(4*time.Second) {
		t.Error("expected progress update for unavailable decrease")
	}

	// Backward movement — no progress
	saved := p.lastProgressAt
	p.recordProgress(2, 1, 1, now.Add(5*time.Second))
	if p.lastProgressAt != saved {
		t.Error("expected no progress update for backward movement")
	}
}

func TestClusterRegistry_ClientsetFor(t *testing.T) {
	registry := NewClusterRegistry([]config.ClusterInfo{
		{ID: "cluster-1", Name: "cluster-1", RestConfig: &rest.Config{Host: "https://cluster-1:6443"}},
	})

	_, err := registry.ClientsetFor("cluster-1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	_, err = registry.ClientsetFor("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent cluster")
	}
}

func TestClusterRegistry_Update(t *testing.T) {
	registry := NewClusterRegistry([]config.ClusterInfo{
		{ID: "cluster-1", Name: "cluster-1", RestConfig: &rest.Config{Host: "https://cluster-1:6443"}},
	})

	registry.Update([]config.ClusterInfo{
		{ID: "cluster-2", Name: "cluster-2", RestConfig: &rest.Config{Host: "https://cluster-2:6443"}},
	})

	_, err := registry.ClientsetFor("cluster-1")
	if err == nil {
		t.Fatal("expected error for removed cluster")
	}
	_, err = registry.ClientsetFor("cluster-2")
	if err != nil {
		t.Fatalf("expected no error for new cluster, got: %v", err)
	}
}

func TestFindNewReplicaSet(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	clientset := fake.NewSimpleClientset(deploy, rs)

	analyzer := &RolloutAnalyzer{}
	ctx := context.Background()

	found, err := analyzer.findNewReplicaSet(ctx, clientset, deploy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find new replica set")
	}
	if found.Name != "myapp-abc123" {
		t.Errorf("expected myapp-abc123, got %s", found.Name)
	}
}

func TestPodsForReplicaSet(t *testing.T) {
	rs := newReplicaSet(types.UID("deploy-uid"))
	pod1 := readyPod("myapp-abc123-p1", rs.UID)
	pod2 := readyPod("myapp-abc123-p2", rs.UID)
	// Unrelated pod — different owner
	unrelated := readyPod("other-pod", types.UID("other-rs"))

	clientset := fake.NewSimpleClientset(rs, pod1, pod2, unrelated)

	analyzer := &RolloutAnalyzer{}
	ctx := context.Background()

	pods, err := analyzer.podsForReplicaSet(ctx, clientset, rs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(pods))
	}
}

func TestCollectEvents(t *testing.T) {
	event := testEvent()

	// Related warning event
	relatedEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "evt1", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			Name: "myapp-abc123-p1",
			Kind: "Pod",
		},
		Reason:  "BackOff",
		Message: "Back-off restarting failed container",
		Type:    "Warning",
		Count:   3,
	}
	// Unrelated warning event
	unrelatedEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "evt2", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			Name: "other-deploy-xyz",
			Kind: "Pod",
		},
		Reason:  "BackOff",
		Message: "some other error",
		Type:    "Warning",
		Count:   1,
	}
	// Info event — should not appear (field selector won't filter in fake, but we test the logic)
	clientset := fake.NewSimpleClientset(relatedEvent, unrelatedEvent)

	analyzer := &RolloutAnalyzer{}
	ctx := context.Background()

	events := analyzer.collectEvents(ctx, clientset, event)

	// The fake client doesn't support field selectors, so both Warning events come back.
	// But name prefix filtering should exclude the unrelated one.
	foundRelated := false
	for _, e := range events {
		if e.Object == "Pod/myapp-abc123-p1" {
			foundRelated = true
			if e.Count != 3 {
				t.Errorf("expected count 3, got %d", e.Count)
			}
		}
		if e.Object == "Pod/other-deploy-xyz" {
			t.Error("unrelated event should have been filtered out")
		}
	}
	if !foundRelated {
		t.Error("expected to find the related event")
	}
}

func TestAsyncDiagnosticTarget_DropsWhenFull(t *testing.T) {
	registry := NewClusterRegistry([]config.ClusterInfo{
		{ID: "test-cluster", Name: "test-cluster", RestConfig: &rest.Config{Host: "https://localhost:6443"}},
	})
	analyzer := NewRolloutAnalyzer(registry)
	target := NewAsyncDiagnosticTarget(analyzer, 1)
	defer target.Stop()

	// Fill the semaphore
	target.semaphore <- struct{}{}

	// This should not block — event should be dropped
	event := testEvent()
	err := target.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// Release semaphore
	<-target.semaphore
}

func TestAsyncDiagnosticTarget_Name(t *testing.T) {
	target := NewAsyncDiagnosticTarget(nil, 1)
	defer target.Stop()
	if target.Name() != "diagnostic" {
		t.Errorf("expected 'diagnostic', got %q", target.Name())
	}
}

func TestAsyncDiagnosticTarget_RejectsAfterStop(t *testing.T) {
	registry := NewClusterRegistry([]config.ClusterInfo{
		{ID: "test-cluster", Name: "test-cluster", RestConfig: &rest.Config{Host: "https://localhost:6443"}},
	})
	analyzer := NewRolloutAnalyzer(registry)
	target := NewAsyncDiagnosticTarget(analyzer, 10)

	target.Stop()

	// Dispatch after Stop should be rejected, not panic or block
	event := testEvent()
	err := target.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestAsyncDiagnosticTarget_StopDrainsInFlight(t *testing.T) {
	registry := NewClusterRegistry([]config.ClusterInfo{
		{ID: "test-cluster", Name: "test-cluster", RestConfig: &rest.Config{Host: "https://localhost:6443"}},
	})
	analyzer := NewRolloutAnalyzer(registry)
	target := NewAsyncDiagnosticTarget(analyzer, 10)

	// Dispatch an event — it will fail quickly (fake clientset from registry)
	// but the goroutine lifecycle is what we're testing
	event := testEvent()
	_ = target.Dispatch(context.Background(), event)

	// Stop should wait for the goroutine to finish and not hang
	done := make(chan struct{})
	go func() {
		target.Stop()
		close(done)
	}()

	select {
	case <-done:
		// good — Stop returned
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within timeout — goroutine leak")
	}
}
