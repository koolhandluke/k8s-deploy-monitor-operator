# Codebase Structure

**Analysis Date:** 2026-07-23

## Directory Layout

```
k8s-deploy-monitor-operator/
├── api/
│   └── v1alpha1/                    # CRD type definitions (Kubernetes API types)
│       ├── groupversion_info.go     # Scheme registration, GroupVersion
│       ├── monitor_config.go        # MonitorConfig CRD type
│       ├── types.go                 # ClusterRolloutState, RolloutRecord CRD types
│       └── zz_generated_deepcopy.go # Auto-generated DeepCopy methods
├── chart/
│   └── deploy-monitor/              # Helm chart for Kubernetes deployment
│       ├── Chart.yaml
│       ├── values.yaml
│       ├── crds/                    # CRD YAML manifests
│       │   ├── clusterrolloutstates.yaml
│       │   ├── monitorconfigs.yaml
│       │   └── rolloutrecords.yaml
│       └── templates/               # Helm templates (deployment, RBAC, etc.)
├── cmd/
│   ├── monitor/
│   │   └── main.go                  # Monitor binary entry point
│   └── dispatcher/
│       └── main.go                  # Dispatcher binary entry point (split mode)
├── deploy/
│   ├── crds.yaml                    # Raw CRD manifests (non-Helm)
│   └── deployment.yaml              # Raw K8s deployment manifest
├── docs/                            # Design docs and runbooks
│   ├── 2026-07-15-central-rollout-monitor-design.md
│   ├── architecture.md
│   ├── async-diagnostic-target.md
│   ├── deployment-rollout-monitoring-runbook.md
│   └── holmes-generic-investigation-runbook.md
├── internal/
│   ├── config/                      # Configuration loading and validation
│   │   ├── config.go                # Config struct, YAML + env loading, validation
│   │   ├── config_test.go
│   │   ├── env_config.go            # Per-app env configs, slack routing, namespace lookup
│   │   ├── env_config_test.go
│   │   ├── kubeconfig.go            # Cluster loading from KUBECONFIG_DIR
│   │   └── kubeconfig_test.go
│   ├── diagnostic/                  # Rollout health analysis (two-phase runbook)
│   │   ├── analyzer.go              # RolloutAnalyzer: monitor + gather diagnostics
│   │   ├── analyzer_test.go
│   │   ├── diagnostics_test.go
│   │   ├── fixtures_test.go
│   │   ├── monitor_test.go
│   │   ├── registry.go              # ClusterRegistry (ClientsetProvider impl)
│   │   ├── report.go                # DiagnosticReport, Result types
│   │   ├── scenarios_test.go
│   │   ├── soak_test.go
│   │   ├── target.go                # AsyncDiagnosticTarget (legacy dispatch target)
│   │   └── testdata/                # YAML fixtures for test scenarios
│   │       ├── data.go              # Fixture loader
│   │       ├── deployment-*.yaml    # Deployment state fixtures
│   │       ├── events-*.yaml        # K8s event fixtures
│   │       ├── pod-*.yaml           # Pod state fixtures
│   │       └── replicaset-*.yaml    # ReplicaSet fixtures
│   ├── dispatch/                    # Event fan-out to notification targets
│   │   ├── audit_target.go          # AuditTarget (writes RolloutRecord CRDs)
│   │   ├── dispatcher.go            # Dispatcher, Target interface, worker pool
│   │   ├── log.go                   # LogTarget (structured log output)
│   │   ├── record_watcher.go        # RecordWatcher (split-mode CRD watcher)
│   │   ├── record_watcher_test.go
│   │   ├── slack.go                 # SlackTarget (webhook)
│   │   ├── slack_test.go
│   │   ├── slack_bot.go             # SlackBotTarget (per-app channel routing)
│   │   ├── slack_bot_test.go
│   │   ├── ttl_cleaner.go           # TTLCleaner (garbage-collects old records)
│   │   └── ttl_cleaner_test.go
│   ├── investigation/               # Post-rollout investigation orchestration
│   │   ├── holmes.go                # HolmesInvestigator (external AI API)
│   │   ├── investigator.go          # Investigator interface
│   │   ├── orchestrator.go          # Orchestrator (concurrent, supersede semantics)
│   │   ├── orchestrator_test.go
│   │   ├── runbook.go               # RunbookInvestigator (wraps RolloutAnalyzer)
│   │   ├── slack_reporter.go        # SlackReporter (posts DiagnosticReport to Slack)
│   │   ├── slack_reporter_test.go
│   │   ├── status_api.go            # HTTP handler for investigation status
│   │   ├── status_cache.go          # In-memory status cache
│   │   └── target.go                # InvestigationTarget (dispatch.Target impl)
│   ├── models/
│   │   └── event.go                 # RolloutEvent struct (shared pipeline type)
│   ├── persistence/                 # CRD-based storage
│   │   ├── audit_recorder.go        # AuditRecorder (RolloutRecord CRUD)
│   │   ├── hash_store.go            # HashStore (batched ClusterRolloutState writes)
│   │   └── names.go                 # K8s name sanitization helpers
│   └── trace/
│       └── level.go                 # Custom slog trace level (-8)
├── test/
│   └── e2e.sh                       # E2E test script
├── Dockerfile                       # Container image build
├── go.mod
├── go.sum
├── CLAUDE.md                        # Project instructions for Claude Code
├── README.md
├── TESTING.md
└── PLAN.md
```

