package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvConfig defines the monitoring scope for a single environment.
// One YAML file per environment, loaded from the env config directory.
type EnvConfig struct {
	Clusters []ClusterDeployments `yaml:"clusters"`
}

// ClusterDeployments defines the deployments to watch on a single cluster.
type ClusterDeployments struct {
	Name        string              `yaml:"name"`
	Deployments []AppDeployment     `yaml:"deployments"`
}

// AppDeployment maps an app to its namespaces on a cluster.
type AppDeployment struct {
	App        string   `yaml:"app"`
	Namespaces []string `yaml:"namespaces"`
}

// SlackRouting maps app names to Slack channel IDs/names.
type SlackRouting map[string]string

// LoadEnvConfigs reads all YAML files from the given directory and returns
// the merged env configs. Each file represents one environment.
func LoadEnvConfigs(dir string) ([]EnvConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading env config dir %s: %w", dir, err)
	}

	var configs []EnvConfig
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Error("failed to read env config", "path", path, "error", err)
			continue
		}

		var ec EnvConfig
		if err := yaml.Unmarshal(data, &ec); err != nil {
			slog.Error("failed to parse env config", "path", path, "error", err)
			continue
		}

		configs = append(configs, ec)
		slog.Info("loaded env config", "file", name, "clusters", len(ec.Clusters))
	}

	if len(configs) == 0 {
		return nil, fmt.Errorf("no valid env config files found in %s", dir)
	}

	return configs, nil
}

// LoadSlackRouting reads a slack routing YAML file (app → channel mapping).
func LoadSlackRouting(path string) (SlackRouting, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading slack routing file %s: %w", path, err)
	}

	routing := make(SlackRouting)
	if err := yaml.Unmarshal(data, &routing); err != nil {
		return nil, fmt.Errorf("parsing slack routing file: %w", err)
	}

	slog.Info("loaded slack routing", "apps", len(routing))
	return routing, nil
}

// NamespaceLookup is a pre-built lookup table for namespace → app name,
// scoped per cluster.
type NamespaceLookup struct {
	// clusterID → namespace → app name
	table map[string]map[string]string
}

// BuildNamespaceLookup builds a lookup table from env configs for fast
// namespace → app resolution at event time.
func BuildNamespaceLookup(configs []EnvConfig) *NamespaceLookup {
	table := make(map[string]map[string]string)
	for _, ec := range configs {
		for _, cluster := range ec.Clusters {
			if table[cluster.Name] == nil {
				table[cluster.Name] = make(map[string]string)
			}
			for _, deploy := range cluster.Deployments {
				for _, ns := range deploy.Namespaces {
					if existing, ok := table[cluster.Name][ns]; ok {
						slog.Warn("namespace claimed by multiple apps",
							"cluster", cluster.Name,
							"namespace", ns,
							"existing_app", existing,
							"new_app", deploy.App,
						)
						continue
					}
					table[cluster.Name][ns] = deploy.App
				}
			}
		}
	}
	return &NamespaceLookup{table: table}
}

// GetApp returns the app name for a cluster+namespace pair.
// Returns empty string if not found.
func (l *NamespaceLookup) GetApp(clusterID, namespace string) string {
	if l == nil || l.table == nil {
		return ""
	}
	nsMap, ok := l.table[clusterID]
	if !ok {
		return ""
	}
	return nsMap[namespace]
}

// GetSlackChannel returns the Slack channel for a cluster+namespace pair
// by resolving namespace → app → channel.
func (l *NamespaceLookup) GetSlackChannel(clusterID, namespace string, routing SlackRouting) string {
	app := l.GetApp(clusterID, namespace)
	if app == "" || routing == nil {
		return ""
	}
	return routing[app]
}

// RequiredClusters returns the deduplicated set of cluster IDs from all env configs.
func RequiredClusters(configs []EnvConfig) []string {
	seen := make(map[string]bool)
	var out []string
	for _, ec := range configs {
		for _, cluster := range ec.Clusters {
			if !seen[cluster.Name] {
				seen[cluster.Name] = true
				out = append(out, cluster.Name)
			}
		}
	}
	return out
}

// AllowedNamespaces returns the set of namespaces that should be watched
// on a given cluster, based on env configs.
func AllowedNamespaces(configs []EnvConfig, clusterID string) map[string]bool {
	ns := make(map[string]bool)
	for _, ec := range configs {
		for _, cluster := range ec.Clusters {
			if cluster.Name != clusterID {
				continue
			}
			for _, deploy := range cluster.Deployments {
				for _, n := range deploy.Namespaces {
					ns[n] = true
				}
			}
		}
	}
	return ns
}
