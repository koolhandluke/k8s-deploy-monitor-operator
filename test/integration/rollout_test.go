//go:build integration

package integration

import (
	"context"
	"testing"
	"time"
)

// TestHealthyRollout creates a deployment, updates the image to a valid tag,
// and verifies the investigation result is SUCCESS.
func TestHealthyRollout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ns := "integration-healthy-" + uniqueName("t")
	name := "nginx-healthy"

	if err := createNamespace(ctx, workloadClient, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	defer deleteNamespace(ctx, workloadClient, ns)

	// Create initial deployment
	if err := createDeployment(ctx, workloadClient, ns, name, "nginx:1.25"); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Wait for initial deployment to be ready (seeds the operator's template cache)
	if err := waitForDeploymentReady(ctx, workloadClient, ns, name, 60*time.Second); err != nil {
		t.Fatalf("initial deployment not ready: %v", err)
	}

	// Allow time for the informer to seed the cache
	time.Sleep(10 * time.Second)

	// Trigger rollout
	if err := updateImage(ctx, workloadClient, ns, name, "nginx:1.26"); err != nil {
		t.Fatalf("update image: %v", err)
	}

	// Wait for investigation result
	status, err := waitForInvestigation(ctx, statusAPIURL, ns, name, "SUCCESS", 180*time.Second)
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}
	t.Logf("investigation completed: result=%s duration=%s", status.Result, status.Duration)
}

// TestBadImage creates a deployment and sets an image that doesn't exist,
// expecting a FAILED investigation result.
func TestBadImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ns := "integration-badimg-" + uniqueName("t")
	name := "nginx-badimg"

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

	// Set a non-existent image tag
	if err := updateImage(ctx, workloadClient, ns, name, "nginx:doesnotexist"); err != nil {
		t.Fatalf("update image: %v", err)
	}

	status, err := waitForInvestigation(ctx, statusAPIURL, ns, name, "FAILED", 180*time.Second)
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}
	t.Logf("investigation completed: result=%s reason=%s", status.Result, status.FailureReason)
}

// TestCrashLoopBackOff creates a deployment and sets an image that will exit
// immediately (busybox with no command override), causing a crash loop.
func TestCrashLoopBackOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ns := "integration-crash-" + uniqueName("t")
	name := "nginx-crash"

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

	// busybox with no command exits immediately → CrashLoopBackOff
	if err := updateImage(ctx, workloadClient, ns, name, "busybox:1.36"); err != nil {
		t.Fatalf("update image: %v", err)
	}

	status, err := waitForInvestigation(ctx, statusAPIURL, ns, name, "FAILED", 180*time.Second)
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}
	t.Logf("investigation completed: result=%s reason=%s", status.Result, status.FailureReason)
}

// TestMissingConfigMap creates a deployment and patches it to reference a
// ConfigMap that doesn't exist, causing pod creation failures.
func TestMissingConfigMap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ns := "integration-cfgmap-" + uniqueName("t")
	name := "nginx-cfgmap"

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

	// Add envFrom referencing a non-existent ConfigMap
	if err := patchEnvFrom(ctx, workloadClient, ns, name, "does-not-exist"); err != nil {
		t.Fatalf("patch envFrom: %v", err)
	}

	status, err := waitForInvestigation(ctx, statusAPIURL, ns, name, "FAILED", 180*time.Second)
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}
	t.Logf("investigation completed: result=%s reason=%s", status.Result, status.FailureReason)
}

// TestRetryAfterFailure sets a bad image, waits for FAILED, then fixes the
// image and expects a new SUCCESS investigation.
func TestRetryAfterFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	ns := "integration-retry-" + uniqueName("t")
	name := "nginx-retry"

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

	// First: set bad image
	if err := updateImage(ctx, workloadClient, ns, name, "nginx:doesnotexist"); err != nil {
		t.Fatalf("update to bad image: %v", err)
	}

	status, err := waitForInvestigation(ctx, statusAPIURL, ns, name, "FAILED", 180*time.Second)
	if err != nil {
		t.Fatalf("first investigation (expected FAILED): %v", err)
	}
	t.Logf("first investigation: result=%s", status.Result)

	// Second: fix the image
	if err := updateImage(ctx, workloadClient, ns, name, "nginx:1.26"); err != nil {
		t.Fatalf("update to good image: %v", err)
	}

	status, err = waitForInvestigation(ctx, statusAPIURL, ns, name, "SUCCESS", 180*time.Second)
	if err != nil {
		t.Fatalf("second investigation (expected SUCCESS): %v", err)
	}
	t.Logf("second investigation: result=%s", status.Result)
}