## Directory Purposes

**`api/v1alpha1/`:**
- Purpose: Kubernetes CRD type definitions for the deploy-monitor.io API group
- Contains: Go structs with kubebuilder markers, scheme registration, generated deepcopy
- Key files: `types.go` (ClusterRolloutState, RolloutRecord), `monitor_config.go` (MonitorConfig)

**`cmd/`:**
- Purpose: Binary entry points
- Contains: Two binaries -- monitor (primary) and dispatcher (split mode)
- Key files: `cmd/monitor/main.go`, `cmd/dispatcher/main.go`

**`internal/config/`:**
- Purpose: All configuration loading, parsing, validation
- Contains: YAML config file loading, env var overrides, kubeconfig directory loading, per-app env configs, slack routing
- Key files: `config.go` (Config struct + Load), `kubeconfig.go` (cluster loading), `env_config.go` (app/namespace mapping)

**`internal/watcher/`:**
- Purpose: Multi-cluster Deployment watching and rollout detection
- Contains: Manager (lifecycle), ClusterWatcher (per-cluster informer), Debouncer (event coalescing), NamespaceFilter, ConfigWatcher
- Key files: `manager.go`, `informer.go`, `debouncer.go`

**`internal/dispatch/`:**
- Purpose: Fan-out of rollout events to notification/audit targets
- Contains: Dispatcher, Target implementations (Log, Slack, SlackBot, Audit), RecordWatcher (split mode), TTLCleaner
- Key files: `dispatcher.go` (Target interface + worker pool), `slack.go`, `record_watcher.go`

**`internal/investigation/`:**
- Purpose: Post-rollout investigation with concurrent orchestration
- Contains: Orchestrator, Investigator interface, RunbookInvestigator, HolmesInvestigator, SlackReporter, StatusAPI
- Key files: `orchestrator.go`, `investigator.go`, `holmes.go`

**`internal/diagnostic/`:**
- Purpose: Two-phase rollout health analysis engine
- Contains: RolloutAnalyzer (Phase 1: monitor convergence, Phase 2: gather diagnostics), DiagnosticReport, ClusterRegistry
- Key files: `analyzer.go` (727 lines, core analysis logic), `report.go` (result types)

**`internal/persistence/`:**
- Purpose: CRD-based state storage
- Contains: HashStore (batched template hash persistence), AuditRecorder (RolloutRecord CRUD)
- Key files: `hash_store.go`, `audit_recorder.go`

**`internal/models/`:**
- Purpose: Shared data types used across the pipeline
- Contains: RolloutEvent struct (the primary data flowing through the system)
- Key files: `event.go`

**`internal/trace/`:**
- Purpose: Custom slog trace level for verbose diagnostics
- Contains: Single file defining trace level constant
- Key files: `level.go`

**`chart/deploy-monitor/`:**
- Purpose: Helm chart for production deployment
- Contains: CRD manifests, Deployment templates for both monitor and dispatcher, RBAC
- Key files: `values.yaml`, `templates/deployment.yaml`, `templates/deployment-dispatcher.yaml`

**`deploy/`:**
- Purpose: Raw Kubernetes manifests (non-Helm alternative)
- Contains: CRD definitions, Deployment manifest
- Key files: `crds.yaml`, `deployment.yaml`

**`docs/`:**
- Purpose: Design documents and operational runbooks
- Contains: Architecture docs, design decisions, monitoring runbooks

## Key File Locations

**Entry Points:**
- `cmd/monitor/main.go`: Monitor binary -- loads config, wires pipeline, runs until signal
- `cmd/dispatcher/main.go`: Dispatcher binary -- watches RolloutRecord CRDs, dispatches events

**Configuration:**
- `internal/config/config.go`: Config struct definition, YAML loading, env var overrides, validation
- `internal/config/kubeconfig.go`: Cluster loading from KUBECONFIG_DIR directory
- `internal/config/env_config.go`: Per-app env configs, slack routing, namespace lookup tables

**Core Logic:**
- `internal/watcher/informer.go`: ClusterWatcher -- rollout detection via template hash comparison
- `internal/watcher/manager.go`: Manager -- multi-cluster lifecycle, health checks, retry backoff
- `internal/watcher/debouncer.go`: Debouncer -- event coalescing with non-blocking emit
- `internal/dispatch/dispatcher.go`: Dispatcher + Target interface -- fan-out worker pool
- `internal/diagnostic/analyzer.go`: RolloutAnalyzer -- two-phase rollout health analysis
- `internal/investigation/orchestrator.go`: Orchestrator -- concurrent investigation with supersede

