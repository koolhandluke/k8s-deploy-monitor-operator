package diagnostic

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// AsyncDiagnosticTarget implements dispatch.Target. It spawns a goroutine per
// event with bounded concurrency to run the full rollout analysis runbook.
type AsyncDiagnosticTarget struct {
	analyzer  *RolloutAnalyzer
	semaphore chan struct{}
	cancel    context.CancelFunc
	ctx       context.Context
	wg        sync.WaitGroup
}

// NewAsyncDiagnosticTarget creates a target that runs at most maxConcurrent
// analyses simultaneously.
func NewAsyncDiagnosticTarget(analyzer *RolloutAnalyzer, maxConcurrent int) *AsyncDiagnosticTarget {
	ctx, cancel := context.WithCancel(context.Background())
	return &AsyncDiagnosticTarget{
		analyzer:  analyzer,
		semaphore: make(chan struct{}, maxConcurrent),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Name returns the target identifier used for logging and dispatch routing.
func (t *AsyncDiagnosticTarget) Name() string {
	return "diagnostic"
}

// Dispatch acquires a semaphore slot and spawns a goroutine to analyze the rollout.
// Returns nil immediately — analysis runs in the background.
// If the semaphore is full, the event is dropped with a warning.
func (t *AsyncDiagnosticTarget) Dispatch(_ context.Context, event models.RolloutEvent) error {
	// Don't accept new work after Stop() has been called.
	select {
	case <-t.ctx.Done():
		slog.Warn("diagnostic analysis rejected: target is shutting down",
			"deployment", event.DeploymentKey(),
		)
		return nil
	default:
	}

	select {
	case t.semaphore <- struct{}{}:
	default:
		slog.Warn("diagnostic analysis dropped: max concurrent analyses reached",
			"deployment", event.DeploymentKey(),
		)
		return nil
	}

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		defer func() { <-t.semaphore }()

		cfg := t.analyzer.config
		ctx, cancel := context.WithTimeout(t.ctx, cfg.AbsoluteTimeout+cfg.SoakPeriod+time.Minute)
		defer cancel()

		slog.Info("starting rollout analysis",
			"deployment", event.DeploymentKey(),
			"cluster", event.ClusterID,
		)

		report, err := t.analyzer.Analyze(ctx, event)
		if err != nil {
			slog.Error("rollout analysis failed",
				"deployment", event.DeploymentKey(),
				"error", err,
			)
			return
		}

		t.logReport(report)
	}()

	return nil
}

// Stop cancels all in-flight analyses and waits for goroutines to finish.
func (t *AsyncDiagnosticTarget) Stop() {
	slog.Info("stopping diagnostic target, draining in-flight analyses")
	t.cancel()
	t.wg.Wait()
	slog.Info("diagnostic target stopped")
}

// logReport emits structured log entries for a completed diagnostic report,
// including detailed events, pod statuses, and log snippets for non-success results.
func (t *AsyncDiagnosticTarget) logReport(report *DiagnosticReport) {
	attrs := []any{
		"deployment", report.Event.DeploymentKey(),
		"cluster", report.Event.ClusterID,
		"result", string(report.Result),
		"duration", report.Duration.Round(time.Second).String(),
	}

	if report.FailureReason != "" {
		attrs = append(attrs, "failure_reason", report.FailureReason)
	}
	if len(report.Events) > 0 {
		attrs = append(attrs, "warning_events", len(report.Events))
	}
	if len(report.PodStatuses) > 0 {
		attrs = append(attrs, "pod_statuses", len(report.PodStatuses))
	}
	if len(report.LogSnippets) > 0 {
		attrs = append(attrs, "log_snippets", len(report.LogSnippets))
	}

	if report.Result == ResultSuccess {
		slog.Info("rollout analysis complete", attrs...)
		return
	}

	slog.Warn("rollout analysis complete", attrs...)

	// Log detailed diagnostics
	for _, ev := range report.Events {
		slog.Info("diagnostic event",
			"deployment", report.Event.DeploymentKey(),
			"reason", ev.Reason,
			"message", ev.Message,
			"count", ev.Count,
			"object", ev.Object,
		)
	}
	for _, ps := range report.PodStatuses {
		if ps.Reason != "" {
			slog.Info("diagnostic pod status",
				"deployment", report.Event.DeploymentKey(),
				"pod", ps.Name,
				"container", ps.ContainerName,
				"reason", ps.Reason,
				"restarts", ps.RestartCount,
				"init_container", ps.InitContainer,
			)
		}
	}
	for _, ls := range report.LogSnippets {
		slog.Info("diagnostic log snippet",
			"deployment", report.Event.DeploymentKey(),
			"pod", ls.PodName,
			"container", ls.ContainerName,
			"previous", ls.Previous,
			"init_container", ls.InitContainer,
			"error_lines", len(ls.Lines),
			"total_lines", ls.TotalLines,
		)
		for _, line := range ls.Lines {
			slog.Info("diagnostic log line",
				"deployment", report.Event.DeploymentKey(),
				"pod", ls.PodName,
				"line", line,
			)
		}
	}
}
