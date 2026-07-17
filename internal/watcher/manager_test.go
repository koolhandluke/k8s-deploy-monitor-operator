package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

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
		0, // rescan disabled for direct reconcile calls
	)
	m.clientsetFactory = fakeClientsetFactory
	return m, eventCh
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
	m.reconcile(ctx)

	m.mu.Lock()
	if len(m.watchers) != 1 {
		t.Fatalf("expected 1 watcher after first reconcile, got %d", len(m.watchers))
	}
	m.mu.Unlock()

	// Add a new cluster file
	os.WriteFile(filepath.Join(dir, "cluster-b.yaml"),
		minimalKubeconfig("https://b:6443"), 0644)

	m.reconcile(ctx)

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
	m.reconcile(ctx)

	m.mu.Lock()
	if len(m.watchers) != 2 {
		t.Fatalf("expected 2 watchers, got %d", len(m.watchers))
	}
	m.mu.Unlock()

	// Remove cluster-b
	os.Remove(filepath.Join(dir, "cluster-b.yaml"))

	m.reconcile(ctx)

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

	m.reconcile(ctx)

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

	m.reconcile(ctx)

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

	m.reconcile(ctx)

	m.mu.Lock()
	firstWatcher := m.watchers["cluster-a"]
	m.mu.Unlock()

	// Reconcile again without changes
	m.reconcile(ctx)

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

	m.reconcile(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.watchers["existing"]; !exists {
		t.Error("existing watcher should survive a directory read error")
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

	m.reconcile(ctx)

	m.mu.Lock()
	if len(m.watchers) != 1 {
		t.Fatalf("expected 1 watcher, got %d", len(m.watchers))
	}
	m.mu.Unlock()

	// Remove all files
	os.Remove(filepath.Join(dir, "cluster-a.yaml"))

	m.reconcile(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.watchers) != 0 {
		t.Errorf("expected 0 watchers after removing all files, got %d", len(m.watchers))
	}
}
