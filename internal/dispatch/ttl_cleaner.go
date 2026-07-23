package dispatch

import (
	"context"
	"log/slog"
	"sync"
	"time"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// TTLCleaner periodically deletes old RolloutRecord CRDs that have been fully processed.
type TTLCleaner struct {
	dynClient dynamic.Interface
	namespace string
	ttl       time.Duration
	stopCh    chan struct{}
	stopOnce  sync.Once
}

// NewTTLCleaner creates a cleaner that deletes records older than ttl.
func NewTTLCleaner(dynClient dynamic.Interface, namespace string, ttlDays int) *TTLCleaner {
	return &TTLCleaner{
		dynClient: dynClient,
		namespace: namespace,
		ttl:       time.Duration(ttlDays) * 24 * time.Hour,
		stopCh:    make(chan struct{}),
	}
}

// Start begins the periodic cleanup loop.
func (tc *TTLCleaner) Start(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	slog.Info("ttl cleaner started", "ttl_days", int(tc.ttl.Hours()/24), "namespace", tc.namespace)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tc.stopCh:
			return
		case <-ticker.C:
			tc.cleanup(ctx)
		}
	}
}

// Stop stops the TTL cleaner. Safe to call multiple times.
func (tc *TTLCleaner) Stop() {
	tc.stopOnce.Do(func() {
		close(tc.stopCh)
	})
}

// cleanup lists terminal RolloutRecords and deletes those older than the configured TTL.
func (tc *TTLCleaner) cleanup(ctx context.Context) {
	list, err := tc.dynClient.Resource(rolloutRecordGVR).Namespace(tc.namespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		slog.Warn("ttl cleaner: failed to list records", "error", err)
		return
	}

	now := time.Now()
	var deleted int

	for i := range list.Items {
		item := &list.Items[i]

		// Only delete terminal records (Dispatched or Failed)
		phase := getNestedString(item, "status", "phase")
		if phase != string(v1alpha1.PhaseDispatched) && phase != string(v1alpha1.PhaseFailed) {
			continue
		}

		// Check age
		creationTime := item.GetCreationTimestamp().Time
		if now.Sub(creationTime) < tc.ttl {
			continue
		}

		// Delete the record
		err := tc.dynClient.Resource(rolloutRecordGVR).Namespace(tc.namespace).
			Delete(ctx, item.GetName(), metav1.DeleteOptions{})
		if err != nil {
			slog.Warn("ttl cleaner: failed to delete record", "name", item.GetName(), "error", err)
		} else {
			deleted++
		}
	}

	if deleted > 0 {
		slog.Info("ttl cleaner: deleted old records", "count", deleted)
	}
}
