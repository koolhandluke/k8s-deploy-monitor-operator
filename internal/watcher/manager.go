package watcher

import (
	"context"
	"log/slog"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/persistence"
)

// Manager manages cluster watchers across one or more clusters.
type Manager struct {
	clusters  []config.ClusterInfo
	nsFilter  func(string) bool
	debouncer *Debouncer
	watchers  []*ClusterWatcher
	store     *persistence.Store // nil if persistence disabled
}

func NewManager(
	clusters []config.ClusterInfo,
	nsFilter func(string) bool,
	debounceWindow time.Duration,
	eventCh chan<- models.RolloutEvent,
	store *persistence.Store,
) *Manager {
	return &Manager{
		clusters:  clusters,
		nsFilter:  nsFilter,
		debouncer: NewDebouncer(debounceWindow, eventCh),
		store:     store,
	}
}

// Start launches a watcher per cluster with staggered startup (1s between clusters).
func (m *Manager) Start(ctx context.Context) error {
	// Hash persistence callbacks
	var onHashUpdate HashCallback
	var onHashDelete HashDeleteCallback
	if m.store != nil {
		onHashUpdate = func(clusterID, deployKey, hash string) {
			m.store.BufferHash(clusterID, deployKey, hash)
		}
		onHashDelete = func(clusterID, deployKey string) {
			m.store.RemoveHash(clusterID, deployKey)
		}
	}

	for i, cluster := range m.clusters {
		clientset, err := kubernetes.NewForConfig(cluster.RestConfig)
		if err != nil {
			slog.Error("failed to create clientset",
				"cluster", cluster.ID, "error", err)
			continue
		}

		w := NewClusterWatcher(
			cluster.ID,
			cluster.Name,
			clientset,
			m.debouncer,
			m.nsFilter,
			onHashUpdate,
			onHashDelete,
		)

		// Seed persisted hashes before starting (enables gap detection)
		if m.store != nil {
			hashes, err := m.store.LoadHashes(ctx, cluster.ID)
			if err != nil {
				slog.Warn("failed to load persisted hashes, starting fresh",
					"cluster", cluster.ID, "error", err)
			} else if hashes != nil {
				w.SeedHashes(hashes)
			}
		}

		if err := w.Start(ctx); err != nil {
			slog.Error("failed to start watcher",
				"cluster", cluster.ID, "error", err)
			continue
		}

		m.watchers = append(m.watchers, w)

		// Staggered startup — 1s between clusters to avoid hammering the API
		if i < len(m.clusters)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}
	}

	if len(m.watchers) == 0 {
		return nil
	}

	slog.Info("all cluster watchers started", "count", len(m.watchers))
	return nil
}

// Stop shuts down all watchers and the debouncer.
func (m *Manager) Stop() {
	m.debouncer.Stop()
	for _, w := range m.watchers {
		w.Stop()
	}
	slog.Info("all cluster watchers stopped")
}
