package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/dispatch"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/persistence"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/watcher"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	clusters, err := config.LoadClusters(cfg)
	if err != nil {
		slog.Error("failed to load cluster configs", "error", err)
		os.Exit(1)
	}

	slog.Info("starting rollout monitor",
		"clusters", len(clusters),
		"dispatch_mode", cfg.DispatchMode,
		"persistence", cfg.PersistenceEnabled,
		"debounce_seconds", cfg.DebounceSeconds,
		"workers", cfg.WorkerCount,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up persistence if enabled
	var store *persistence.Store
	if cfg.PersistenceEnabled {
		store, err = initPersistence(clusters, cfg.PersistenceNamespace)
		if err != nil {
			slog.Error("failed to initialize persistence", "error", err)
			os.Exit(1)
		}
		// Start batched hash flush loop (every 5s with jitter)
		go store.FlushLoop(ctx, 5*time.Second)
		slog.Info("persistence enabled", "namespace", cfg.PersistenceNamespace)
	}

	// Event channel between watchers and dispatcher
	eventCh := make(chan models.RolloutEvent, cfg.QueueMaxSize)

	// Start dispatcher
	dispatcher := dispatch.NewDispatcher(cfg, eventCh, store)
	dispatcher.Start(ctx)

	// Start cluster watch manager
	manager := watcher.NewManager(
		clusters,
		cfg.NamespaceAllowed,
		time.Duration(cfg.DebounceSeconds)*time.Second,
		eventCh,
		store,
	)

	if err := manager.Start(ctx); err != nil {
		slog.Error("failed to start watch manager", "error", err)
		os.Exit(1)
	}

	// Block on shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh

	slog.Info("shutdown signal received", "signal", sig)
	cancel()
	manager.Stop()
	close(eventCh)

	slog.Info("rollout monitor stopped")
}

func initPersistence(clusters []config.ClusterInfo, namespace string) (*persistence.Store, error) {
	// Use the first cluster's rest config to connect to the central cluster
	// (where CRDs live). For local dev this is the same cluster.
	if len(clusters) == 0 {
		return nil, nil
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	c, err := client.New(clusters[0].RestConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}

	return persistence.NewStore(c, namespace), nil
}
