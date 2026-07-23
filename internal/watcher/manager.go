// Package watcher detects Kubernetes Deployment rollouts across one or more
// clusters by watching spec.template hash changes via SharedInformerFactory.
package watcher

import (
	"context"
	"fmt"
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

// OnHashUpdate forwards a template hash update to the backing HashStore.
func (a *hashObserverAdapter) OnHashUpdate(clusterID, deployKey, hash string) {
	a.store.BufferHash(clusterID, deployKey, hash)
}

// OnHashDelete forwards a template hash deletion to the backing HashStore.
func (a *hashObserverAdapter) OnHashDelete(clusterID, deployKey string) {
	a.store.RemoveHash(clusterID, deployKey)
}

// retryEntry tracks a cluster pending retry after a watcher failure.
type retryEntry struct {
	cluster   config.ClusterInfo
	attempt   int
	nextRetry time.Time
	lastError string
}

// retryBackoff returns the backoff duration for a given attempt number.
// Sequence: 10s, 20s, 40s, 80s, 160s, capped at 5m.
func retryBackoff(attempt int) time.Duration {
	d := 10 * time.Second
	for i := 0; i < attempt; i++ {
		d *= 2
		if d > 5*time.Minute {
			d = 5 * time.Minute
			break
		}
	}
	return d
}

// ClientsetFactory creates a kubernetes.Interface from a rest.Config.
// Replaceable in tests with a function that returns fake clientsets.
type ClientsetFactory func(*rest.Config) (kubernetes.Interface, error)

// defaultClientsetFactory is the production ClientsetFactory that creates a
// real Kubernetes clientset from a rest.Config.
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
	startTimeout     time.Duration
	clientsetFactory ClientsetFactory

	mu             sync.Mutex
	watchers       map[string]*ClusterWatcher // clusterID → watcher
	fileHashes     map[string]string          // clusterID → file content hash
	pendingRetry   map[string]*retryEntry     // clusterID → retry state
	observer       HashObserver
	eventEnricher  func(*models.RolloutEvent) // enriches events with app/channel; nil-safe
}

// NewManager creates a Manager that watches the given clusters for deployment
// rollouts, debouncing events before sending them to eventCh.
func NewManager(
	nsFilter func(string) bool,
	debounceWindow time.Duration,
	eventCh chan<- models.RolloutEvent,
	store *persistence.HashStore,
	kubeconfigDir string,
	rescanInterval time.Duration,
	startTimeout time.Duration,
) *Manager {
	m := &Manager{
		kubeconfigDir:    kubeconfigDir,
		nsFilter:         nsFilter,
		debouncer:        NewDebouncer(debounceWindow, eventCh),
		store:            store,
		rescanInterval:   rescanInterval,
		startTimeout:     startTimeout,
		clientsetFactory: defaultClientsetFactory,
		watchers:         make(map[string]*ClusterWatcher),
		fileHashes:       make(map[string]string),
		pendingRetry:     make(map[string]*retryEntry),
	}

	if store != nil {
		m.observer = &hashObserverAdapter{store: store}
	}

	return m
}

// SetEventEnricher sets a function that enriches RolloutEvents with app name
// and Slack channel. Must be called before Start.
func (m *Manager) SetEventEnricher(fn func(*models.RolloutEvent)) {
	m.eventEnricher = fn
}

// Start launches a watcher per cluster with staggered startup (1s between clusters).
// Failed clusters are queued for retry. Always starts the reconcile loop for
// health monitoring and retry processing. Returns an error only if zero watchers
// started AND zero clusters are pending retry.
func (m *Manager) Start(ctx context.Context, clusters []config.ClusterInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, cluster := range clusters {
		if err := m.startWatcherLocked(ctx, cluster); err != nil {
			slog.Error("failed to start watcher",
				"cluster", cluster.ID, "error", err)
			backoff := retryBackoff(0)
			m.pendingRetry[cluster.ID] = &retryEntry{
				cluster:   cluster,
				attempt:   0,
				nextRetry: time.Now().Add(backoff),
				lastError: err.Error(),
			}
			slog.Info("watcher_queued_retry",
				"cluster", cluster.ID,
				"attempt", 0,
				"backoff", backoff,
				"next_retry", m.pendingRetry[cluster.ID].nextRetry,
			)
			continue
		}
		slog.Info("watcher_started", "cluster", cluster.ID)

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

	// Return error only if nothing is running and nothing is queued for retry
	if len(m.watchers) == 0 && len(m.pendingRetry) == 0 {
		return fmt.Errorf("no watchers started and no clusters pending retry")
	}

	// Always start reconcile loop for health monitoring and retry processing
	go m.reconcileLoop(ctx)
	slog.Info("reconcile loop started",
		"rescan_interval", m.rescanInterval,
		"dir", m.kubeconfigDir,
		"pending_retries", len(m.pendingRetry),
	)

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
		clientset,
		m.debouncer,
		m.nsFilter,
		m.observer,
		m.startTimeout,
	)
	w.eventEnricher = m.eventEnricher

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
// Uses the minimum of rescanInterval and 10s for retry responsiveness.
func (m *Manager) reconcileLoop(ctx context.Context) {
	interval := m.rescanInterval
	if interval <= 0 {
		interval = 10 * time.Second
	} else if interval > 10*time.Second {
		// Use 10s tick for retry responsiveness; rescan phase checks its own interval
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastRescan time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcile(ctx, &lastRescan)
		}
	}
}

