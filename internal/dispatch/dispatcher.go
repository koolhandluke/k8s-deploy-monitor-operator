package dispatch

import (
	"context"
	"log/slog"
	"sync"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// Target is something that can handle a rollout event.
type Target interface {
	Dispatch(ctx context.Context, event models.RolloutEvent) error
	Name() string
}

// Dispatcher consumes rollout events from a channel and dispatches to configured targets.
type Dispatcher struct {
	eventCh      chan models.RolloutEvent
	targets      []Target
	workers      int
	onDispatched func(context.Context, models.RolloutEvent, []string, string)
	wg           sync.WaitGroup
}

func NewDispatcher(targets []Target, eventCh chan models.RolloutEvent, workerCount int) *Dispatcher {
	return &Dispatcher{
		eventCh: eventCh,
		targets: targets,
		workers: workerCount,
	}
}

// NewStandaloneDispatcher creates a dispatcher without a channel or workers.
// Events are processed synchronously via DispatchEvent(). Used by the dispatcher service.
func NewStandaloneDispatcher(targets []Target) *Dispatcher {
	return &Dispatcher{
		targets: targets,
	}
}

// SetOnDispatched registers a callback invoked after each event is dispatched.
// The callback receives the context, event, list of successful target names, and any error string.
func (d *Dispatcher) SetOnDispatched(fn func(context.Context, models.RolloutEvent, []string, string)) {
	d.onDispatched = fn
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

			if d.onDispatched != nil {
				d.onDispatched(ctx, event, targetNames, dispatchErr)
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
