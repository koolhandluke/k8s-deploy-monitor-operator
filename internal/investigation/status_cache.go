package investigation

import (
	"sync"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
)

// InvestigationStatus captures the latest investigation result for a deployment.
type InvestigationStatus struct {
	DeploymentKey string            `json:"deployment_key"`
	Result        diagnostic.Result `json:"result"`
	FailureReason string            `json:"failure_reason,omitempty"`
	Duration      time.Duration     `json:"duration"`
	Timestamp     time.Time         `json:"timestamp"`
}

// StatusCache is a thread-safe last-1 cache of investigation results per deployment key.
type StatusCache struct {
	mu      sync.RWMutex
	entries map[string]InvestigationStatus
}

// NewStatusCache creates an empty StatusCache.
func NewStatusCache() *StatusCache {
	return &StatusCache{
		entries: make(map[string]InvestigationStatus),
	}
}

// Update stores or overwrites the investigation status for a deployment key.
func (c *StatusCache) Update(status InvestigationStatus) {
	c.mu.Lock()
	c.entries[status.DeploymentKey] = status
	c.mu.Unlock()
}

// Get returns the latest investigation status for the given key.
func (c *StatusCache) Get(key string) (InvestigationStatus, bool) {
	c.mu.RLock()
	s, ok := c.entries[key]
	c.mu.RUnlock()
	return s, ok
}

// List returns a snapshot of all investigation statuses.
func (c *StatusCache) List() []InvestigationStatus {
	c.mu.RLock()
	out := make([]InvestigationStatus, 0, len(c.entries))
	for _, s := range c.entries {
		out = append(out, s)
	}
	c.mu.RUnlock()
	return out
}
