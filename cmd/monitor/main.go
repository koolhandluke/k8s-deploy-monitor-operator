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
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
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
		"diagnostic", cfg.DiagnosticEnabled,
		"debounce_seconds", cfg.DebounceSeconds,
		"workers", cfg.WorkerCount,
		"rescan_interval_seconds", cfg.RescanIntervalSeconds,
	)

	ctx, cancel := context.WithCancel(context.Background())

	// Create NamespaceFilter seeded from env vars (backwards compatible)
	nsFilter := watcher.NewNamespaceFilter(cfg.NamespaceAllowlist, cfg.NamespaceDenylist)

	// Set up persistence if enabled
	var hashStore *persistence.HashStore
	var recorder *persistence.AuditRecorder
	if cfg.PersistenceEnabled {
		c, dynClient, err := initK8sClients(clusters)
		if err != nil {
			slog.Error("failed to initialize persistence client", "error", err)
			os.Exit(1)
		}

		hashStore = persistence.NewHashStore(c, cfg.PersistenceNamespace)
		recorder = persistence.NewAuditRecorder(c, cfg.PersistenceNamespace)

		// Start batched hash flush loop (every 5s with jitter)
		go hashStore.FlushLoop(ctx, 5*time.Second)
		slog.Info("persistence enabled", "namespace", cfg.PersistenceNamespace)

		// Start ConfigWatcher for runtime-reloadable namespace filtering
		configWatcher := watcher.NewConfigWatcher(nsFilter, c, dynClient)
		go configWatcher.Start(ctx)
		defer configWatcher.Stop()
	}

	// Event channel between watchers and dispatcher/recorder
	eventCh := make(chan models.RolloutEvent, cfg.QueueMaxSize)

	var dispatcher *dispatch.Dispatcher
	var diagTarget *diagnostic.AsyncDiagnosticTarget

	if cfg.DispatcherSplit {
		// Split mode: monitor only writes CRDs, dispatcher service handles dispatch
		if recorder == nil {
			slog.Error("DISPATCHER_SPLIT=true requires PERSISTENCE_ENABLED=true")
			os.Exit(1)
		}
		go func() {
			for event := range eventCh {
				if err := recorder.RecordRollout(ctx, event); err != nil {
					slog.Error("failed to write rollout record", "error", err)
				}
			}
		}()
		slog.Info("running in split mode: writing CRDs only, no local dispatch")
	} else {
		// Combined mode: monitor dispatches events directly (original behavior)
		dispatcher = dispatch.NewDispatcher(cfg, eventCh, recorder)

		// Register async diagnostic target if enabled
		if cfg.DiagnosticEnabled {
			registry := diagnostic.NewClusterRegistry(clusters)
			analyzer := diagnostic.NewRolloutAnalyzer(registry)
			diagTarget = diagnostic.NewAsyncDiagnosticTarget(analyzer, cfg.DiagnosticMaxConcurrent)
			dispatcher.AddTarget(diagTarget)
			slog.Info("diagnostic target enabled", "max_concurrent", cfg.DiagnosticMaxConcurrent)
		}

		dispatcher.Start(ctx)
	}

	// Start cluster watch manager
	manager := watcher.NewManager(
		nsFilter.Allowed,
		time.Duration(cfg.DebounceSeconds)*time.Second,
		eventCh,
		hashStore,
		cfg.KubeconfigDir,
		time.Duration(cfg.RescanIntervalSeconds)*time.Second,
	)

	if err := manager.Start(ctx, clusters); err != nil {
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
	if dispatcher != nil {
		dispatcher.Wait()
	}
	if diagTarget != nil {
		diagTarget.Stop()
	}
	cancel()

	slog.Info("rollout monitor stopped")
}

func initK8sClients(clusters []config.ClusterInfo) (client.Client, dynamic.Interface, error) {
	if len(clusters) == 0 {
		return nil, nil, fmt.Errorf("no clusters configured")
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	c, err := client.New(clusters[0].RestConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("creating controller-runtime client: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(clusters[0].RestConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	return c, dynClient, nil
}
