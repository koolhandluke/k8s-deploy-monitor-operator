package dispatch

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/persistence"
)

// Target is something that can handle a rollout event.
type Target interface {
	Dispatch(ctx context.Context, event models.RolloutEvent) error
	Name() string
}

// Dispatcher consumes rollout events from a channel and dispatches to configured targets.
type Dispatcher struct {
	eventCh  chan models.RolloutEvent
	targets  []Target
	workers  int
	recorder *persistence.AuditRecorder // nil if persistence disabled
	wg       sync.WaitGroup
}

func NewDispatcher(cfg *config.Config, eventCh chan models.RolloutEvent, recorder *persistence.AuditRecorder) *Dispatcher {
	d := &Dispatcher{
		eventCh:  eventCh,
		workers:  cfg.WorkerCount,
		recorder: recorder,
	}

	// Always log
	d.targets = append(d.targets, &LogTarget{})

	// Audit persistence
	if recorder != nil {
		d.targets = append(d.targets, NewAuditTarget(recorder))
	}

	// Holmes
	if cfg.DispatchMode == config.DispatchHolmes || cfg.DispatchMode == config.DispatchBoth {
		d.targets = append(d.targets, NewHolmesTarget(cfg.HolmesAPIURL, &http.Client{Timeout: 5 * time.Minute}))
	}

	// Slack
	if cfg.DispatchMode == config.DispatchSlack || cfg.DispatchMode == config.DispatchBoth {
		d.targets = append(d.targets, NewSlackTarget(cfg.SlackWebhookURL, &http.Client{Timeout: 10 * time.Second}))
	}

	return d
}

// NewStandaloneDispatcher creates a dispatcher without a channel or workers.
// Events are processed synchronously via DispatchEvent(). Used by the dispatcher service.
func NewStandaloneDispatcher(cfg *config.Config) *Dispatcher {
	d := &Dispatcher{
		workers: cfg.WorkerCount,
	}

	// Always log
	d.targets = append(d.targets, &LogTarget{})

	// Holmes
	if cfg.DispatchMode == config.DispatchHolmes || cfg.DispatchMode == config.DispatchBoth {
		d.targets = append(d.targets, NewHolmesTarget(cfg.HolmesAPIURL, &http.Client{Timeout: 5 * time.Minute}))
	}

	// Slack
	if cfg.DispatchMode == config.DispatchSlack || cfg.DispatchMode == config.DispatchBoth {
		d.targets = append(d.targets, NewSlackTarget(cfg.SlackWebhookURL, &http.Client{Timeout: 10 * time.Second}))
	}

	return d
}

// AddTarget registers an additional dispatch target. Must be called before Start.
func (d *Dispatcher) AddTarget(t Target) {
	d.targets = append(d.targets, t)
}

// Start launches worker goroutines that consume events until the event channel is closed.
func (d *Dispatcher) Start(ctx context.Context) {
	for i := 0; i < d.workers; i++ {
		d.wg.Add(1)
		go func(workerID int) {
			defer d.wg.Done()
			d.worker(ctx, workerID)
		}(i)
	}

	slog.Info("dispatcher started", "workers", d.workers, "targets", d.targetNames())
}

// Wait blocks until all dispatcher workers have exited.
func (d *Dispatcher) Wait() {
	d.wg.Wait()
	slog.Info("all dispatcher workers stopped")
}

// DispatchEvent dispatches a single event to all configured targets.
// Returns the list of targets that succeeded and any error string from failed targets.
func (d *Dispatcher) DispatchEvent(ctx context.Context, event models.RolloutEvent) (targets []string, dispatchErr string) {
	targetNames := make([]string, 0, len(d.targets))
	for _, t := range d.targets {
		if err := t.Dispatch(ctx, event); err != nil {
			slog.Error("dispatch failed",
				"target", t.Name(),
				"cluster", event.ClusterName,
				"deployment", event.Namespace+"/"+event.DeploymentName,
				"error", err,
			)
			dispatchErr = err.Error()
		} else {
			targetNames = append(targetNames, t.Name())
		}
	}
	return targetNames, dispatchErr
}

func (d *Dispatcher) worker(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-d.eventCh:
			if !ok {
				return
			}

			targetNames, dispatchErr := d.DispatchEvent(ctx, event)

			// Update record status after dispatch
			if d.recorder != nil {
				phase := v1alpha1.PhaseDispatched
				if dispatchErr != "" {
					phase = v1alpha1.PhaseFailed
				}
				d.recorder.UpdateRecordStatus(ctx, event, phase, targetNames, dispatchErr)
			}
		}
	}
}

func (d *Dispatcher) targetNames() []string {
	names := make([]string, len(d.targets))
	for i, t := range d.targets {
		names[i] = t.Name()
	}
	return names
}
