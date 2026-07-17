package watcher

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/persistence"
)

// hashObserverAdapter adapts a HashStore to the HashObserver interface.
type hashObserverAdapter struct {
	store *persistence.HashStore
}

func (a *hashObserverAdapter) OnHashUpdate(clusterID, deployKey, hash string) {
	a.store.BufferHash(clusterID, deployKey, hash)
}

func (a *hashObserverAdapter) OnHashDelete(clusterID, deployKey string) {
	a.store.RemoveHash(clusterID, deployKey)
}

// ClientsetFactory creates a kubernetes.Interface from a rest.Config.
// Replaceable in tests with a function that returns fake clientsets.
type ClientsetFactory func(*rest.Config) (kubernetes.Interface, error)

func defaultClientsetFactory(cfg *rest.Config) (kubernetes.Interface, error) {
	return kubernetes.NewForConfig(cfg)
}

// Manager manages cluster watchers across one or more clusters.
type Manager struct {
	kubeconfigDir    string
	nsFilter         func(string) bool
	debouncer        *Debouncer
	store            *persistence.HashStore // nil if persistence disabled
	rescanInterval   time.Duration
	clientsetFactory ClientsetFactory

	mu         sync.Mutex
	watchers   map[string]*ClusterWatcher // clusterID → watcher
	fileHashes map[string]string          // clusterID → file content hash
	observer   HashObserver
}

func NewManager(
	nsFilter func(string) bool,
	debounceWindow time.Duration,
	eventCh chan<- models.RolloutEvent,
	store *persistence.HashStore,
	kubeconfigDir string,
	rescanInterval time.Duration,
) *Manager {
	m := &Manager{
		kubeconfigDir:    kubeconfigDir,
		nsFilter:         nsFilter,
		debouncer:        NewDebouncer(debounceWindow, eventCh),
		store:            store,
		rescanInterval:   rescanInterval,
		clientsetFactory: defaultClientsetFactory,
		watchers:         make(map[string]*ClusterWatcher),
		fileHashes:       make(map[string]string),
	}

	if store != nil {
		m.observer = &hashObserverAdapter{store: store}
	}

	return m
}

// Start launches a watcher per cluster with staggered startup (1s between clusters).
// If kubeconfigDir is set and rescanInterval > 0, starts a reconcile loop.
func (m *Manager) Start(ctx context.Context, clusters []config.ClusterInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, cluster := range clusters {
		if err := m.startWatcherLocked(ctx, cluster); err != nil {
			slog.Error("failed to start watcher",
				"cluster", cluster.ID, "error", err)
			continue
		}

		// Staggered startup — 1s between clusters to avoid hammering the API
		if i < len(clusters)-1 {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				m.mu.Lock()
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
			m.mu.Lock()
		}
	}

	// Load initial file hashes if using directory mode
	if m.kubeconfigDir != "" {
		snap, err := config.LoadDirectorySnapshot(m.kubeconfigDir)
		if err != nil {
			slog.Warn("failed to load initial directory snapshot", "error", err)
		} else {
			m.fileHashes = snap.FileHashes
		}
	}

	if len(m.watchers) > 0 {
		slog.Info("all cluster watchers started", "count", len(m.watchers))
	}

	// Start reconcile loop if directory mode with rescan enabled
	if m.kubeconfigDir != "" && m.rescanInterval > 0 {
		go m.reconcileLoop(ctx)
		slog.Info("kubeconfig rescan enabled",
			"interval", m.rescanInterval,
			"dir", m.kubeconfigDir)
	}

	return nil
}

// startWatcherLocked creates and starts a single ClusterWatcher.
// Caller must hold m.mu.
func (m *Manager) startWatcherLocked(ctx context.Context, cluster config.ClusterInfo) error {
	clientset, err := m.clientsetFactory(cluster.RestConfig)
	if err != nil {
		return err
	}

	w := NewClusterWatcher(
		cluster.ID,
		cluster.Name,
		clientset,
		m.debouncer,
		m.nsFilter,
		m.observer,
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
		return err
	}

	m.watchers[cluster.ID] = w
	return nil
}

// reconcileLoop runs on a ticker and calls reconcile on each tick.
func (m *Manager) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(m.rescanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

// reconcile re-reads the kubeconfig directory and starts/stops/recycles watchers.
func (m *Manager) reconcile(ctx context.Context) {
	snap, err := config.LoadDirectorySnapshot(m.kubeconfigDir)
	if err != nil {
		slog.Error("kubeconfig rescan failed, keeping existing watchers",
			"dir", m.kubeconfigDir, "error", err)
		return
	}

	// Build lookup of new snapshot
	newClusters := make(map[string]config.ClusterInfo, len(snap.Clusters))
	for _, c := range snap.Clusters {
		newClusters[c.ID] = c
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Removed clusters: in watchers but not in snapshot
	for id, w := range m.watchers {
		if _, exists := snap.FileHashes[id]; !exists {
			slog.Info("cluster removed, stopping watcher", "cluster", id)
			w.Stop()
			delete(m.watchers, id)
			delete(m.fileHashes, id)
		}
	}

	for id, newHash := range snap.FileHashes {
		oldHash, existed := m.fileHashes[id]

		if !existed {
			// New cluster
			cluster := newClusters[id]
			slog.Info("new cluster detected, starting watcher", "cluster", id)
			if err := m.startWatcherLocked(ctx, cluster); err != nil {
				slog.Error("failed to start watcher for new cluster",
					"cluster", id, "error", err)
				continue
			}
			m.fileHashes[id] = newHash
		} else if newHash != oldHash {
			// Changed cluster — recycle watcher
			slog.Info("cluster config changed, recycling watcher", "cluster", id)
			if w, ok := m.watchers[id]; ok {
				w.Stop()
				delete(m.watchers, id)
			}
			cluster := newClusters[id]
			if err := m.startWatcherLocked(ctx, cluster); err != nil {
				slog.Error("failed to restart watcher for changed cluster",
					"cluster", id, "error", err)
				delete(m.fileHashes, id)
				continue
			}
			m.fileHashes[id] = newHash
		}
		// Unchanged: skip
	}

	if len(m.watchers) == 0 {
		slog.Warn("no active cluster watchers after rescan")
	}
}

// Stop shuts down all watchers and the debouncer.
func (m *Manager) Stop() {
	m.debouncer.Stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, w := range m.watchers {
		w.Stop()
		delete(m.watchers, id)
	}
	slog.Info("all cluster watchers stopped")
}
