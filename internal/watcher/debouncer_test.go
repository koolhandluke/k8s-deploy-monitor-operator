package watcher

import (
	"testing"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

func TestDebouncer_SingleEvent(t *testing.T) {
	out := make(chan models.RolloutEvent, 10)
	d := NewDebouncer(50*time.Millisecond, out)
	defer d.Stop()

	d.Submit("cluster/ns/deploy", models.RolloutEvent{
		ClusterID:      "test-cluster",
		Namespace:      "ns",
		DeploymentName: "deploy",
		NewImages:      []string{"app:v2"},
	})

	select {
	case event := <-out:
		if event.DeploymentName != "deploy" {
			t.Errorf("expected deploy, got %s", event.DeploymentName)
		}
		if event.NewImages[0] != "app:v2" {
			t.Errorf("expected app:v2, got %s", event.NewImages[0])
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for debounced event")
	}
}

func TestDebouncer_ReplacesEvent(t *testing.T) {
	out := make(chan models.RolloutEvent, 10)
	d := NewDebouncer(100*time.Millisecond, out)
	defer d.Stop()

	key := "cluster/ns/deploy"

	// Submit v2, then v3 quickly — only v3 should be emitted
	d.Submit(key, models.RolloutEvent{
		DeploymentName: "deploy",
		NewImages:      []string{"app:v2"},
	})
	time.Sleep(20 * time.Millisecond)
	d.Submit(key, models.RolloutEvent{
		DeploymentName: "deploy",
		NewImages:      []string{"app:v3"},
	})

	select {
	case event := <-out:
		if event.NewImages[0] != "app:v3" {
			t.Errorf("expected app:v3 (latest), got %s", event.NewImages[0])
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for debounced event")
	}

	// Should not get a second event
	select {
	case event := <-out:
		t.Fatalf("unexpected second event: %+v", event)
	case <-time.After(200 * time.Millisecond):
		// expected
	}
}

func TestDebouncer_IndependentKeys(t *testing.T) {
	out := make(chan models.RolloutEvent, 10)
	d := NewDebouncer(50*time.Millisecond, out)
	defer d.Stop()

	d.Submit("cluster/ns/a", models.RolloutEvent{DeploymentName: "a"})
	d.Submit("cluster/ns/b", models.RolloutEvent{DeploymentName: "b"})

	received := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case event := <-out:
			received[event.DeploymentName] = true
		case <-time.After(1 * time.Second):
			t.Fatal("timed out waiting for events")
		}
	}

	if !received["a"] || !received["b"] {
		t.Errorf("expected both events, got %v", received)
	}
}

func TestDebouncer_Stop(t *testing.T) {
	out := make(chan models.RolloutEvent, 10)
	d := NewDebouncer(100*time.Millisecond, out)

	d.Submit("cluster/ns/deploy", models.RolloutEvent{DeploymentName: "deploy"})
	d.Stop()

	// After stop, pending event should not be emitted
	select {
	case event := <-out:
		t.Fatalf("unexpected event after stop: %+v", event)
	case <-time.After(200 * time.Millisecond):
		// expected
	}
}
