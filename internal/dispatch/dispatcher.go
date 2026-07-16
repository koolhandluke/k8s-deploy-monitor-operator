package dispatch

import (
	"context"
	"log/slog"
	"sync"

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
	eventCh chan models.RolloutEvent
	targets []Target
	workers int
	store   *persistence.Store // nil if persistence disabled
}

func NewDispatcher(cfg *config.Config, eventCh chan models.RolloutEvent, store *persistence.Store) *Dispatcher {
	d := &Dispatcher{
		eventCh: eventCh,
		workers: cfg.WorkerCount,
		store:   store,
	}

	// Always log
	d.targets = append(d.targets, &LogTarget{})

	// Holmes
	if cfg.DispatchMode == config.DispatchHolmes || cfg.DispatchMode == config.DispatchBoth {
		d.targets = append(d.targets, NewHolmesTarget(cfg.HolmesAPIURL))
	}

	// Slack
	if cfg.DispatchMode == config.DispatchSlack || cfg.DispatchMode == config.DispatchBoth {
		d.targets = append(d.targets, NewSlackTarget(cfg.SlackWebhookURL))
	}

	return d
}

// Start launches worker goroutines that consume events until ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context) {
	var wg sync.WaitGroup

	for i := 0; i < d.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			d.worker(ctx, workerID)
		}(i)
	}

	slog.Info("dispatcher started", "workers", d.workers, "targets", d.targetNames())

	go func() {
		wg.Wait()
		slog.Info("all dispatcher workers stopped")
	}()
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

			// Record the rollout event as a CRD
			if d.store != nil {
				if err := d.store.RecordRollout(ctx, event); err != nil {
					slog.Error("failed to record rollout", "error", err)
				}
			}

			// Dispatch to all targets
			var dispatchErr string
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

			// Update record status after dispatch
			if d.store != nil {
				phase := v1alpha1.PhaseDispatched
				if dispatchErr != "" {
					phase = v1alpha1.PhaseFailed
				}
				d.store.UpdateRecordStatus(ctx, event, phase, targetNames, dispatchErr)
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
