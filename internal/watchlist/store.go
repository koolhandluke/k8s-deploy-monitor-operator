// Package watchlist provides a CRD-backed store for dynamic app watch registrations,
// allowing an HTTP API to control which clusters and namespaces are monitored.
package watchlist

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
)

// WatchEntry is the result of a Lookup — the app name and Slack channel for
// a given (clusterID, namespace) pair.
type WatchEntry struct {
	App          string
	SlackChannel string
}

// AppSummary is returned by ListAll for the GET endpoint.
type AppSummary struct {
	App          string              `json:"app"`
	SlackChannel string              `json:"slackChannel"`
	Clusters     map[string][]string `json:"clusters"`
}

// PutResult describes the outcome of a Put operation.
type PutResult struct {
	Created  bool                `json:"created"`
	Accepted map[string][]string `json:"accepted"`
	Rejected map[string]string   `json:"rejected,omitempty"`
}

// ConflictError is returned when a Put would claim a (clusterID, namespace) pair
// already owned by a different app.
type ConflictError struct {
	Conflicts []Conflict
}

// Conflict describes a single ownership collision.
type Conflict struct {
	ClusterID string `json:"clusterID"`
	Namespace string `json:"namespace"`
	OwnerApp  string `json:"ownerApp"`
}

func (e *ConflictError) Error() string {
	var parts []string
	for _, c := range e.Conflicts {
		parts = append(parts, fmt.Sprintf("%s/%s owned by %s", c.ClusterID, c.Namespace, c.OwnerApp))
	}
	return "namespace conflicts: " + strings.Join(parts, ", ")
}

// Store manages AppWatchConfig CRDs and an in-memory index for fast lookups.
type Store struct {
	client    client.Client
	namespace string

	mu            sync.RWMutex
	index         map[string]WatchEntry                  // "clusterID/namespace" → entry
	apps          map[string]*v1alpha1.AppWatchConfigSpec // app name → spec
	knownClusters func() map[string]bool                 // callback: valid cluster IDs
}

// NewStore creates a watchlist Store.
// knownClustersFn returns the set of cluster IDs that have kubeconfigs on disk.
func NewStore(c client.Client, namespace string, knownClustersFn func() map[string]bool) *Store {
	return &Store{
		client:        c,
		namespace:     namespace,
		index:         make(map[string]WatchEntry),
		apps:          make(map[string]*v1alpha1.AppWatchConfigSpec),
		knownClusters: knownClustersFn,
	}
}

// LoadAll lists all AppWatchConfig CRDs and rebuilds the in-memory index.
func (s *Store) LoadAll(ctx context.Context) error {
	list := &v1alpha1.AppWatchConfigList{}
	if err := s.client.List(ctx, list, client.InNamespace(s.namespace)); err != nil {
		return fmt.Errorf("listing AppWatchConfigs: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.index = make(map[string]WatchEntry)
	s.apps = make(map[string]*v1alpha1.AppWatchConfigSpec)

	for i := range list.Items {
		item := &list.Items[i]
		spec := item.Spec.DeepCopy()
		s.apps[item.Name] = spec
		s.addToIndex(item.Name, spec)
	}

	slog.Info("watchlist loaded", "apps", len(s.apps), "index_entries", len(s.index))
	return nil
}

// Lookup returns the WatchEntry for a (clusterID, namespace) pair. O(1).
func (s *Store) Lookup(clusterID, namespace string) (WatchEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.index[clusterID+"/"+namespace]
	return entry, ok
}

// ListAll returns a summary of all registered apps.
func (s *Store) ListAll() []AppSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]AppSummary, 0, len(s.apps))
	for app, spec := range s.apps {
		clusters := make(map[string][]string, len(spec.Clusters))
		for k, v := range spec.Clusters {
			cp := make([]string, len(v))
			copy(cp, v)
			clusters[k] = cp
		}
		out = append(out, AppSummary{
			App:          app,
			SlackChannel: spec.SlackChannel,
			Clusters:     clusters,
		})
	}
	return out
}

