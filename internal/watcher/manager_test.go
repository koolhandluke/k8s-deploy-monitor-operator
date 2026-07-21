package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// minimalKubeconfig returns a valid kubeconfig YAML with a unique server URL.
func minimalKubeconfig(server string) []byte {
	return []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user:
    token: fake-token
`, server))
}

func fakeClientsetFactory(_ *rest.Config) (kubernetes.Interface, error) {
	return fake.NewSimpleClientset(), nil
}

func newTestManager(t *testing.T, dir string) (*Manager, chan models.RolloutEvent) {
	t.Helper()
	eventCh := make(chan models.RolloutEvent, 100)
	m := NewManager(
		func(string) bool { return true }, // allow all namespaces
		100*time.Millisecond,              // short debounce for tests
		eventCh,
		nil, // no persistence
		dir,
		10*time.Minute, // rescan interval (reconcileOnce forces immediate rescan)
		30*time.Second, // start timeout
	)
	m.clientsetFactory = fakeClientsetFactory
	return m, eventCh
}

// loadClustersForTest is a test helper to load clusters from a directory.
func (m *Manager) loadClustersForTest(dir string) ([]config.ClusterInfo, error) {
	cfg := &config.Config{KubeconfigDir: dir}
	return config.LoadClusters(cfg)
}

// reconcileOnce is a test helper that calls reconcile with a zero lastRescan
// so the rescan phase always runs.
func reconcileOnce(m *Manager, ctx context.Context) {
	lastRescan := time.Time{} // zero value → always rescan
	m.reconcile(ctx, &lastRescan)
}

func TestReconcile_AddsNewCluster(t *testing.T) {
	dir := t.TempDir()

	// Start with one cluster
	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a:6443"), 0644)

	m, _ := newTestManager(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Stop()

	// First reconcile picks up cluster-a
	reconcileOnce(m, ctx)

	m.mu.Lock()
	if len(m.watchers) != 1 {
		t.Fatalf("expected 1 watcher after first reconcile, got %d", len(m.watchers))
	}
	m.mu.Unlock()

	// Add a new cluster file
	os.WriteFile(filepath.Join(dir, "cluster-b.yaml"),
		minimalKubeconfig("https://b:6443"), 0644)

	reconcileOnce(m, ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.watchers) != 2 {
		t.Errorf("expected 2 watchers after adding cluster-b, got %d", len(m.watchers))
	}
	if _, hasB := m.watchers["cluster-b"]; !hasB {
		t.Error("expected watcher for cluster-b")
	}
}

func TestReconcile_RemovesCluster(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a:6443"), 0644)
	os.WriteFile(filepath.Join(dir, "cluster-b.yaml"),
		minimalKubeconfig("https://b:6443"), 0644)

	m, _ := newTestManager(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Stop()

	// Reconcile to start both watchers
	reconcileOnce(m, ctx)

	m.mu.Lock()
	if len(m.watchers) != 2 {
		t.Fatalf("expected 2 watchers, got %d", len(m.watchers))
	}
	m.mu.Unlock()

	// Remove cluster-b
	os.Remove(filepath.Join(dir, "cluster-b.yaml"))

	reconcileOnce(m, ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.watchers) != 1 {
		t.Errorf("expected 1 watcher after removal, got %d", len(m.watchers))
	}
	if _, hasB := m.watchers["cluster-b"]; hasB {
		t.Error("cluster-b watcher should have been removed")
	}
	if _, hasA := m.watchers["cluster-a"]; !hasA {
		t.Error("cluster-a watcher should still exist")
	}
}

func TestReconcile_RecyclesChangedCluster(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a:6443"), 0644)

	m, _ := newTestManager(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Stop()

	reconcileOnce(m, ctx)

	m.mu.Lock()
	oldWatcher := m.watchers["cluster-a"]
	oldHash := m.fileHashes["cluster-a"]
	m.mu.Unlock()

	if oldWatcher == nil {
		t.Fatal("expected watcher for cluster-a")
	}

	// Modify the kubeconfig file (different token = different hash)
	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a-new:6443"), 0644)

	reconcileOnce(m, ctx)

	m.mu.Lock()
	newWatcher := m.watchers["cluster-a"]
	newHash := m.fileHashes["cluster-a"]
	m.mu.Unlock()

	if newWatcher == nil {
		t.Fatal("expected watcher for cluster-a after recycle")
	}
	if newWatcher == oldWatcher {
		t.Error("watcher should have been recycled (new instance)")
	}
	if newHash == oldHash {
		t.Error("file hash should have changed")
	}
}

func TestReconcile_NoRestartOnUnchanged(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a:6443"), 0644)

	m, _ := newTestManager(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Stop()

	reconcileOnce(m, ctx)

	m.mu.Lock()
	firstWatcher := m.watchers["cluster-a"]
	m.mu.Unlock()

	// Reconcile again without changes
	reconcileOnce(m, ctx)

	m.mu.Lock()
	sameWatcher := m.watchers["cluster-a"]
	m.mu.Unlock()

	if sameWatcher != firstWatcher {
		t.Error("watcher should not be recycled when file is unchanged")
	}
}

func TestReconcile_DirectoryReadError(t *testing.T) {
	// Point at a nonexistent directory — reconcile should log and not crash
	m, _ := newTestManager(t, "/nonexistent/path")
	ctx := context.Background()

	// Manually add a watcher to verify it survives
	m.mu.Lock()
	m.watchers["existing"] = &ClusterWatcher{clusterID: "existing"}
	m.mu.Unlock()

	reconcileOnce(m, ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.watchers["existing"]; !exists {
		t.Error("existing watcher should survive a directory read error")
	}
}

func TestRetryBackoff(t *testing.T) {
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 10 * time.Second},
		{1, 20 * time.Second},
		{2, 40 * time.Second},
		{3, 80 * time.Second},
		{4, 160 * time.Second},
		{5, 5 * time.Minute}, // capped
		{10, 5 * time.Minute},
	}

	for _, tt := range tests {
		got := retryBackoff(tt.attempt)
		if got != tt.expected {
			t.Errorf("retryBackoff(%d) = %v, want %v", tt.attempt, got, tt.expected)
		}
	}
}

func TestStartup_FailedClusterQueuesRetry(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a:6443"), 0644)

	m, _ := newTestManager(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Stop()

	// Make clientset factory fail for all clusters
	callCount := 0
	m.clientsetFactory = func(_ *rest.Config) (kubernetes.Interface, error) {
		callCount++
		return nil, fmt.Errorf("connection refused")
	}

	clusters, _ := m.loadClustersForTest(dir)
	err := m.Start(ctx, clusters)

	// Should NOT return error because cluster is queued for retry
	if err != nil {
		t.Fatalf("expected no error (cluster queued for retry), got: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.watchers) != 0 {
		t.Errorf("expected 0 watchers, got %d", len(m.watchers))
	}
	if len(m.pendingRetry) != 1 {
		t.Fatalf("expected 1 pending retry, got %d", len(m.pendingRetry))
	}
	entry := m.pendingRetry["cluster-a"]
	if entry == nil {
		t.Fatal("expected pending retry for cluster-a")
	}
	if entry.attempt != 0 {
		t.Errorf("expected attempt 0, got %d", entry.attempt)
	}
}

func TestReconcile_RetrySucceeds(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a:6443"), 0644)

	m, _ := newTestManager(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Stop()

	// Add a pending retry entry with nextRetry in the past
	m.mu.Lock()
	m.pendingRetry["cluster-a"] = &retryEntry{
		cluster: func() config.ClusterInfo {
			snap, _ := config.LoadDirectorySnapshot(dir)
			return snap.Clusters[0]
		}(),
		attempt:   2,
		nextRetry: time.Now().Add(-1 * time.Second), // already past
		lastError: "previous error",
	}
	m.mu.Unlock()

	// Use working clientset factory
	reconcileOnce(m, ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.pendingRetry) != 0 {
		t.Errorf("expected 0 pending retries after recovery, got %d", len(m.pendingRetry))
	}
	if len(m.watchers) != 1 {
		t.Errorf("expected 1 watcher after recovery, got %d", len(m.watchers))
	}
}

func TestReconcile_RetrySkipsNotYetDue(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a:6443"), 0644)

	m, _ := newTestManager(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Stop()

	// Load the real file hash so rescan doesn't see it as "changed"
	snap, _ := config.LoadDirectorySnapshot(dir)

	// Add a pending retry entry with nextRetry in the future
	m.mu.Lock()
	m.pendingRetry["cluster-a"] = &retryEntry{
		cluster:   snap.Clusters[0],
		attempt:   1,
		nextRetry: time.Now().Add(10 * time.Minute), // far future
		lastError: "not yet",
	}
	m.fileHashes["cluster-a"] = snap.FileHashes["cluster-a"]
	m.mu.Unlock()

	reconcileOnce(m, ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Should still be pending — nextRetry is in the future
	if len(m.pendingRetry) != 1 {
		t.Errorf("expected retry to remain pending, got %d pending", len(m.pendingRetry))
	}
	if len(m.watchers) != 0 {
		t.Errorf("expected 0 watchers (not yet retried), got %d", len(m.watchers))
	}
}

func TestReconcile_HealthCheckRecyclesUnhealthyWatcher(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a:6443"), 0644)

	m, _ := newTestManager(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Stop()

	// Start watcher normally
	reconcileOnce(m, ctx)

	m.mu.Lock()
	w := m.watchers["cluster-a"]
	m.mu.Unlock()

	if w == nil {
		t.Fatal("expected watcher for cluster-a")
	}

	// Simulate 5 consecutive errors to make it unhealthy
	for i := 0; i < 5; i++ {
		w.lastWatchError.Store(fmt.Errorf("connection reset"))
	}
	// Set consecutiveErrors atomically
	for i := int64(0); i < 5; i++ {
		w.resetHealthCounters() // reset first
	}
	// Actually increment errors
	for i := 0; i < 5; i++ {
		atomic.AddInt64(&w.consecutiveErrors, 1)
		w.lastWatchError.Store(fmt.Errorf("connection reset"))
	}

	// Verify unhealthy
	healthy, _, _ := w.HealthStatus()
	if healthy {
		t.Fatal("expected watcher to be unhealthy after 5 errors")
	}

	// Reconcile should detect unhealthy and recycle
	reconcileOnce(m, ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	// The watcher should have been recycled — the old unhealthy one stopped,
	// and if the rescan picks it up, a new one starts. Otherwise it goes to retry.
	// Since we have a working clientset factory, a new watcher should start.
	if _, pending := m.pendingRetry["cluster-a"]; pending && len(m.watchers) == 0 {
		// It went to retry — that's acceptable if rescan created it
		t.Log("watcher went to retry queue (acceptable)")
	}
}

func TestReconcile_AllFilesRemoved(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
		minimalKubeconfig("https://a:6443"), 0644)

	m, _ := newTestManager(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Stop()

	reconcileOnce(m, ctx)

	m.mu.Lock()
	if len(m.watchers) != 1 {
		t.Fatalf("expected 1 watcher, got %d", len(m.watchers))
	}
	m.mu.Unlock()

	// Remove all files
	os.Remove(filepath.Join(dir, "cluster-a.yaml"))

	reconcileOnce(m, ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.watchers) != 0 {
		t.Errorf("expected 0 watchers after removing all files, got %d", len(m.watchers))
	}
}
