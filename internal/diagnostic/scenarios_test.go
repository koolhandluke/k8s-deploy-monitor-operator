package diagnostic

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic/testdata"
)

// TestConfigErrorTimerResets verifies that the configErrorFirstSeen timer resets
// when all containers leave the Waiting state. Without this reset, a transient
// Waiting→Running→Waiting sequence could accumulate time across both episodes
// and trigger a false FAILED.
func TestConfigErrorTimerResets(t *testing.T) {
	deploy := yamlToDeploy(t, testdata.DeploymentProgressing)
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)
	waitingPod := yamlToPod(t, testdata.PodConfigError)
	readyPod := yamlToPod(t, testdata.PodReady)

	cfg := DefaultAnalyzerConfig()
	cfg.ConfigErrorWindow = 90 * time.Second
	now := time.Now()
	progress := &progressState{lastProgressAt: now}

	// Step 1: Pod is Waiting — stamps configErrorFirstSeen
	clientset := fake.NewSimpleClientset(deploy, rs, waitingPod)
	analyzer := &RolloutAnalyzer{config: cfg}
	configFirstSeen := time.Time{}

	result, _ := analyzer.checkFailureConditions(
		context.Background(), clientset, deploy, now, progress, &configFirstSeen,
	)
	if result != "" {
		t.Fatalf("expected no result on first check, got %q", result)
	}
	if configFirstSeen.IsZero() {
		t.Fatal("expected configErrorFirstSeen to be stamped after seeing Waiting pod")
	}

	// Step 2: Pod recovers to Ready — should reset the timer
	clientset = fake.NewSimpleClientset(deploy, rs, readyPod)
	result, _ = analyzer.checkFailureConditions(
		context.Background(), clientset, deploy, now.Add(30*time.Second), progress, &configFirstSeen,
	)
	if result != "" {
		t.Fatalf("expected no result after recovery, got %q", result)
	}
	if !configFirstSeen.IsZero() {
		t.Fatal("expected configErrorFirstSeen to be reset after pod recovered")
	}

	// Step 3: Pod goes back to Waiting — timer should restart from zero,
	// so even though 95s total have elapsed (30s + 65s), only 65s count
	// against the window (which is 90s). Should NOT trigger failure.
	clientset = fake.NewSimpleClientset(deploy, rs, waitingPod)
	result, _ = analyzer.checkFailureConditions(
		context.Background(), clientset, deploy, now.Add(95*time.Second), progress, &configFirstSeen,
	)
	if result != "" {
		t.Fatalf("expected no result (timer was reset), got %q", result)
	}
	if configFirstSeen.IsZero() {
		t.Fatal("expected configErrorFirstSeen to be re-stamped")
	}
}

func TestCheckFailureConditions(t *testing.T) {
	rs := yamlToReplicaSet(t, testdata.ReplicasetNew)

	tests := []struct {
		name             string
		deployFixture    string
		pods             []runtime.Object
		configSeenAgo    time.Duration
		expectedResult   Result
		expectedContains string
	}{
		{
			name:             "ProgressDeadlineExceeded",
			deployFixture:    testdata.DeploymentDeadlineExceeded,
			pods:             []runtime.Object{yamlToPod(t, testdata.PodReady)},
			expectedResult:   ResultFailed,
			expectedContains: "ProgressDeadlineExceeded",
		},
		{
			name:             "InvalidImageName triggers generic waiting after window",
			deployFixture:    testdata.DeploymentProgressing,
			pods:             []runtime.Object{yamlToPod(t, testdata.PodInvalidImage)},
			configSeenAgo:    91 * time.Second,
			expectedResult:   ResultFailed,
			expectedContains: "InvalidImageName",
		},
		{
			name:          "CrashLoop restart threshold",
			deployFixture: testdata.DeploymentProgressing,
			pods: func() []runtime.Object {
				p := yamlToPod(t, testdata.PodCrashloop)
				p.Name = "myapp-abc123-crash"
				return []runtime.Object{p}
			}(),
			expectedResult:   ResultFailed,
			expectedContains: "restart threshold",
		},
		{
			name:          "CreateContainerConfigError before window",
			deployFixture: testdata.DeploymentProgressing,
			pods: func() []runtime.Object {
				p := yamlToPod(t, testdata.PodConfigError)
				p.Name = "myapp-abc123-cfg"
				return []runtime.Object{p}
			}(),
			configSeenAgo:  0,
			expectedResult: "",
		},
		{
			name:          "CreateContainerConfigError after window",
			deployFixture: testdata.DeploymentProgressing,
			pods: func() []runtime.Object {
				p := yamlToPod(t, testdata.PodConfigError)
				p.Name = "myapp-abc123-cfg"
				return []runtime.Object{p}
			}(),
			configSeenAgo:    91 * time.Second,
			expectedResult:   ResultFailed,
			expectedContains: "CreateContainerConfigError",
		},
		{
			name:           "OOMKilled under restart threshold - no failure",
			deployFixture:  testdata.DeploymentProgressing,
			pods:           []runtime.Object{yamlToPod(t, testdata.PodOOMKilled)},
			expectedResult: "",
		},
		{
			name:           "Pending pod with no container status - no failure",
			deployFixture:  testdata.DeploymentProgressing,
			pods:           []runtime.Object{yamlToPod(t, testdata.PodPendingNoStatus)},
			expectedResult: "",
		},
		{
			name:           "Healthy - no failure",
			deployFixture:  testdata.DeploymentStable,
			pods:           []runtime.Object{yamlToPod(t, testdata.PodReady)},
			expectedResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deploy := yamlToDeploy(t, tt.deployFixture)
			objs := []runtime.Object{deploy, rs}
			objs = append(objs, tt.pods...)
			clientset := fake.NewSimpleClientset(objs...)

			cfg := DefaultAnalyzerConfig()
			analyzer := &RolloutAnalyzer{config: cfg}

			now := time.Now()
			progress := &progressState{lastProgressAt: now}

			var configFirstSeen time.Time
			if tt.configSeenAgo > 0 {
				configFirstSeen = now.Add(-tt.configSeenAgo)
			}

			result, reason := analyzer.checkFailureConditions(
				context.Background(), clientset, deploy, now, progress, &configFirstSeen,
			)

			if result != tt.expectedResult {
				t.Errorf("expected result %q, got %q (reason: %s)", tt.expectedResult, result, reason)
			}
			if tt.expectedContains != "" && result != "" {
				if !strings.Contains(reason, tt.expectedContains) {
					t.Errorf("expected reason to contain %q, got %q", tt.expectedContains, reason)
				}
			}
		})
	}
}
