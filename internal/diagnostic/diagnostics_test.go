package diagnostic

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func failingPod(name string, rsUID types.UID) *corev1.Pod {
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
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "myapp",
					Ready:        false,
					RestartCount: 3,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				},
			},
		},
	}
}

func initCrashPod(name string, rsUID types.UID) *corev1.Pod {
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
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "init-db",
					Ready:        false,
					RestartCount: 4,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "myapp",
					Ready: false,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"},
					},
				},
			},
		},
	}
}

func TestGatherDiagnostics_CollectsEvents(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := failingPod("myapp-abc123-fail", rs.UID)

	relatedEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "evt1", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			Name: "myapp-abc123-fail",
			Kind: "Pod",
		},
		Reason:  "BackOff",
		Message: "Back-off restarting failed container",
		Type:    "Warning",
		Count:   5,
	}
	unrelatedEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "evt2", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			Name: "other-deploy-xyz",
			Kind: "Pod",
		},
		Reason: "BackOff",
		Type:   "Warning",
		Count:  1,
	}

	clientset := fake.NewSimpleClientset(deploy, rs, pod, relatedEvent, unrelatedEvent)
	analyzer := &RolloutAnalyzer{config: DefaultAnalyzerConfig()}
	event := testEvent()
	report := &DiagnosticReport{Event: event}

	analyzer.gatherDiagnostics(context.Background(), clientset, event, report)

	if len(report.Events) == 0 {
		t.Fatal("expected events to be collected")
	}

	for _, ev := range report.Events {
		if ev.Object == "Pod/other-deploy-xyz" {
			t.Error("unrelated event should have been filtered out")
		}
	}
}

func TestGatherDiagnostics_InspectsPodStatus(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := failingPod("myapp-abc123-fail", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)
	analyzer := &RolloutAnalyzer{config: DefaultAnalyzerConfig()}
	event := testEvent()
	report := &DiagnosticReport{Event: event}

	analyzer.gatherDiagnostics(context.Background(), clientset, event, report)

	if len(report.PodStatuses) == 0 {
		t.Fatal("expected pod statuses")
	}

	found := false
	for _, ps := range report.PodStatuses {
		if ps.Name == "myapp-abc123-fail" && ps.Reason == "CrashLoopBackOff" {
			found = true
			if ps.RestartCount != 3 {
				t.Errorf("expected 3 restarts, got %d", ps.RestartCount)
			}
		}
	}
	if !found {
		t.Error("expected to find failing pod status with CrashLoopBackOff reason")
	}
}

func TestGatherDiagnostics_SkipsHealthyPods(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	healthyPod := readyPod("myapp-abc123-healthy", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, healthyPod)
	analyzer := &RolloutAnalyzer{config: DefaultAnalyzerConfig()}
	event := testEvent()
	report := &DiagnosticReport{Event: event}

	analyzer.gatherDiagnostics(context.Background(), clientset, event, report)

	// Healthy pods should have status but no logs
	if len(report.LogSnippets) != 0 {
		t.Errorf("expected no log snippets for healthy pods, got %d", len(report.LogSnippets))
	}
}

func TestGatherDiagnostics_InitContainerStatus(t *testing.T) {
	deploy := stableDeployment()
	rs := newReplicaSet(deploy.UID)
	pod := initCrashPod("myapp-abc123-initfail", rs.UID)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)
	analyzer := &RolloutAnalyzer{config: DefaultAnalyzerConfig()}
	event := testEvent()
	report := &DiagnosticReport{Event: event}

	analyzer.gatherDiagnostics(context.Background(), clientset, event, report)

	foundInit := false
	for _, ps := range report.PodStatuses {
		if ps.InitContainer && ps.ContainerName == "init-db" {
			foundInit = true
			if ps.Reason != "CrashLoopBackOff" {
				t.Errorf("expected CrashLoopBackOff reason for init container, got %s", ps.Reason)
			}
		}
	}
	if !foundInit {
		t.Error("expected init container status to be collected")
	}
}
