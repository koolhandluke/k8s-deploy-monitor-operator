package config

import (
	"os"
	"testing"
)

func TestNamespaceAllowed_Allowlist(t *testing.T) {
	cfg := &Config{NamespaceAllowlist: []string{"default", "production"}}

	if !cfg.NamespaceAllowed("default") {
		t.Error("expected default to be allowed")
	}
	if !cfg.NamespaceAllowed("production") {
		t.Error("expected production to be allowed")
	}
	if cfg.NamespaceAllowed("kube-system") {
		t.Error("expected kube-system to be denied")
	}
}

func TestNamespaceAllowed_Denylist(t *testing.T) {
	cfg := &Config{NamespaceDenylist: []string{"kube-system", "kube-public"}}

	if cfg.NamespaceAllowed("kube-system") {
		t.Error("expected kube-system to be denied")
	}
	if !cfg.NamespaceAllowed("default") {
		t.Error("expected default to be allowed")
	}
}

func TestNamespaceAllowed_AllowlistTakesPrecedence(t *testing.T) {
	cfg := &Config{
		NamespaceAllowlist: []string{"production"},
		NamespaceDenylist:  []string{"staging"},
	}

	// Allowlist takes precedence — only "production" is allowed
	if !cfg.NamespaceAllowed("production") {
		t.Error("expected production to be allowed")
	}
	if cfg.NamespaceAllowed("staging") {
		t.Error("expected staging to be denied (not in allowlist)")
	}
	if cfg.NamespaceAllowed("default") {
		t.Error("expected default to be denied (not in allowlist)")
	}
}

func TestLoad_Defaults(t *testing.T) {
	// Clear relevant env vars
	for _, key := range []string{"KUBECONFIG_DIR", "KUBECONFIG", "DISPATCH_MODE",
		"HOLMES_API_URL", "SLACK_WEBHOOK_URL", "NAMESPACE_ALLOWLIST", "NAMESPACE_DENYLIST",
		"WORKER_COUNT", "DEBOUNCE_SECONDS", "QUEUE_MAX_SIZE"} {
		os.Unsetenv(key)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.DispatchMode != DispatchLog {
		t.Errorf("expected log dispatch mode, got %s", cfg.DispatchMode)
	}
	if cfg.WorkerCount != 3 {
		t.Errorf("expected 3 workers, got %d", cfg.WorkerCount)
	}
	if cfg.DebounceSeconds != 30 {
		t.Errorf("expected 30s debounce, got %d", cfg.DebounceSeconds)
	}
	if len(cfg.NamespaceDenylist) == 0 {
		t.Error("expected default denylist to be set")
	}
}

func TestLoad_HolmesRequiresURL(t *testing.T) {
	os.Setenv("DISPATCH_MODE", "holmes")
	os.Unsetenv("HOLMES_API_URL")
	defer os.Unsetenv("DISPATCH_MODE")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when HOLMES_API_URL is missing")
	}
}

func TestLoad_InvalidMode(t *testing.T) {
	os.Setenv("DISPATCH_MODE", "invalid")
	defer os.Unsetenv("DISPATCH_MODE")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid dispatch mode")
	}
}
