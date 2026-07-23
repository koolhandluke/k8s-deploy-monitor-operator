package config

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ClusterInfo holds connection details for a single cluster.
type ClusterInfo struct {
	ID         string       // unique identifier (context name or filename stem)
	RestConfig *rest.Config
}

// LoadClusters returns cluster configs from the KUBECONFIG_DIR directory.
// Returns an error if KubeconfigDir is empty.
func LoadClusters(cfg *Config) ([]ClusterInfo, error) {
	if cfg.KubeconfigDir == "" {
		return nil, fmt.Errorf("KUBECONFIG_DIR is required")
	}
	return loadFromDirectory(cfg.KubeconfigDir)
}

// loadFromDirectory reads one kubeconfig file per cluster from a directory.
func loadFromDirectory(dir string) ([]ClusterInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig dir %s: %w", dir, err)
	}

	var clusters []ClusterInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isKubeconfigFile(name) {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Error("failed to read kubeconfig", "path", path, "error", err)
			continue
		}

		restCfg, err := clientcmd.RESTConfigFromKubeConfig(data)
		if err != nil {
			slog.Error("failed to parse kubeconfig", "path", path, "error", err)
			continue
		}

		stem := filenameStem(name)
		clusters = append(clusters, ClusterInfo{
			ID:         stem,
			RestConfig: restCfg,
		})
	}

	if len(clusters) == 0 {
		return nil, fmt.Errorf("no valid kubeconfig files found in %s", dir)
	}

	slog.Info("loaded clusters from directory", "dir", dir, "count", len(clusters))
	return clusters, nil
}

// ClusterSnapshot holds clusters and file content hashes from a directory scan.
type ClusterSnapshot struct {
	Clusters   []ClusterInfo
	FileHashes map[string]string // clusterID → SHA256 of raw file bytes
}

// LoadDirectorySnapshot reads kubeconfig files from a directory and returns
// clusters with SHA256 hashes of each file's raw bytes. The hashes allow
// detecting file changes without unnecessary watcher restarts.
func LoadDirectorySnapshot(dir string) (*ClusterSnapshot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig dir %s: %w", dir, err)
	}

	snap := &ClusterSnapshot{
		FileHashes: make(map[string]string),
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isKubeconfigFile(name) {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Error("failed to read kubeconfig", "path", path, "error", err)
			continue
		}

		restCfg, err := clientcmd.RESTConfigFromKubeConfig(data)
		if err != nil {
			slog.Error("failed to parse kubeconfig", "path", path, "error", err)
			continue
		}

		stem := filenameStem(name)
		hash := fmt.Sprintf("%x", sha256.Sum256(data))

		snap.Clusters = append(snap.Clusters, ClusterInfo{
			ID:         stem,
			RestConfig: restCfg,
		})
		snap.FileHashes[stem] = hash
	}

	return snap, nil
}

// isKubeconfigFile checks if a filename has a supported kubeconfig extension.
func isKubeconfigFile(name string) bool {
	return strings.HasSuffix(name, ".yaml") ||
		strings.HasSuffix(name, ".yml") ||
		strings.HasSuffix(name, ".conf")
}

// filenameStem strips the kubeconfig file extension to get the cluster ID.
func filenameStem(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml"), ".conf")
}
