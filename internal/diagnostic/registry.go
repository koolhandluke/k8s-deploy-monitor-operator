// Package diagnostic implements rollout analysis and diagnostics for Kubernetes deployments.
// It monitors rollout progress, detects failures, and gathers diagnostic context
// such as pod statuses, warning events, and container logs.
package diagnostic

import (
	"fmt"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
)

// ClusterRegistry maps cluster IDs to their rest.Config for building per-cluster clients.
type ClusterRegistry struct {
	mu      sync.RWMutex
	configs map[string]*rest.Config
}

// NewClusterRegistry builds a registry from a slice of ClusterInfo.
func NewClusterRegistry(clusters []config.ClusterInfo) *ClusterRegistry {
	configs := make(map[string]*rest.Config, len(clusters))
	for _, c := range clusters {
		configs[c.ID] = rest.CopyConfig(c.RestConfig)
	}
	return &ClusterRegistry{configs: configs}
}

// ClientsetFor builds a kubernetes.Interface for the given cluster.
func (r *ClusterRegistry) ClientsetFor(clusterID string) (kubernetes.Interface, error) {
	r.mu.RLock()
	cfg, ok := r.configs[clusterID]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown cluster: %s", clusterID)
	}
	return kubernetes.NewForConfig(cfg)
}

// Update replaces the registry contents with new cluster configs.
func (r *ClusterRegistry) Update(clusters []config.ClusterInfo) {
	configs := make(map[string]*rest.Config, len(clusters))
	for _, c := range clusters {
		configs[c.ID] = rest.CopyConfig(c.RestConfig)
	}
	r.mu.Lock()
	r.configs = configs
	r.mu.Unlock()
}
