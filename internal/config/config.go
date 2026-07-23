// Package config loads and validates application configuration from a YAML
// config file and optional environment variable overrides for secrets.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
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

// Config holds all application settings.
// Shared by both the monitor and dispatcher binaries.
type Config struct {
	// --- Shared (used by both monitor and dispatcher) ---
	Debug            bool         `yaml:"debug"`
	DispatchMode     DispatchMode `yaml:"dispatchMode"`
	SlackWebhookURL  string       `yaml:"slackWebhookURL"`
	SlackBotToken    string       `yaml:"slackBotToken"`
	PersistenceNamespace string   `yaml:"persistenceNamespace"`

	// --- Monitor only ---
	KubeconfigDir              string            `yaml:"kubeconfigDir"`
	EnvConfigDir               string            `yaml:"envConfigDir"`
	SlackRoutingFile           string            `yaml:"slackRoutingFile"`
	Trace                      bool              `yaml:"trace"`
	StatusAPIPort              int               `yaml:"statusAPIPort"`
	NamespaceAllowlist         []string          `yaml:"namespaceAllowlist"`
	NamespaceDenylist          []string          `yaml:"namespaceDenylist"`
	HolmesAPIURL               string            `yaml:"holmesAPIURL"`
	WorkerCount                int               `yaml:"workerCount"`
	DebounceSeconds            int               `yaml:"debounceSeconds"`
	QueueMaxSize               int               `yaml:"queueMaxSize"`
	RescanIntervalSeconds      int               `yaml:"rescanIntervalSeconds"`
	WatcherStartTimeoutSeconds int               `yaml:"watcherStartTimeoutSeconds"`
	PersistenceEnabled         bool              `yaml:"persistenceEnabled"`
	DispatcherSplit            bool              `yaml:"dispatcherSplit"`
	InvestigationMode          InvestigationMode `yaml:"investigationMode"`
	InvestigationMaxConcurrent int               `yaml:"investigationMaxConcurrent"`
	DiagnosticEnabled          bool              `yaml:"diagnosticEnabled"`
	DiagnosticMaxConcurrent    int               `yaml:"diagnosticMaxConcurrent"`

	// --- Dispatcher only ---
	RolloutRecordTTLDays int `yaml:"rolloutRecordTTLDays"`
}

// DefaultConfigPath is the default location for the YAML config file.
const DefaultConfigPath = "/etc/rollout-monitor/config.yaml"

// Load reads configuration from the YAML file specified by CONFIG_FILE env var
// (default /etc/rollout-monitor/config.yaml), then applies env var overrides
// for secrets and backward compatibility. If no config file exists, falls back
// to pure env var loading for backward compatibility.
func Load() (*Config, error) {
	path := os.Getenv("CONFIG_FILE")
	if path == "" {
		path = DefaultConfigPath
	}

	c, err := LoadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Fall back to env-var-only loading for backward compat
			slog.Info("no config file found, falling back to env vars", "path", path)
			c = &Config{}
			loadFromEnvVars(c)
		} else {
			return nil, fmt.Errorf("reading config file %s: %w", path, err)
		}
	}

	// Env var overrides for secrets (always applied — supports Secret mounts)
	applyEnvOverrides(c)

	// Apply defaults for zero values
	applyDefaults(c)

	// Validate
	if err := validate(c); err != nil {
		return nil, err
	}

	return c, nil
}

// LoadFile reads and unmarshals a YAML config file.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	return c, nil
}

// applyEnvOverrides applies environment variable overrides for sensitive values
// and backward compatibility. Env vars take precedence over the config file.
func applyEnvOverrides(c *Config) {
	if v := os.Getenv("SLACK_WEBHOOK_URL"); v != "" {
		c.SlackWebhookURL = v
	}
	if v := os.Getenv("SLACK_BOT_TOKEN"); v != "" {
		c.SlackBotToken = v
	}
	if v := os.Getenv("HOLMES_API_URL"); v != "" {
		c.HolmesAPIURL = v
	}
	if v := os.Getenv("KUBECONFIG_DIR"); v != "" {
		c.KubeconfigDir = v
	}
	if v := os.Getenv("ENV_CONFIG_DIR"); v != "" {
		c.EnvConfigDir = v
	}
	if v := os.Getenv("SLACK_ROUTING_FILE"); v != "" {
		c.SlackRoutingFile = v
	}
}

