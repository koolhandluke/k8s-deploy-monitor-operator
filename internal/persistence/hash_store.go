// Package persistence provides batched CRD-based storage for deployment template
// hashes and rollout audit records, using controller-runtime clients.
package persistence

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
)

// HashStore handles batched persistence of deployment template hashes to CRDs.
type HashStore struct {
	client    client.Client
	namespace string

	pendingHashes map[string]map[string]string // clusterID -> {ns/deploy -> hash}
	mu            sync.Mutex
}

// NewHashStore creates a HashStore that persists template hashes to the given namespace.
func NewHashStore(c client.Client, namespace string) *HashStore {
	return &HashStore{
		client:        c,
		namespace:     namespace,
		pendingHashes: make(map[string]map[string]string),
	}
}

// LoadHashes loads persisted template hashes for a cluster.
// Returns nil map if no state exists (first run).
func (s *HashStore) LoadHashes(ctx context.Context, clusterID string) (map[string]string, error) {
	state := &v1alpha1.ClusterRolloutState{}
	key := types.NamespacedName{
		Name:      sanitizeName(clusterID),
		Namespace: s.namespace,
	}

	if err := s.client.Get(ctx, key, state); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil // first run for this cluster
		}
		return nil, fmt.Errorf("loading cluster state for %s: %w", clusterID, err)
	}

	slog.Info("loaded persisted hashes",
		"cluster", clusterID,
		"deployments", len(state.Spec.TemplateHashes),
	)
	return state.Spec.TemplateHashes, nil
}

// BufferHash queues a hash update for batched persistence.
func (s *HashStore) BufferHash(clusterID, deployKey, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pendingHashes[clusterID] == nil {
		s.pendingHashes[clusterID] = make(map[string]string)
	}
	s.pendingHashes[clusterID][deployKey] = hash
}

// RemoveHash queues a hash removal (deployment deleted).
func (s *HashStore) RemoveHash(clusterID, deployKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pendingHashes[clusterID] == nil {
		s.pendingHashes[clusterID] = make(map[string]string)
	}
	// Empty string signals deletion
	s.pendingHashes[clusterID][deployKey] = ""
}

// FlushLoop periodically writes buffered hashes to CRDs.
// Runs until ctx is cancelled.
func (s *HashStore) FlushLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final flush on shutdown
			s.flush(context.Background())
			return
		case <-ticker.C:
			s.flush(ctx)
		}
	}
}

// flush drains all pending hash updates and writes them to their respective
// ClusterRolloutState CRDs. Failed updates are re-queued for the next cycle.
func (s *HashStore) flush(ctx context.Context) {
	s.mu.Lock()
	pending := s.pendingHashes
	s.pendingHashes = make(map[string]map[string]string)
	s.mu.Unlock()

	for clusterID, hashes := range pending {
		if err := s.upsertClusterState(ctx, clusterID, hashes); err != nil {
			slog.Error("failed to flush cluster state",
				"cluster", clusterID, "error", err)
			// Re-queue failed updates
			s.mu.Lock()
			for k, v := range hashes {
				if s.pendingHashes[clusterID] == nil {
					s.pendingHashes[clusterID] = make(map[string]string)
				}
				// Don't overwrite newer pending values
				if _, exists := s.pendingHashes[clusterID][k]; !exists {
					s.pendingHashes[clusterID][k] = v
				}
			}
			s.mu.Unlock()
		}
	}
}

// upsertClusterState creates or updates the ClusterRolloutState CRD for the
// given cluster, applying the provided hash updates and refreshing the status.
func (s *HashStore) upsertClusterState(ctx context.Context, clusterID string, updates map[string]string) error {
	name := sanitizeName(clusterID)
	key := types.NamespacedName{Name: name, Namespace: s.namespace}

	state := &v1alpha1.ClusterRolloutState{}
	err := s.client.Get(ctx, key, state)

	if errors.IsNotFound(err) {
		// Create new
		state = &v1alpha1.ClusterRolloutState{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: s.namespace,
			},
			Spec: v1alpha1.ClusterRolloutStateSpec{
				ClusterID:      clusterID,
				TemplateHashes: make(map[string]string),
			},
		}
		applyUpdates(state, updates)

		if err := s.client.Create(ctx, state); err != nil {
			return fmt.Errorf("creating cluster state: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting cluster state: %w", err)
	} else {
		// Update existing
		if state.Spec.TemplateHashes == nil {
			state.Spec.TemplateHashes = make(map[string]string)
		}
		applyUpdates(state, updates)

		if err := s.client.Update(ctx, state); err != nil {
			return fmt.Errorf("updating cluster state: %w", err)
		}
	}

	// Update status
	now := metav1.Now()
	state.Status.TrackedDeployments = len(state.Spec.TemplateHashes)
	state.Status.LastSyncTime = &now
	if err := s.client.Status().Update(ctx, state); err != nil {
		slog.Warn("failed to update cluster state status", "cluster", clusterID, "error", err)
	}

	slog.Debug("flushed cluster state",
		"cluster", clusterID,
		"deployments", len(state.Spec.TemplateHashes),
	)
	return nil
}

// applyUpdates merges hash updates into a ClusterRolloutState. An empty value
// signals deletion of the corresponding key.
func applyUpdates(state *v1alpha1.ClusterRolloutState, updates map[string]string) {
	for k, v := range updates {
		if v == "" {
			delete(state.Spec.TemplateHashes, k) // deletion
		} else {
			state.Spec.TemplateHashes[k] = v
		}
	}
}