**CRD Types:**
- `api/v1alpha1/types.go`: ClusterRolloutState, RolloutRecord
- `api/v1alpha1/monitor_config.go`: MonitorConfig

**Persistence:**
- `internal/persistence/hash_store.go`: Batched hash writes to ClusterRolloutState CRDs
- `internal/persistence/audit_recorder.go`: RolloutRecord CRD CRUD

**Testing:**
- `internal/watcher/debouncer_test.go`, `internal/watcher/informer_test.go`, `internal/watcher/manager_test.go`
- `internal/diagnostic/analyzer_test.go`, `internal/diagnostic/scenarios_test.go`, `internal/diagnostic/soak_test.go`
- `internal/dispatch/slack_test.go`, `internal/dispatch/slack_bot_test.go`, `internal/dispatch/record_watcher_test.go`
- `internal/investigation/orchestrator_test.go`, `internal/investigation/slack_reporter_test.go`
- `internal/diagnostic/testdata/`: YAML fixtures for test scenarios

## Naming Conventions

**Files:**
- snake_case for Go source files: `hash_store.go`, `config_watcher.go`, `record_watcher.go`
- Test files use `_test.go` suffix co-located with source: `debouncer_test.go`
- Generated files prefixed with `zz_generated_`: `zz_generated_deepcopy.go`

**Directories:**
- All lowercase, single word or hyphenated: `v1alpha1`, `testdata`, `deploy-monitor`
- Standard Go layout: `cmd/`, `internal/`, `api/`

**Go Types:**
- PascalCase exported types: `ClusterWatcher`, `RolloutEvent`, `DiagnosticReport`
- Interface names describe capability: `Target`, `Investigator`, `HashObserver`, `ClientsetProvider`
- CRD types match Kubernetes conventions: `ClusterRolloutState`, `RolloutRecord`, `MonitorConfig`

**Constants:**
- PascalCase with category prefix: `DispatchLog`, `DispatchSlack`, `ResultSuccess`, `ResultFailed`
- Phase constants: `PhaseDetected`, `PhaseProcessing`, `PhaseDispatched`, `PhaseFailed`

## Where to Add New Code

**New Dispatch Target:**
1. Create `internal/dispatch/my_target.go` implementing the `Target` interface
2. Implement `Dispatch(ctx context.Context, event models.RolloutEvent) error` and `Name() string`
3. Register in `cmd/monitor/main.go` by appending to `targets` slice (around line 153)
4. Add tests in `internal/dispatch/my_target_test.go`

**New Investigation Backend:**
1. Create `internal/investigation/my_backend.go` implementing `Investigator` interface
2. Implement `Investigate(ctx context.Context, event models.RolloutEvent) (*diagnostic.DiagnosticReport, error)`
3. Add a new `InvestigationMode` constant in `internal/config/config.go`
4. Wire into the switch statement in `cmd/monitor/main.go` (around line 177)

**New CRD Type:**
1. Add type definitions in `api/v1alpha1/`
2. Run deepcopy generation
3. Register in scheme (`api/v1alpha1/groupversion_info.go`)
4. Add CRD YAML to `chart/deploy-monitor/crds/` and `deploy/crds.yaml`

**New Configuration Field:**
1. Add field to `Config` struct in `internal/config/config.go` with `yaml` tag
2. Add env var override in `applyEnvOverrides()` or `loadFromEnvVars()`
3. Add default in `applyDefaults()`
4. Add validation in `validate()` if needed
5. Wire in `cmd/monitor/main.go` or `cmd/dispatcher/main.go`

**New Diagnostic Check:**
- Add logic to `internal/diagnostic/analyzer.go` in `checkFailureConditions()` or `gatherDiagnostics()`
- Add test fixtures in `internal/diagnostic/testdata/`
- Add test scenarios in `internal/diagnostic/scenarios_test.go`

**Utility Functions:**
- Models/shared types: `internal/models/`
- Kubernetes name helpers: `internal/persistence/names.go`
- Log levels: `internal/trace/level.go`

## Special Directories

**`internal/diagnostic/testdata/`:**
- Purpose: YAML fixtures representing Kubernetes resource states for test scenarios
- Generated: No (hand-written)
- Committed: Yes

**`chart/deploy-monitor/crds/`:**
- Purpose: CRD YAML manifests installed by Helm
- Generated: No (hand-written, must match `api/v1alpha1/` types)
- Committed: Yes

**`api/v1alpha1/zz_generated_deepcopy.go`:**
- Purpose: Auto-generated DeepCopy methods for CRD types
- Generated: Yes (by controller-gen)
- Committed: Yes

---

*Structure analysis: 2026-07-23*
