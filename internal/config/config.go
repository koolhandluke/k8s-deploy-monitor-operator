package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type DispatchMode string

const (
	DispatchLog    DispatchMode = "log"
	DispatchHolmes DispatchMode = "holmes"
	DispatchSlack  DispatchMode = "slack"
	DispatchBoth   DispatchMode = "both"
)

type Config struct {
	// Cluster credentials
	KubeconfigDir  string // directory of kubeconfig files (multi-cluster)
	KubeconfigPath string // single kubeconfig file (local dev mode)

	// Namespace filtering
	NamespaceAllowlist []string
	NamespaceDenylist  []string

	// Dispatch
	DispatchMode    DispatchMode
	HolmesAPIURL    string
	SlackWebhookURL string

	// Tuning
	WorkerCount          int
	DebounceSeconds      int
	QueueMaxSize         int
	RescanIntervalSeconds int

	// Persistence
	PersistenceEnabled   bool
	PersistenceNamespace string

	// Diagnostic
	DiagnosticEnabled       bool
	DiagnosticMaxConcurrent int

	// Logging
	Debug bool
}

func Load() (*Config, error) {
	c := &Config{
		KubeconfigDir:  os.Getenv("KUBECONFIG_DIR"),
		KubeconfigPath: os.Getenv("KUBECONFIG"),
		HolmesAPIURL:   os.Getenv("HOLMES_API_URL"),
		SlackWebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
	}

	// Namespace filtering
	if v := os.Getenv("NAMESPACE_ALLOWLIST"); v != "" {
		c.NamespaceAllowlist = splitTrim(v)
	}
	if v := os.Getenv("NAMESPACE_DENYLIST"); v != "" {
		c.NamespaceDenylist = splitTrim(v)
	}
	// Default denylist if nothing is set
	if len(c.NamespaceAllowlist) == 0 && len(c.NamespaceDenylist) == 0 {
		c.NamespaceDenylist = []string{"kube-system", "kube-public", "kube-node-lease"}
	}

	// Dispatch mode
	mode := strings.ToLower(os.Getenv("DISPATCH_MODE"))
	switch DispatchMode(mode) {
	case DispatchHolmes, DispatchSlack, DispatchBoth, DispatchLog:
		c.DispatchMode = DispatchMode(mode)
	case "":
		c.DispatchMode = DispatchLog
	default:
		return nil, fmt.Errorf("invalid DISPATCH_MODE: %q (must be log|holmes|slack|both)", mode)
	}

	// Validate dispatch dependencies
	if (c.DispatchMode == DispatchHolmes || c.DispatchMode == DispatchBoth) && c.HolmesAPIURL == "" {
		return nil, fmt.Errorf("HOLMES_API_URL required when DISPATCH_MODE=%s", c.DispatchMode)
	}
	if (c.DispatchMode == DispatchSlack || c.DispatchMode == DispatchBoth) && c.SlackWebhookURL == "" {
		return nil, fmt.Errorf("SLACK_WEBHOOK_URL required when DISPATCH_MODE=%s", c.DispatchMode)
	}

	// Tuning with defaults
	c.WorkerCount = envInt("WORKER_COUNT", 3)
	c.DebounceSeconds = envInt("DEBOUNCE_SECONDS", 30)
	c.QueueMaxSize = envInt("QUEUE_MAX_SIZE", 100)
	c.RescanIntervalSeconds = envInt("RESCAN_INTERVAL_SECONDS", 600)

	// Diagnostic
	c.DiagnosticEnabled = strings.ToLower(os.Getenv("DIAGNOSTIC_ENABLED")) == "true"
	c.DiagnosticMaxConcurrent = envInt("DIAGNOSTIC_MAX_CONCURRENT", 10)

	// Logging
	c.Debug = strings.ToLower(os.Getenv("DEBUG")) == "true"

	// Persistence
	c.PersistenceEnabled = strings.ToLower(os.Getenv("PERSISTENCE_ENABLED")) == "true"
	c.PersistenceNamespace = os.Getenv("PERSISTENCE_NAMESPACE")
	if c.PersistenceEnabled && c.PersistenceNamespace == "" {
		c.PersistenceNamespace = "rollout-monitor"
	}

	return c, nil
}

// NamespaceAllowed returns true if the namespace passes the allow/deny filter.
func (c *Config) NamespaceAllowed(ns string) bool {
	if len(c.NamespaceAllowlist) > 0 {
		for _, allowed := range c.NamespaceAllowlist {
			if ns == allowed {
				return true
			}
		}
		return false
	}
	for _, denied := range c.NamespaceDenylist {
		if ns == denied {
			return false
		}
	}
	return true
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func envInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}
