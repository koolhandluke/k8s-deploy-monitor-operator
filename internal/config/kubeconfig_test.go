package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
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

func TestLoadDirectorySnapshot_BasicHashing(t *testing.T) {
	dir := t.TempDir()

	data1 := minimalKubeconfig("https://cluster1:6443")
	data2 := minimalKubeconfig("https://cluster2:6443")

	os.WriteFile(filepath.Join(dir, "cluster1.yaml"), data1, 0644)
	os.WriteFile(filepath.Join(dir, "cluster2.yaml"), data2, 0644)

	snap, err := LoadDirectorySnapshot(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(snap.Clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(snap.Clusters))
	}

	if len(snap.FileHashes) != 2 {
		t.Fatalf("expected 2 file hashes, got %d", len(snap.FileHashes))
	}

	// Verify hashes match SHA256 of raw file bytes
	expectedHash1 := fmt.Sprintf("%x", sha256.Sum256(data1))
	if snap.FileHashes["cluster1"] != expectedHash1 {
		t.Errorf("hash mismatch for cluster1: got %s, want %s",
			snap.FileHashes["cluster1"], expectedHash1)
	}

	expectedHash2 := fmt.Sprintf("%x", sha256.Sum256(data2))
	if snap.FileHashes["cluster2"] != expectedHash2 {
		t.Errorf("hash mismatch for cluster2: got %s, want %s",
			snap.FileHashes["cluster2"], expectedHash2)
	}
}

func TestLoadDirectorySnapshot_DetectsChanges(t *testing.T) {
	dir := t.TempDir()

	data := minimalKubeconfig("https://cluster1:6443")
	os.WriteFile(filepath.Join(dir, "cluster1.yaml"), data, 0644)

	snap1, err := LoadDirectorySnapshot(dir)
	if err != nil {
		t.Fatalf("snap1 error: %v", err)
	}

	// Same content → same hash
	snap2, err := LoadDirectorySnapshot(dir)
	if err != nil {
		t.Fatalf("snap2 error: %v", err)
	}

	if snap1.FileHashes["cluster1"] != snap2.FileHashes["cluster1"] {
		t.Error("identical file should produce identical hash")
	}

	// Modify file → different hash
	newData := minimalKubeconfig("https://cluster1-new:6443")
	os.WriteFile(filepath.Join(dir, "cluster1.yaml"), newData, 0644)

	snap3, err := LoadDirectorySnapshot(dir)
	if err != nil {
		t.Fatalf("snap3 error: %v", err)
	}

	if snap1.FileHashes["cluster1"] == snap3.FileHashes["cluster1"] {
		t.Error("modified file should produce different hash")
	}
}

func TestLoadDirectorySnapshot_AddRemoveFiles(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "a.yaml"), minimalKubeconfig("https://a:6443"), 0644)

	snap1, err := LoadDirectorySnapshot(dir)
	if err != nil {
		t.Fatalf("snap1 error: %v", err)
	}
	if len(snap1.Clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(snap1.Clusters))
	}

	// Add a second cluster
	os.WriteFile(filepath.Join(dir, "b.yaml"), minimalKubeconfig("https://b:6443"), 0644)

	snap2, err := LoadDirectorySnapshot(dir)
	if err != nil {
		t.Fatalf("snap2 error: %v", err)
	}
	if len(snap2.Clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(snap2.Clusters))
	}
	if _, ok := snap2.FileHashes["b"]; !ok {
		t.Error("expected hash for new cluster 'b'")
	}

	// Remove the first cluster
	os.Remove(filepath.Join(dir, "a.yaml"))

	snap3, err := LoadDirectorySnapshot(dir)
	if err != nil {
		t.Fatalf("snap3 error: %v", err)
	}
	if len(snap3.Clusters) != 1 {
		t.Fatalf("expected 1 cluster after removal, got %d", len(snap3.Clusters))
	}
	if _, ok := snap3.FileHashes["a"]; ok {
		t.Error("removed cluster 'a' should not have a hash")
	}
}

func TestLoadDirectorySnapshot_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	snap, err := LoadDirectorySnapshot(dir)
	if err != nil {
		t.Fatalf("unexpected error on empty dir: %v", err)
	}

	if len(snap.Clusters) != 0 {
		t.Errorf("expected 0 clusters, got %d", len(snap.Clusters))
	}
	if len(snap.FileHashes) != 0 {
		t.Errorf("expected 0 hashes, got %d", len(snap.FileHashes))
	}
}

func TestLoadDirectorySnapshot_SkipsNonKubeconfigs(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a kubeconfig"), 0644)
	os.WriteFile(filepath.Join(dir, "cluster.yaml"), minimalKubeconfig("https://c:6443"), 0644)

	snap, err := LoadDirectorySnapshot(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(snap.Clusters) != 1 {
		t.Errorf("expected 1 cluster (skipping non-kubeconfig), got %d", len(snap.Clusters))
	}
}

func TestLoadDirectorySnapshot_InvalidDir(t *testing.T) {
	_, err := LoadDirectorySnapshot("/nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}
