package watcher

import (
	"sync"
)

// NamespaceFilter is a thread-safe namespace allow/deny filter that can be
// updated at runtime (e.g. from a MonitorConfig CRD).
type NamespaceFilter struct {
	mu        sync.RWMutex
	allowlist map[string]bool
	denylist  map[string]bool
}

// NewNamespaceFilter creates a filter seeded with the given allow/deny lists.
func NewNamespaceFilter(allow, deny []string) *NamespaceFilter {
	f := &NamespaceFilter{}
	f.Update(allow, deny)
	return f
}

// Allowed returns true if the namespace passes the allow/deny filter.
// If an allowlist is set, only namespaces in it are allowed.
// Otherwise, namespaces in the denylist are rejected.
func (f *NamespaceFilter) Allowed(ns string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if len(f.allowlist) > 0 {
		return f.allowlist[ns]
	}
	return !f.denylist[ns]
}

// Update replaces the allow/deny lists atomically.
func (f *NamespaceFilter) Update(allow, deny []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.allowlist = toSet(allow)
	f.denylist = toSet(deny)
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
}
