package diagnostic

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// eventList is a decode target for the v1/List YAML containing Event items.
type eventList struct {
	Items []corev1.Event `json:"items"`
}

func yamlToDeploy(t *testing.T, y string) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	if err := yaml.Unmarshal([]byte(y), &d); err != nil {
		t.Fatalf("unmarshal deployment: %v", err)
	}
	return &d
}

func yamlToPod(t *testing.T, y string) *corev1.Pod {
	t.Helper()
	var p corev1.Pod
	if err := yaml.Unmarshal([]byte(y), &p); err != nil {
		t.Fatalf("unmarshal pod: %v", err)
	}
	return &p
}

func yamlToReplicaSet(t *testing.T, y string) *appsv1.ReplicaSet {
	t.Helper()
	var rs appsv1.ReplicaSet
	if err := yaml.Unmarshal([]byte(y), &rs); err != nil {
		t.Fatalf("unmarshal replicaset: %v", err)
	}
	return &rs
}

func yamlToEvents(t *testing.T, y string) []corev1.Event {
	t.Helper()
	var el eventList
	if err := yaml.Unmarshal([]byte(y), &el); err != nil {
		t.Fatalf("unmarshal event list: %v", err)
	}
	return el.Items
}
