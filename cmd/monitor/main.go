package main

import (
	"context"
	"fmt"
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

	clusters, err := config.LoadClusters(cfg)
	if err != nil {
		slog.Error("failed to load cluster configs", "error", err)
		os.Exit(1)
	}

	if len(clusters) == 0 {
		slog.Error("no clusters configured")
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

	// Set up persistence if enabled
	var hashStore *persistence.HashStore
	var recorder *persistence.AuditRecorder
	if cfg.PersistenceEnabled {
		c, err := initK8sClient(clusters)
		if err != nil {
			slog.Error("failed to initialize persistence client", "error", err)
			os.Exit(1)
		}

		hashStore = persistence.NewHashStore(c, cfg.PersistenceNamespace)
		recorder = persistence.NewAuditRecorder(c, cfg.PersistenceNamespace)

		// Start batched hash flush loop (every 5s with jitter)
		go hashStore.FlushLoop(ctx, 5*time.Second)
		slog.Info("persistence enabled", "namespace", cfg.PersistenceNamespace)
	}

	// Event channel between watchers and dispatcher
	eventCh := make(chan models.RolloutEvent, cfg.QueueMaxSize)

	// Start dispatcher
	dispatcher := dispatch.NewDispatcher(cfg, eventCh, recorder)
	dispatcher.Start(ctx)

	// Start cluster watch manager
	manager := watcher.NewManager(
		clusters,
		cfg.NamespaceAllowed,
		time.Duration(cfg.DebounceSeconds)*time.Second,
		eventCh,
		hashStore,
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
	manager.Stop()
	close(eventCh)
	dispatcher.Wait()
	cancel()

	slog.Info("rollout monitor stopped")
}

func initK8sClient(clusters []config.ClusterInfo) (client.Client, error) {
	// Use the first cluster's rest config to connect to the central cluster
	// (where CRDs live). For local dev this is the same cluster.
	if len(clusters) == 0 {
		return nil, fmt.Errorf("no clusters configured")
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	return client.New(clusters[0].RestConfig, client.Options{Scheme: scheme})
}
