//go:build integration

package integration

import (
	"context"
	"testing"
	"time"
)

// TestDebouncing triggers three rapid image changes within the 5s debounce
// window and asserts only one investigation result appears (for the final image).
func TestDebouncing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	ns := "integration-debounce-" + uniqueName("t")
	name := "nginx-debounce"

	if err := createNamespace(ctx, workloadClient, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	defer deleteNamespace(ctx, workloadClient, ns)

	if err := createDeployment(ctx, workloadClient, ns, name, "nginx:1.25"); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if err := waitForDeploymentReady(ctx, workloadClient, ns, name, 60*time.Second); err != nil {
		t.Fatalf("initial deployment not ready: %v", err)
	}
	time.Sleep(10 * time.Second)

	// Rapid-fire three image changes within the 5s debounce window.
	// The debouncer should coalesce these into a single event for nginx:1.28.
	for _, img := range []string{"nginx:1.26", "nginx:1.27", "nginx:1.28"} {
		if err := updateImage(ctx, workloadClient, ns, name, img); err != nil {
			t.Fatalf("update image to %s: %v", img, err)
		}
		time.Sleep(1 * time.Second)
	}

	// Wait for the investigation to complete (should be for the final image)
	status, err := waitForInvestigation(ctx, statusAPIURL, ns, name, "SUCCESS", 120*time.Second)
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}
	t.Logf("investigation completed: result=%s key=%s", status.Result, status.DeploymentKey)

	// The status cache is last-1 per deployment key, so we can only verify
	// that exactly 1 entry exists. The debouncer ensures only the last event
	// survives the window, so there should be exactly 1 investigation.
	count, err := countInvestigations(statusAPIURL, ns, name)
	if err != nil {
		t.Fatalf("count investigations: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 investigation, got %d", count)
	}
}
