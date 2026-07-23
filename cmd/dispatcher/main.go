// Package main implements the standalone rollout dispatcher service, which watches
// RolloutRecord CRDs and dispatches notifications to configured targets independently
// of the monitor process.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/dispatch"
)

// main loads configuration, initializes Kubernetes clients and dispatch targets,
// starts the record watcher and TTL cleaner, and runs until a shutdown signal is received.
func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logLevel := slog.LevelInfo
	if cfg.Debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	slog.Info("starting rollout dispatcher",
		"dispatch_mode", cfg.DispatchMode,
		"persistence_namespace", cfg.PersistenceNamespace,
		"ttl_days", cfg.RolloutRecordTTLDays,
	)

	// Initialize K8s clients
	restCfg, err := getRestConfig(cfg)
	if err != nil {
		slog.Error("failed to get kubernetes config", "error", err)
		os.Exit(1)
	}

	_, dynClient, err := initClients(restCfg)
	if err != nil {
		slog.Error("failed to initialize kubernetes clients", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Build targets inline
	targets := []dispatch.Target{&dispatch.LogTarget{}}

	if cfg.DispatchMode == config.DispatchSlack || cfg.DispatchMode == config.DispatchBoth {
		targets = append(targets, dispatch.NewSlackTarget(cfg.SlackWebhookURL, &http.Client{Timeout: 10 * time.Second}))
	}

	// Create standalone dispatcher (no channel, synchronous dispatch)
	dispatcher := dispatch.NewStandaloneDispatcher(targets)

	// Create record watcher
	namespace := cfg.PersistenceNamespace
	if namespace == "" {
		namespace = "rollout-monitor"
	}

	recordWatcher := dispatch.NewRecordWatcher(dynClient, dispatcher, namespace)

	// Create TTL cleaner
	ttlCleaner := dispatch.NewTTLCleaner(dynClient, namespace, cfg.RolloutRecordTTLDays)

	// Start components
	go recordWatcher.Start(ctx)
	go ttlCleaner.Start(ctx)

	// Block on shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh

	slog.Info("shutdown signal received", "signal", sig)
	recordWatcher.Stop()
	ttlCleaner.Stop()
	cancel()

	slog.Info("rollout dispatcher stopped")
}

// getRestConfig returns a Kubernetes REST config, trying in-cluster config first
// and falling back to the KUBECONFIG env var or the default kubeconfig path.
func getRestConfig(_ *config.Config) (*rest.Config, error) {
	// Try in-cluster first
	restCfg, err := rest.InClusterConfig()
	if err == nil {
		return restCfg, nil
	}

	// Fall back to KUBECONFIG for local dev
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		home, _ := os.UserHomeDir()
		kubeconfigPath = home + "/.kube/config"
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}

// initClients creates a controller-runtime client and a dynamic client from
// the given REST config, registering the CRD scheme types.
func initClients(restCfg *rest.Config) (client.Client, dynamic.Interface, error) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("creating controller-runtime client: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	return c, dynClient, nil
}
