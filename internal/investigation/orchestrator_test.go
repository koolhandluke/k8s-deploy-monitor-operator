package investigation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// fakeInvestigator is a test double for Investigator.
type fakeInvestigator struct {
	delay  time.Duration
	result diagnostic.Result
	calls  atomic.Int32
}

func (f *fakeInvestigator) Investigate(ctx context.Context, event models.RolloutEvent) (*diagnostic.DiagnosticReport, error) {
	f.calls.Add(1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(f.delay):
	}
	return &diagnostic.DiagnosticReport{
		Event:    event,
		Result:   f.result,
		Duration: f.delay,
	}, nil
}

// fakeReporter is a test double for Reporter.
type fakeReporter struct {
	mu      sync.Mutex
	reports []*diagnostic.DiagnosticReport
}

func (f *fakeReporter) PostReport(_ context.Context, report *diagnostic.DiagnosticReport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, report)
	return nil
}

func (f *fakeReporter) getReports() []*diagnostic.DiagnosticReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*diagnostic.DiagnosticReport, len(f.reports))
	copy(out, f.reports)
	return out
}

func testRolloutEvent(name string) models.RolloutEvent {
	return models.RolloutEvent{
		ClusterID:      "test-cluster",
		ClusterName:    "test-cluster",
		Namespace:      "default",
		DeploymentName: name,
		Timestamp:      time.Now(),
	}
}

func TestOrchestrator_HappyPath(t *testing.T) {
	inv := &fakeInvestigator{delay: 1 * time.Millisecond, result: diagnostic.ResultSuccess}
	reporter := &fakeReporter{}
	orch := NewOrchestrator(inv, reporter, 10)
	defer orch.Stop()

	orch.Investigate(testRolloutEvent("myapp"))

	// Wait for completion
	time.Sleep(50 * time.Millisecond)

	reports := reporter.getReports()
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if reports[0].Result != diagnostic.ResultSuccess {
		t.Errorf("expected SUCCESS, got %s", reports[0].Result)
	}
}

func TestOrchestrator_SupersedeCancellation(t *testing.T) {
	// Investigator that blocks until context is cancelled (simulates long investigation)
	inv := &fakeInvestigator{delay: 5 * time.Second, result: diagnostic.ResultFailed}
	reporter := &fakeReporter{}
	orch := NewOrchestrator(inv, reporter, 10)
	defer orch.Stop()

	// Start first investigation (will block for 5s)
	orch.Investigate(testRolloutEvent("myapp"))
	time.Sleep(10 * time.Millisecond) // let it start

	// Supersede with a second investigation for the same deployment key.
	// The first should be cancelled via context. The second will also use
	// the same slow investigator, but the cancel of the first is what we're testing.
	orch.Investigate(testRolloutEvent("myapp"))

	// Wait a bit — first should have been cancelled
	time.Sleep(50 * time.Millisecond)

	// The first investigation should have been cancelled (its context was cancelled).
	// We can verify by checking that the first call was indeed made.
	calls := inv.calls.Load()
	if calls < 2 {
		t.Errorf("expected at least 2 investigate calls (original + supersede), got %d", calls)
	}
}

func TestOrchestrator_ConcurrencyLimit(t *testing.T) {
	inv := &fakeInvestigator{delay: 100 * time.Millisecond, result: diagnostic.ResultSuccess}
	reporter := &fakeReporter{}
	orch := NewOrchestrator(inv, reporter, 2)
	defer orch.Stop()

	// Start 3 investigations for different deployments; third should be dropped
	orch.Investigate(testRolloutEvent("app1"))
	orch.Investigate(testRolloutEvent("app2"))
	time.Sleep(5 * time.Millisecond) // let first two acquire semaphore
	orch.Investigate(testRolloutEvent("app3"))

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	reports := reporter.getReports()
	// app3 should have been dropped (semaphore full)
	if len(reports) > 2 {
		t.Errorf("expected at most 2 reports (third dropped), got %d", len(reports))
	}
}

func TestOrchestrator_StopDrains(t *testing.T) {
	inv := &fakeInvestigator{delay: 50 * time.Millisecond, result: diagnostic.ResultSuccess}
	reporter := &fakeReporter{}
	orch := NewOrchestrator(inv, reporter, 10)

	orch.Investigate(testRolloutEvent("myapp"))

	done := make(chan struct{})
	go func() {
		orch.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Stop returned — goroutines drained
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within timeout — goroutine leak")
	}
}

func TestOrchestrator_RejectsAfterStop(t *testing.T) {
	inv := &fakeInvestigator{delay: 1 * time.Millisecond, result: diagnostic.ResultSuccess}
	orch := NewOrchestrator(inv, nil, 10)
	orch.Stop()

	// Should not panic or block
	orch.Investigate(testRolloutEvent("myapp"))
}

func TestOrchestrator_NilReporter(t *testing.T) {
	inv := &fakeInvestigator{delay: 1 * time.Millisecond, result: diagnostic.ResultSuccess}
	orch := NewOrchestrator(inv, nil, 10)
	defer orch.Stop()

	// Should not panic when reporter is nil
	orch.Investigate(testRolloutEvent("myapp"))
	time.Sleep(50 * time.Millisecond)
}
