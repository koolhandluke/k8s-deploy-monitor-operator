// Package main implements the rollout monitor service, which watches Kubernetes
// Deployments across one or more clusters, detects rollouts via template hash
// changes, and dispatches notifications through configured targets.
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
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/config"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/dispatch"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/investigation"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/persistence"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/trace"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/watcher"
)

// main loads configuration, initializes cluster watchers and dispatch targets,
// and runs until a SIGTERM or SIGINT signal is received.
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
	if cfg.Trace {
		logLevel = trace.LevelTrace
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
		"investigation_mode", cfg.InvestigationMode,
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
	var orchestrator *investigation.Orchestrator
	var statusCache *investigation.StatusCache

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
		// Build targets inline
		targets := []dispatch.Target{&dispatch.LogTarget{}}

		if recorder != nil {
			targets = append(targets, dispatch.NewAuditTarget(recorder))
		}

		if cfg.DispatchMode == config.DispatchSlack || cfg.DispatchMode == config.DispatchBoth {
			targets = append(targets, dispatch.NewSlackTarget(cfg.SlackWebhookURL, &http.Client{Timeout: 10 * time.Second}))
		}

		// Register investigation orchestrator if configured
		if cfg.InvestigationMode != config.InvestigationNone {
			registry := diagnostic.NewClusterRegistry(clusters)
			slackReporter := investigation.NewSlackReporter(
				cfg.SlackWebhookURL,
				&http.Client{Timeout: 30 * time.Second},
			)

			var investigator investigation.Investigator
			switch cfg.InvestigationMode {
			case config.InvestigationRunbook:
				analyzer := diagnostic.NewRolloutAnalyzer(registry, diagnostic.DefaultAnalyzerConfig())
				investigator = investigation.NewRunbookInvestigator(analyzer)
			case config.InvestigationHolmes:
				investigator = investigation.NewHolmesInvestigator(
					cfg.HolmesAPIURL,
					&http.Client{Timeout: 5 * time.Minute},
				)
			}

			if cfg.Trace {
				statusCache = investigation.NewStatusCache()
			}
			orchestrator = investigation.NewOrchestrator(investigator, slackReporter, cfg.InvestigationMaxConcurrent, statusCache)
			targets = append(targets, investigation.NewInvestigationTarget(orchestrator))
			slog.Info("investigation mode enabled",
				"mode", cfg.InvestigationMode,
				"max_concurrent", cfg.InvestigationMaxConcurrent,
			)
		} else if cfg.DiagnosticEnabled {
			// Legacy diagnostic target (backward compat)
			registry := diagnostic.NewClusterRegistry(clusters)
			analyzer := diagnostic.NewRolloutAnalyzer(registry, diagnostic.DefaultAnalyzerConfig())
			diagTarget = diagnostic.NewAsyncDiagnosticTarget(analyzer, cfg.DiagnosticMaxConcurrent)
			targets = append(targets, diagTarget)
			slog.Info("diagnostic target enabled (legacy)", "max_concurrent", cfg.DiagnosticMaxConcurrent)
		}

		dispatcher = dispatch.NewDispatcher(targets, eventCh, cfg.WorkerCount)

		// Wire persistence callback for post-dispatch status updates
		if recorder != nil {
			dispatcher.SetOnDispatched(func(ctx context.Context, event models.RolloutEvent, targetNames []string, dispatchErr string) {
				phase := v1alpha1.PhaseDispatched
				if dispatchErr != "" {
					phase = v1alpha1.PhaseFailed
				}
				recorder.UpdateRecordStatus(ctx, event, phase, targetNames, dispatchErr)
			})
		}

		dispatcher.Start(ctx)
	}

	// Start status API server if trace is enabled and investigation mode is active
	var statusServer *http.Server
	if statusCache != nil {
		handler := investigation.NewStatusHandler(statusCache)
		statusServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.StatusAPIPort),
			Handler: handler,
		}
		go func() {
			slog.Info("status API enabled", "port", cfg.StatusAPIPort)
			if err := statusServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("status API server error", "error", err)
			}
		}()
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
	if statusServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := statusServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("status API shutdown error", "error", err)
		}
	}
	manager.Stop()
	close(eventCh)
	if dispatcher != nil {
		dispatcher.Wait()
	}
	if orchestrator != nil {
		orchestrator.Stop()
	}
	if diagTarget != nil {
		diagTarget.Stop()
	}
	cancel()

	slog.Info("rollout monitor stopped")
}

// initK8sClients creates a controller-runtime client and a dynamic client using
// the REST config from the first configured cluster.
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
