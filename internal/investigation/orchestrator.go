package investigation

import (
	"context"
	"log/slog"
	"sync"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// Reporter posts investigation results to an external system (e.g. Slack).
type Reporter interface {
	PostReport(ctx context.Context, report *diagnostic.DiagnosticReport) error
}

// Orchestrator manages concurrent investigations with supersede semantics:
// a new rollout for the same deployment cancels any in-flight investigation.
type Orchestrator struct {
	investigator Investigator
	reporter     Reporter

	mu     sync.Mutex
	active map[string]context.CancelFunc // deploymentKey → cancel

	semaphore chan struct{}
	wg        sync.WaitGroup

	ctx    context.Context
	cancel context.CancelFunc
}

// NewOrchestrator creates an orchestrator with bounded concurrency.
func NewOrchestrator(investigator Investigator, reporter Reporter, maxConcurrent int) *Orchestrator {
	ctx, cancel := context.WithCancel(context.Background())
	return &Orchestrator{
		investigator: investigator,
		reporter:     reporter,
		active:       make(map[string]context.CancelFunc),
		semaphore:    make(chan struct{}, maxConcurrent),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Investigate starts an async investigation for the event.
// Supersedes any in-flight investigation for the same deployment.
func (o *Orchestrator) Investigate(event models.RolloutEvent) {
	key := event.DeploymentKey()

	// Supersede: cancel any in-flight investigation for this deployment
	o.mu.Lock()
	if cancelFn, ok := o.active[key]; ok {
		slog.Info("superseding in-flight investigation", "deployment", key)
		cancelFn()
		delete(o.active, key)
	}
	o.mu.Unlock()

	// Check if orchestrator is shutting down
	select {
	case <-o.ctx.Done():
		slog.Warn("investigation rejected: orchestrator is shutting down", "deployment", key)
		return
	default:
	}

	// Non-blocking semaphore acquire
	select {
	case o.semaphore <- struct{}{}:
	default:
		slog.Warn("investigation dropped: max concurrent investigations reached", "deployment", key)
		return
	}

	// Create investigation context
	invCtx, invCancel := context.WithCancel(o.ctx)

	o.mu.Lock()
	o.active[key] = invCancel
	o.mu.Unlock()

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer func() { <-o.semaphore }()
		defer func() {
			o.mu.Lock()
			// Only delete if we're still the active investigation
			if cancel, ok := o.active[key]; ok && cancel == nil {
				delete(o.active, key)
			}
			// Clean up our entry if it hasn't been superseded
			if _, ok := o.active[key]; ok {
				delete(o.active, key)
			}
			o.mu.Unlock()
			invCancel()
		}()

		slog.Info("starting investigation", "deployment", key)

		report, err := o.investigator.Investigate(invCtx, event)
		if err != nil {
			if invCtx.Err() != nil {
				slog.Info("investigation cancelled (superseded or shutdown)", "deployment", key)
				return
			}
			slog.Error("investigation failed", "deployment", key, "error", err)
			return
		}

		slog.Info("investigation complete",
			"deployment", key,
			"result", string(report.Result),
			"duration", report.Duration.String(),
		)

		if o.reporter != nil {
			if err := o.reporter.PostReport(invCtx, report); err != nil {
				if invCtx.Err() != nil {
					return
				}
				slog.Error("failed to post investigation report", "deployment", key, "error", err)
			}
		}
	}()
}

// Stop cancels all in-flight investigations and waits for goroutines to drain.
func (o *Orchestrator) Stop() {
	slog.Info("stopping investigation orchestrator, draining in-flight investigations")
	o.cancel()
	o.wg.Wait()
	slog.Info("investigation orchestrator stopped")
}
