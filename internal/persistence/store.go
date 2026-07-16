package persistence

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// Store handles reading and writing CRDs for persistence.
type Store struct {
	client    client.Client
	namespace string

	// Batched hash updates: buffer changes and flush periodically
	pendingHashes map[string]map[string]string // clusterID -> {ns/deploy -> hash}
	mu            sync.Mutex
}

func NewStore(c client.Client, namespace string) *Store {
	return &Store{
		client:        c,
		namespace:     namespace,
		pendingHashes: make(map[string]map[string]string),
	}
}

// LoadHashes loads persisted template hashes for a cluster.
// Returns nil map if no state exists (first run).
func (s *Store) LoadHashes(ctx context.Context, clusterID string) (map[string]string, error) {
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
func (s *Store) BufferHash(clusterID, deployKey, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pendingHashes[clusterID] == nil {
		s.pendingHashes[clusterID] = make(map[string]string)
	}
	s.pendingHashes[clusterID][deployKey] = hash
}

// RemoveHash queues a hash removal (deployment deleted).
func (s *Store) RemoveHash(clusterID, deployKey string) {
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
func (s *Store) FlushLoop(ctx context.Context, interval time.Duration) {
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

func (s *Store) flush(ctx context.Context) {
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

func (s *Store) upsertClusterState(ctx context.Context, clusterID string, updates map[string]string) error {
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

func applyUpdates(state *v1alpha1.ClusterRolloutState, updates map[string]string) {
	for k, v := range updates {
		if v == "" {
			delete(state.Spec.TemplateHashes, k) // deletion
		} else {
			state.Spec.TemplateHashes[k] = v
		}
	}
}

// RecordRollout creates a RolloutRecord CR for the event.
func (s *Store) RecordRollout(ctx context.Context, event models.RolloutEvent) error {
	name := fmt.Sprintf("%s-%s-%s-%d",
		sanitizeName(event.ClusterID),
		sanitizeName(event.Namespace),
		sanitizeName(event.DeploymentName),
		event.Timestamp.Unix(),
	)
	// K8s name max is 253 chars
	if len(name) > 253 {
		name = name[:253]
	}

	record := &v1alpha1.RolloutRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.namespace,
			Labels: map[string]string{
				"deploy-monitor.io/cluster":    sanitizeName(event.ClusterID),
				"deploy-monitor.io/namespace":  event.Namespace,
				"deploy-monitor.io/deployment": sanitizeName(event.DeploymentName),
			},
		},
		Spec: v1alpha1.RolloutRecordSpec{
			ClusterID:       event.ClusterID,
			ClusterName:     event.ClusterName,
			Namespace:       event.Namespace,
			Deployment:      event.DeploymentName,
			OldImages:       event.OldImages,
			NewImages:       event.NewImages,
			OldTemplateHash: event.OldTemplateHash,
			NewTemplateHash: event.NewTemplateHash,
			DetectedAt:      metav1.NewTime(event.Timestamp),
		},
		Status: v1alpha1.RolloutRecordStatus{
			Phase: v1alpha1.PhaseDetected,
		},
	}

	if err := s.client.Create(ctx, record); err != nil {
		return fmt.Errorf("creating rollout record: %w", err)
	}

	slog.Info("rollout record created",
		"name", name,
		"cluster", event.ClusterName,
		"deployment", event.Namespace+"/"+event.DeploymentName,
	)
	return nil
}

// UpdateRecordStatus updates the status of a RolloutRecord after dispatch.
func (s *Store) UpdateRecordStatus(ctx context.Context, event models.RolloutEvent, phase v1alpha1.RolloutPhase, targets []string, dispatchErr string) {
	name := fmt.Sprintf("%s-%s-%s-%d",
		sanitizeName(event.ClusterID),
		sanitizeName(event.Namespace),
		sanitizeName(event.DeploymentName),
		event.Timestamp.Unix(),
	)
	if len(name) > 253 {
		name = name[:253]
	}

	record := &v1alpha1.RolloutRecord{}
	key := types.NamespacedName{Name: name, Namespace: s.namespace}

	if err := s.client.Get(ctx, key, record); err != nil {
		slog.Warn("failed to get rollout record for status update",
			"name", name, "error", err)
		return
	}

	now := metav1.Now()
	record.Status.Phase = phase
	record.Status.DispatchedAt = &now
	record.Status.DispatchTargets = targets
	record.Status.Error = dispatchErr

	if err := s.client.Status().Update(ctx, record); err != nil {
		slog.Warn("failed to update rollout record status",
			"name", name, "error", err)
	}
}

// sanitizeName makes a string safe for k8s resource names.
func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	// Trim leading/trailing hyphens
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}
