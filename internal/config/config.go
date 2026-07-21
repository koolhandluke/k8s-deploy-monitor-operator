// Package config loads and validates application configuration from environment
// variables, including cluster credentials, namespace filtering, dispatch mode,
// and persistence settings.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// DispatchMode controls which notification targets receive rollout events.
type DispatchMode string

const (
	// DispatchLog sends events to structured log output only.
	DispatchLog DispatchMode = "log"
	// DispatchHolmes sends events to the Holmes API for AI-powered analysis.
	DispatchHolmes DispatchMode = "holmes"
	// DispatchSlack sends events to a Slack webhook.
	DispatchSlack DispatchMode = "slack"
	// DispatchBoth sends events to both Holmes and Slack.
	DispatchBoth DispatchMode = "both"
)

// InvestigationMode controls how rollout outcomes are investigated and reported.
type InvestigationMode string

const (
	// InvestigationNone disables post-rollout investigation.
	InvestigationNone InvestigationMode = "none"
	// InvestigationRunbook runs the built-in diagnostic runbook and reports to Slack.
	InvestigationRunbook InvestigationMode = "runbook"
	// InvestigationHolmes delegates investigation to Holmes AI and reports to Slack.
	InvestigationHolmes InvestigationMode = "holmes"
)

// Config holds all application settings loaded from environment variables.
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
	RolloutRecordTTLDays int

	// Split mode: when true, monitor only writes CRDs; dispatcher service handles dispatch
	DispatcherSplit bool

	// Diagnostic (legacy, superseded by InvestigationMode)
	DiagnosticEnabled       bool
	DiagnosticMaxConcurrent int

	// Investigation
	InvestigationMode          InvestigationMode
	InvestigationMaxConcurrent int

	// Logging
	Debug         bool
	Trace         bool
	StatusAPIPort int
}

// Load reads environment variables and returns a validated Config.
// It returns an error if required variables are missing or values are invalid.
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

	// Diagnostic (legacy)
	c.DiagnosticEnabled = strings.ToLower(os.Getenv("DIAGNOSTIC_ENABLED")) == "true"
	c.DiagnosticMaxConcurrent = envInt("DIAGNOSTIC_MAX_CONCURRENT", 10)

	// Investigation mode
	invMode := strings.ToLower(os.Getenv("INVESTIGATION_MODE"))
	switch InvestigationMode(invMode) {
	case InvestigationRunbook, InvestigationHolmes, InvestigationNone:
		c.InvestigationMode = InvestigationMode(invMode)
	case "":
		c.InvestigationMode = InvestigationNone
		// Backward compat: map DISPATCH_MODE to InvestigationMode if INVESTIGATION_MODE not set
		if c.DispatchMode == DispatchHolmes || c.DispatchMode == DispatchBoth {
			if c.DiagnosticEnabled {
				// Old config had both diagnostic and holmes; map to holmes investigation
				c.InvestigationMode = InvestigationHolmes
				slog.Warn("DISPATCH_MODE is deprecated for investigation; use INVESTIGATION_MODE=holmes")
			}
		}
		if c.DispatchMode == DispatchSlack && c.DiagnosticEnabled {
			c.InvestigationMode = InvestigationRunbook
			slog.Warn("DISPATCH_MODE is deprecated for investigation; use INVESTIGATION_MODE=runbook")
		}
	default:
		return nil, fmt.Errorf("invalid INVESTIGATION_MODE: %q (must be none|runbook|holmes)", invMode)
	}
	c.InvestigationMaxConcurrent = envInt("INVESTIGATION_MAX_CONCURRENT", 10)

	// Validate investigation dependencies
	if c.InvestigationMode == InvestigationRunbook && c.SlackWebhookURL == "" {
		return nil, fmt.Errorf("SLACK_WEBHOOK_URL required when INVESTIGATION_MODE=runbook")
	}
	if c.InvestigationMode == InvestigationHolmes {
		if c.SlackWebhookURL == "" {
			return nil, fmt.Errorf("SLACK_WEBHOOK_URL required when INVESTIGATION_MODE=holmes")
		}
		if c.HolmesAPIURL == "" {
			return nil, fmt.Errorf("HOLMES_API_URL required when INVESTIGATION_MODE=holmes")
		}
	}

	// Logging
	c.Debug = strings.ToLower(os.Getenv("DEBUG")) == "true"
	c.Trace = strings.ToLower(os.Getenv("TRACE")) == "true"
	c.StatusAPIPort = envInt("STATUS_API_PORT", 8081)

	// Persistence
	c.PersistenceEnabled = strings.ToLower(os.Getenv("PERSISTENCE_ENABLED")) == "true"
	c.PersistenceNamespace = os.Getenv("PERSISTENCE_NAMESPACE")
	if c.PersistenceEnabled && c.PersistenceNamespace == "" {
		c.PersistenceNamespace = "rollout-monitor"
	}
	c.RolloutRecordTTLDays = envInt("ROLLOUT_RECORD_TTL_DAYS", 7)
	c.DispatcherSplit = strings.ToLower(os.Getenv("DISPATCHER_SPLIT")) == "true"

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

// splitTrim splits a comma-separated string and trims whitespace from each element.
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

// envInt reads an integer environment variable, returning defaultVal if unset or unparseable.
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