// loadFromEnvVars populates a Config entirely from environment variables.
// Used as fallback when no config file exists.
func loadFromEnvVars(c *Config) {
	c.KubeconfigDir = os.Getenv("KUBECONFIG_DIR")
	c.HolmesAPIURL = os.Getenv("HOLMES_API_URL")
	c.SlackWebhookURL = os.Getenv("SLACK_WEBHOOK_URL")
	c.SlackBotToken = os.Getenv("SLACK_BOT_TOKEN")

	if v := os.Getenv("NAMESPACE_ALLOWLIST"); v != "" {
		c.NamespaceAllowlist = splitTrim(v)
	}
	if v := os.Getenv("NAMESPACE_DENYLIST"); v != "" {
		c.NamespaceDenylist = splitTrim(v)
	}

	mode := strings.ToLower(os.Getenv("DISPATCH_MODE"))
	if mode != "" {
		c.DispatchMode = DispatchMode(mode)
	}

	invMode := strings.ToLower(os.Getenv("INVESTIGATION_MODE"))
	if invMode != "" {
		c.InvestigationMode = InvestigationMode(invMode)
	}

	c.WorkerCount = envInt("WORKER_COUNT", 0)
	c.DebounceSeconds = envInt("DEBOUNCE_SECONDS", 0)
	c.QueueMaxSize = envInt("QUEUE_MAX_SIZE", 0)
	c.RescanIntervalSeconds = envInt("RESCAN_INTERVAL_SECONDS", 0)
	c.WatcherStartTimeoutSeconds = envInt("WATCHER_START_TIMEOUT_SECONDS", 0)
	c.InvestigationMaxConcurrent = envInt("INVESTIGATION_MAX_CONCURRENT", 0)
	c.StatusAPIPort = envInt("STATUS_API_PORT", 0)
	c.RolloutRecordTTLDays = envInt("ROLLOUT_RECORD_TTL_DAYS", 0)
	c.DiagnosticMaxConcurrent = envInt("DIAGNOSTIC_MAX_CONCURRENT", 0)

	c.Debug = strings.ToLower(os.Getenv("DEBUG")) == "true"
	c.Trace = strings.ToLower(os.Getenv("TRACE")) == "true"
	c.PersistenceEnabled = strings.ToLower(os.Getenv("PERSISTENCE_ENABLED")) == "true"
	c.DispatcherSplit = strings.ToLower(os.Getenv("DISPATCHER_SPLIT")) == "true"
	c.DiagnosticEnabled = strings.ToLower(os.Getenv("DIAGNOSTIC_ENABLED")) == "true"

	c.PersistenceNamespace = os.Getenv("PERSISTENCE_NAMESPACE")
}

// applyDefaults sets default values for any fields that are zero-valued.
func applyDefaults(c *Config) {
	if c.DispatchMode == "" {
		c.DispatchMode = DispatchLog
	}
	if c.InvestigationMode == "" {
		c.InvestigationMode = InvestigationNone
	}
	if len(c.NamespaceAllowlist) == 0 && len(c.NamespaceDenylist) == 0 {
		c.NamespaceDenylist = []string{"kube-system", "kube-public", "kube-node-lease"}
	}
	if c.WorkerCount == 0 {
		c.WorkerCount = 3
	}
	if c.DebounceSeconds == 0 {
		c.DebounceSeconds = 30
	}
	if c.QueueMaxSize == 0 {
		c.QueueMaxSize = 100
	}
	if c.RescanIntervalSeconds == 0 {
		c.RescanIntervalSeconds = 600
	}
	if c.WatcherStartTimeoutSeconds == 0 {
		c.WatcherStartTimeoutSeconds = 30
	}
	if c.InvestigationMaxConcurrent == 0 {
		c.InvestigationMaxConcurrent = 10
	}
	if c.StatusAPIPort == 0 {
		c.StatusAPIPort = 8081
	}
	if c.PersistenceEnabled && c.PersistenceNamespace == "" {
		c.PersistenceNamespace = "rollout-monitor"
	}
	if c.RolloutRecordTTLDays == 0 {
		c.RolloutRecordTTLDays = 7
	}
	if c.DiagnosticMaxConcurrent == 0 {
		c.DiagnosticMaxConcurrent = 10
	}

	// Backward compat: map DISPATCH_MODE to InvestigationMode if not set
	if c.InvestigationMode == InvestigationNone {
		if (c.DispatchMode == DispatchHolmes || c.DispatchMode == DispatchBoth) && c.DiagnosticEnabled {
			c.InvestigationMode = InvestigationHolmes
			slog.Warn("DISPATCH_MODE is deprecated for investigation; use investigationMode: holmes")
		}
		if c.DispatchMode == DispatchSlack && c.DiagnosticEnabled {
			c.InvestigationMode = InvestigationRunbook
			slog.Warn("DISPATCH_MODE is deprecated for investigation; use investigationMode: runbook")
		}
	}
}

// validate checks that all required config dependencies are met.
func validate(c *Config) error {
	switch c.DispatchMode {
	case DispatchHolmes, DispatchSlack, DispatchBoth, DispatchLog:
		// valid
	default:
		return fmt.Errorf("invalid dispatchMode: %q (must be log|holmes|slack|both)", c.DispatchMode)
	}

	if (c.DispatchMode == DispatchHolmes || c.DispatchMode == DispatchBoth) && c.HolmesAPIURL == "" {
		return fmt.Errorf("holmesAPIURL required when dispatchMode=%s", c.DispatchMode)
	}
	if (c.DispatchMode == DispatchSlack || c.DispatchMode == DispatchBoth) && c.SlackWebhookURL == "" {
		return fmt.Errorf("slackWebhookURL required when dispatchMode=%s", c.DispatchMode)
	}

	switch c.InvestigationMode {
	case InvestigationNone, InvestigationRunbook, InvestigationHolmes:
		// valid
	default:
		return fmt.Errorf("invalid investigationMode: %q (must be none|runbook|holmes)", c.InvestigationMode)
	}

	if c.InvestigationMode == InvestigationRunbook && c.SlackWebhookURL == "" {
		return fmt.Errorf("slackWebhookURL required when investigationMode=runbook")
	}
	if c.InvestigationMode == InvestigationHolmes {
		if c.SlackWebhookURL == "" {
			return fmt.Errorf("slackWebhookURL required when investigationMode=holmes")
		}
		if c.HolmesAPIURL == "" {
			return fmt.Errorf("holmesAPIURL required when investigationMode=holmes")
		}
	}

	return nil
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