// reconcile runs three phases per tick:
// 1. Rescan directory for file changes (if enough time has passed)
// 2. Health-check running watchers
// 3. Retry pending clusters
func (m *Manager) reconcile(ctx context.Context, lastRescan *time.Time) {
	// Phase 1: Rescan directory for file changes
	if m.kubeconfigDir != "" && m.rescanInterval > 0 && time.Since(*lastRescan) >= m.rescanInterval {
		m.rescanDirectory(ctx)
		*lastRescan = time.Now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Phase 2: Health-check running watchers
	for id, w := range m.watchers {
		healthy, perm, lastErr := w.HealthStatus()
		if !healthy {
			slog.Warn("watcher_unhealthy",
				"cluster", id,
				"permanent", perm,
				"last_error", lastErr,
			)
			w.Stop()
			delete(m.watchers, id)

			// Queue for retry unless already pending
			if _, pending := m.pendingRetry[id]; !pending {
				// Reconstruct ClusterInfo from the watcher
				backoff := retryBackoff(0)
				errStr := ""
				if lastErr != nil {
					errStr = lastErr.Error()
				}
				m.pendingRetry[id] = &retryEntry{
					cluster: config.ClusterInfo{
						ID: w.clusterID,
					},
					attempt:   0,
					nextRetry: time.Now().Add(backoff),
					lastError: errStr,
				}
				slog.Info("watcher_queued_retry",
					"cluster", id,
					"attempt", 0,
					"backoff", backoff,
				)
			}
		}
	}

	// Phase 3: Retry pending clusters
	now := time.Now()
	for id, entry := range m.pendingRetry {
		if now.Before(entry.nextRetry) {
			continue
		}

		// Reload cluster config from directory if available
		cluster := entry.cluster
		if m.kubeconfigDir != "" {
			snap, err := config.LoadDirectorySnapshot(m.kubeconfigDir)
			if err == nil {
				for _, c := range snap.Clusters {
					if c.ID == id {
						cluster = c
						break
					}
				}
			}
		}

		if cluster.RestConfig == nil {
			entry.attempt++
			backoff := retryBackoff(entry.attempt)
			entry.nextRetry = now.Add(backoff)
			entry.lastError = "no RestConfig available (cluster not found in directory)"
			slog.Warn("watcher_retry_skipped",
				"cluster", id,
				"reason", "nil RestConfig",
				"next_backoff", backoff,
			)
			continue
		}

		if err := m.startWatcherLocked(ctx, cluster); err != nil {
			entry.attempt++
			backoff := retryBackoff(entry.attempt)
			entry.nextRetry = now.Add(backoff)
			entry.lastError = err.Error()
			slog.Warn("watcher_retry_failed",
				"cluster", id,
				"attempt", entry.attempt,
				"error", err,
				"next_backoff", backoff,
			)
		} else {
			slog.Info("watcher_recovered",
				"cluster", id,
				"after_attempts", entry.attempt,
			)
			delete(m.pendingRetry, id)
		}
	}

	if len(m.watchers) == 0 {
		slog.Warn("zero_active_watchers",
			"pending_retries", len(m.pendingRetry),
		)
	}
}

// rescanDirectory re-reads the kubeconfig directory and starts/stops/recycles watchers.
func (m *Manager) rescanDirectory(ctx context.Context) {
	snap, err := config.LoadDirectorySnapshot(m.kubeconfigDir)
	if err != nil {
		slog.Error("kubeconfig rescan failed, keeping existing watchers",
			"dir", m.kubeconfigDir, "error", err)
		return
	}

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
			delete(m.pendingRetry, id)
		}
	}

	// Also remove pending retries for clusters no longer in directory
	for id := range m.pendingRetry {
		if _, exists := snap.FileHashes[id]; !exists {
			slog.Info("cluster removed from directory, cancelling retry", "cluster", id)
			delete(m.pendingRetry, id)
		}
	}

	for id, newHash := range snap.FileHashes {
		oldHash, existed := m.fileHashes[id]

		if !existed {
			// New cluster (skip if already pending retry from startup)
			if _, pending := m.pendingRetry[id]; pending {
				// Update the cluster info in the retry entry with fresh config
				m.pendingRetry[id].cluster = newClusters[id]
				m.fileHashes[id] = newHash
				continue
			}
			cluster := newClusters[id]
			slog.Info("new cluster detected, starting watcher", "cluster", id)
			if err := m.startWatcherLocked(ctx, cluster); err != nil {
				slog.Error("failed to start watcher for new cluster",
					"cluster", id, "error", err)
				backoff := retryBackoff(0)
				m.pendingRetry[id] = &retryEntry{
					cluster:   cluster,
					attempt:   0,
					nextRetry: time.Now().Add(backoff),
					lastError: err.Error(),
				}
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
			// Also clear any pending retry for this cluster
			delete(m.pendingRetry, id)

			cluster := newClusters[id]
			if err := m.startWatcherLocked(ctx, cluster); err != nil {
				slog.Error("failed to restart watcher for changed cluster",
					"cluster", id, "error", err)
				delete(m.fileHashes, id)
				backoff := retryBackoff(0)
				m.pendingRetry[id] = &retryEntry{
					cluster:   cluster,
					attempt:   0,
					nextRetry: time.Now().Add(backoff),
					lastError: err.Error(),
				}
				continue
			}
			m.fileHashes[id] = newHash
		}
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
