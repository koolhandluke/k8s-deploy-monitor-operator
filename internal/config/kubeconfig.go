package config

import (
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
	Name       string       // human-readable name
	RestConfig *rest.Config
}

// LoadClusters returns cluster configs based on the Config.
// Priority: KUBECONFIG_DIR (multi-cluster) > KUBECONFIG (single) > default kubeconfig.
func LoadClusters(cfg *Config) ([]ClusterInfo, error) {
	if cfg.KubeconfigDir != "" {
		return loadFromDirectory(cfg.KubeconfigDir)
	}
	return loadFromKubeconfig(cfg.KubeconfigPath)
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
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".conf") {
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

		stem := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml"), ".conf")
		clusters = append(clusters, ClusterInfo{
			ID:         stem,
			Name:       stem,
			RestConfig: restCfg,
		})
	}

	if len(clusters) == 0 {
		return nil, fmt.Errorf("no valid kubeconfig files found in %s", dir)
	}

	slog.Info("loaded clusters from directory", "dir", dir, "count", len(clusters))
	return clusters, nil
}

// loadFromKubeconfig loads from a single kubeconfig file (or default location).
// Uses the current context.
func loadFromKubeconfig(kubeconfigPath string) ([]ClusterInfo, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	contextName := rawConfig.CurrentContext
	if contextName == "" {
		return nil, fmt.Errorf("no current context set in kubeconfig")
	}

	restCfg, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building rest config for context %s: %w", contextName, err)
	}

	slog.Info("loaded single cluster from kubeconfig", "context", contextName)
	return []ClusterInfo{
		{
			ID:         contextName,
			Name:       contextName,
			RestConfig: restCfg,
		},
	}, nil
}
