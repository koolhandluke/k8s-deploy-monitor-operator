package diagnostic

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func progressingDeployment() *appsv1.Deployment {
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
			UpdatedReplicas:     1,
			AvailableReplicas:   1,
			UnavailableReplicas: 2,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentProgressing,
					Status: corev1.ConditionTrue,
					Reason: "ReplicaSetUpdated",
				},
			},
		},
	}
}

func deadlineExceededDeployment() *appsv1.Deployment {
	d := progressingDeployment()
	d.Status.Conditions = []appsv1.DeploymentCondition{
		{
			Type:   appsv1.DeploymentProgressing,
			Status: corev1.ConditionFalse,
			Reason: "ProgressDeadlineExceeded",
		},
	}
	return d
}

func waitingPod(name string, rsUID types.UID, reason string, restarts int32) *corev1.Pod {
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
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "myapp",
					Ready:        false,
					RestartCount: restarts,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: reason},
					},
				},
			},
		},
	}
}

func TestCheckFailureConditions(t *testing.T) {
	rs := newReplicaSet(types.UID("deploy-uid"))

	tests := []struct {
		name             string
		deploy           *appsv1.Deployment
		pods             []runtime.Object
		configSeenAgo    time.Duration
		expectedResult   Result
		expectedContains string
	}{
		{
			name:             "ProgressDeadlineExceeded",
			deploy:           deadlineExceededDeployment(),
			pods:             []runtime.Object{readyPod("myapp-abc123-p1", rs.UID)},
			expectedResult:   ResultFailed,
			expectedContains: "ProgressDeadlineExceeded",
		},
		{
			name:             "InvalidImageName triggers generic waiting after window",
			deploy:           progressingDeployment(),
			pods:             []runtime.Object{waitingPod("myapp-abc123-inv", rs.UID, "InvalidImageName", 0)},
			configSeenAgo:    91 * time.Second,
			expectedResult:   ResultFailed,
			expectedContains: "InvalidImageName",
		},
		{
			name:             "CrashLoop restart threshold",
			deploy:           progressingDeployment(),
			pods:             []runtime.Object{waitingPod("myapp-abc123-crash", rs.UID, "CrashLoopBackOff", 4)},
			expectedResult:   ResultFailed,
			expectedContains: "restart threshold",
		},
		{
			name:           "CreateContainerConfigError before window",
			deploy:         progressingDeployment(),
			pods:           []runtime.Object{waitingPod("myapp-abc123-cfg", rs.UID, "CreateContainerConfigError", 0)},
			configSeenAgo:  0,
			expectedResult: "",
		},
		{
			name:             "CreateContainerConfigError after window",
			deploy:           progressingDeployment(),
			pods:             []runtime.Object{waitingPod("myapp-abc123-cfg", rs.UID, "CreateContainerConfigError", 0)},
			configSeenAgo:    91 * time.Second,
			expectedResult:   ResultFailed,
			expectedContains: "CreateContainerConfigError",
		},
		{
			name:           "Healthy - no failure",
			deploy:         stableDeployment(),
			pods:           []runtime.Object{readyPod("myapp-abc123-p1", rs.UID)},
			expectedResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []runtime.Object{tt.deploy, rs}
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
				context.Background(), clientset, tt.deploy, now, progress, &configFirstSeen,
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
