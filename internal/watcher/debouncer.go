package watcher

import (
	"log/slog"
	"sync"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// Debouncer coalesces rapid rollout events for the same deployment.
// On event arrival, starts a timer. Subsequent events for the same key
// reset the timer and replace the pending event. On timer expiry,
// the latest event is emitted to the output channel.
type Debouncer struct {
	window  time.Duration
	timers  map[string]*time.Timer
	pending map[string]models.RolloutEvent
	mu      sync.Mutex
	out     chan<- models.RolloutEvent
}

// NewDebouncer creates a Debouncer that waits for the given window of inactivity
// before forwarding events to the out channel.
func NewDebouncer(window time.Duration, out chan<- models.RolloutEvent) *Debouncer {
	return &Debouncer{
		window:  window,
		timers:  make(map[string]*time.Timer),
		pending: make(map[string]models.RolloutEvent),
		out:     out,
	}
}

// Submit adds or replaces a pending event for the given deployment key.
func (d *Debouncer) Submit(key string, event models.RolloutEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.pending[key] = event

	if t, exists := d.timers[key]; exists {
		t.Reset(d.window)
		return
	}

	d.timers[key] = time.AfterFunc(d.window, func() {
		d.emit(key)
	})
}

// emit sends the pending event for key to the output channel using a non-blocking
// send, dropping the event with a warning if the channel is full.
func (d *Debouncer) emit(key string) {
	d.mu.Lock()
	event, exists := d.pending[key]
	delete(d.pending, key)
	delete(d.timers, key)
	d.mu.Unlock()

	if !exists {
		return
	}

	select {
	case d.out <- event:
		slog.Info("debounced event emitted",
			"cluster", event.ClusterName,
			"deployment", event.Namespace+"/"+event.DeploymentName,
		)
	default:
		slog.Warn("dispatch queue full, dropping event",
			"cluster", event.ClusterName,
			"deployment", event.Namespace+"/"+event.DeploymentName,
		)
	}
}

// Stop cancels all pending timers.
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for key, t := range d.timers {
		t.Stop()
		delete(d.timers, key)
		delete(d.pending, key)
	}
}