// Put creates or updates an app registration. Returns which clusters were
// accepted vs rejected (unknown cluster IDs). Returns ConflictError if any
// (clusterID, namespace) is already owned by a different app.
func (s *Store) Put(ctx context.Context, app string, spec *v1alpha1.AppWatchConfigSpec) (*PutResult, error) {
	known := s.knownClusters()

	// Separate accepted/rejected clusters
	accepted := make(map[string][]string)
	rejected := make(map[string]string)
	for clusterID, namespaces := range spec.Clusters {
		if !known[clusterID] {
			rejected[clusterID] = "unknown cluster"
			continue
		}
		accepted[clusterID] = namespaces
	}

	// Check for conflicts with other apps
	s.mu.RLock()
	var conflicts []Conflict
	for clusterID, namespaces := range accepted {
		for _, ns := range namespaces {
			key := clusterID + "/" + ns
			if existing, ok := s.index[key]; ok && existing.App != app {
				conflicts = append(conflicts, Conflict{
					ClusterID: clusterID,
					Namespace: ns,
					OwnerApp:  existing.App,
				})
			}
		}
	}
	s.mu.RUnlock()

	if len(conflicts) > 0 {
		return nil, &ConflictError{Conflicts: conflicts}
	}

	// Build the spec to persist (only accepted clusters)
	persistSpec := &v1alpha1.AppWatchConfigSpec{
		SlackChannel: spec.SlackChannel,
		Clusters:     accepted,
	}

	// Upsert CRD
	created, err := s.upsertCRD(ctx, app, persistSpec)
	if err != nil {
		return nil, fmt.Errorf("persisting AppWatchConfig: %w", err)
	}

	// Update in-memory state
	s.mu.Lock()
	// Remove old index entries for this app
	s.removeFromIndex(app)
	s.apps[app] = persistSpec.DeepCopy()
	s.addToIndex(app, persistSpec)
	s.mu.Unlock()

	return &PutResult{
		Created:  created,
		Accepted: accepted,
		Rejected: rejected,
	}, nil
}

// Delete removes an app registration.
func (s *Store) Delete(ctx context.Context, app string) error {
	s.mu.RLock()
	_, exists := s.apps[app]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("app %q not found", app)
	}

	// Delete CRD
	obj := &v1alpha1.AppWatchConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sanitizeName(app),
			Namespace: s.namespace,
		},
	}
	if err := s.client.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting AppWatchConfig: %w", err)
	}

	// Update in-memory state
	s.mu.Lock()
	s.removeFromIndex(app)
	delete(s.apps, app)
	s.mu.Unlock()

	return nil
}

// DesiredClusters returns the union of all cluster IDs across all registered apps.
func (s *Store) DesiredClusters() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]bool)
	for _, spec := range s.apps {
		for clusterID := range spec.Clusters {
			result[clusterID] = true
		}
	}
	return result
}

// addToIndex adds entries for all (clusterID, namespace) pairs in the spec.
// Caller must hold s.mu (write lock).
func (s *Store) addToIndex(app string, spec *v1alpha1.AppWatchConfigSpec) {
	for clusterID, namespaces := range spec.Clusters {
		for _, ns := range namespaces {
			s.index[clusterID+"/"+ns] = WatchEntry{
				App:          app,
				SlackChannel: spec.SlackChannel,
			}
		}
	}
}

// removeFromIndex removes all index entries owned by the given app.
// Caller must hold s.mu (write lock).
func (s *Store) removeFromIndex(app string) {
	for key, entry := range s.index {
		if entry.App == app {
			delete(s.index, key)
		}
	}
}

// upsertCRD creates or updates the AppWatchConfig CRD. Returns true if created.
func (s *Store) upsertCRD(ctx context.Context, app string, spec *v1alpha1.AppWatchConfigSpec) (bool, error) {
	name := sanitizeName(app)
	key := types.NamespacedName{Name: name, Namespace: s.namespace}

	existing := &v1alpha1.AppWatchConfig{}
	err := s.client.Get(ctx, key, existing)

	if errors.IsNotFound(err) {
		obj := &v1alpha1.AppWatchConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: s.namespace,
			},
			Spec: *spec.DeepCopy(),
		}
		if err := s.client.Create(ctx, obj); err != nil {
			return false, fmt.Errorf("creating AppWatchConfig: %w", err)
		}
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("getting AppWatchConfig: %w", err)
	}

	existing.Spec = *spec.DeepCopy()
	if err := s.client.Update(ctx, existing); err != nil {
		return false, fmt.Errorf("updating AppWatchConfig: %w", err)
	}
	return false, nil
}

// sanitizeName makes a string safe for k8s resource names.
func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}
