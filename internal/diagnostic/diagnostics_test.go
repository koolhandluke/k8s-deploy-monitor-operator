package diagnostic

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic/testdata"
)

func TestGatherDiagnostics_CollectsEvents(t *testing.T) {
	deploy := yamlToDeploy(t, testdata.DeploymentStable)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)

	pod := yamlToPod(t, testdata.PodCrashloop)
	pod.Name = "myapp-abc123-fail"

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
	deploy := yamlToDeploy(t, testdata.DeploymentStable)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)

	pod := yamlToPod(t, testdata.PodCrashloop)
	pod.Name = "myapp-abc123-fail"

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
			if ps.RestartCount != 4 {
				t.Errorf("expected 4 restarts, got %d", ps.RestartCount)
			}
		}
	}
	if !found {
		t.Error("expected to find failing pod status with CrashLoopBackOff reason")
	}
}

func TestGatherDiagnostics_SkipsHealthyPods(t *testing.T) {
	deploy := yamlToDeploy(t, testdata.DeploymentStable)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	healthyPod := yamlToPod(t, testdata.PodReady)
	healthyPod.Name = "myapp-abc123-healthy"

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
	deploy := yamlToDeploy(t, testdata.DeploymentStable)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	pod := yamlToPod(t, testdata.PodInitCrash)
	pod.Name = "myapp-abc123-initfail"

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

func TestGatherDiagnostics_OOMKilledPod(t *testing.T) {
	deploy := yamlToDeploy(t, testdata.DeploymentStable)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	pod := yamlToPod(t, testdata.PodOOMKilled)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)
	analyzer := &RolloutAnalyzer{config: DefaultAnalyzerConfig()}
	event := testEvent()
	report := &DiagnosticReport{Event: event}

	analyzer.gatherDiagnostics(context.Background(), clientset, event, report)

	if len(report.PodStatuses) == 0 {
		t.Fatal("expected pod statuses for OOMKilled pod")
	}

	found := false
	for _, ps := range report.PodStatuses {
		if ps.Name == "myapp-abc123-oom" && ps.Reason == "OOMKilled" {
			found = true
			if ps.RestartCount != 2 {
				t.Errorf("expected 2 restarts, got %d", ps.RestartCount)
			}
		}
	}
	if !found {
		t.Error("expected to find OOMKilled pod status")
	}
}

func TestGatherDiagnostics_PendingNoStatusPod(t *testing.T) {
	deploy := yamlToDeploy(t, testdata.DeploymentStable)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	pod := yamlToPod(t, testdata.PodPendingNoStatus)

	clientset := fake.NewSimpleClientset(deploy, rs, pod)
	analyzer := &RolloutAnalyzer{config: DefaultAnalyzerConfig()}
	event := testEvent()
	report := &DiagnosticReport{Event: event}

	analyzer.gatherDiagnostics(context.Background(), clientset, event, report)

	// Pod has no container statuses, so no PodStatuses should be reported
	if len(report.PodStatuses) != 0 {
		t.Errorf("expected no pod statuses for pending pod without container status, got %d", len(report.PodStatuses))
	}
	// Should not collect logs either
	if len(report.LogSnippets) != 0 {
		t.Errorf("expected no log snippets, got %d", len(report.LogSnippets))
	}
}

func TestGatherDiagnostics_CollectsEventsFromFixture(t *testing.T) {
	deploy := yamlToDeploy(t, testdata.DeploymentStable)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	pod := yamlToPod(t, testdata.PodCrashloop)

	events := yamlToEvents(t, testdata.EventsWarning)
	objs := []runtime.Object{deploy, rs, pod}
	for i := range events {
		objs = append(objs, &events[i])
	}

	clientset := fake.NewSimpleClientset(objs...)
	analyzer := &RolloutAnalyzer{config: DefaultAnalyzerConfig()}
	event := testEvent()
	report := &DiagnosticReport{Event: event}

	analyzer.gatherDiagnostics(context.Background(), clientset, event, report)

	if len(report.Events) == 0 {
		t.Fatal("expected events to be collected from fixture")
	}

	foundBackOff := false
	foundFailedCreate := false
	for _, ev := range report.Events {
		if ev.Reason == "BackOff" && ev.Object == "Pod/myapp-abc123-crash" {
			foundBackOff = true
			if ev.Count != 5 {
				t.Errorf("expected BackOff count 5, got %d", ev.Count)
			}
		}
		if ev.Reason == "FailedCreate" && ev.Object == "ReplicaSet/myapp-abc123" {
			foundFailedCreate = true
			if ev.Count != 2 {
				t.Errorf("expected FailedCreate count 2, got %d", ev.Count)
			}
		}
	}
	if !foundBackOff {
		t.Error("expected BackOff event from fixture")
	}
	if !foundFailedCreate {
		t.Error("expected FailedCreate event from fixture")
	}
}
