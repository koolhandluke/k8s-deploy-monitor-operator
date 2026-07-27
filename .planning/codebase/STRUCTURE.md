# Codebase Structure

**Analysis Date:** 2026-07-26

## Directory Layout

```
k8s-deploy-monitor-operator/
├── api/
│   └── v1alpha1/                  # CRD type definitions (kubebuilder-style)
├── chart/
│   └── deploy-monitor/            # Helm chart for Kubernetes deployment
│       ├── crds/                   # CRD YAML manifests
│       └── templates/              # Helm templates (Deployments, RBAC, etc.)
├── cmd/
│   ├── monitor/                   # Main monitor binary entry point
│   └── dispatcher/                # Standalone dispatcher binary entry point
├── deploy/                        # Raw Kubernetes manifests (non-Helm)
├── docs/                          # Design docs, runbooks, API docs
│   └── superpowers/
│       └── specs/                 # Feature specification documents
├── internal/
│   ├── config/                    # Configuration loading (YAML + env vars + clusters)
│   ├── diagnostic/                # Rollout health analysis engine
│   │   └── testdata/              # YAML fixtures for diagnostic tests
│   ├── dispatch/                  # Event dispatch (targets, worker pool, CRD watcher)
│   ├── investigation/             # Post-rollout investigation orchestration
│   ├── models/                    # Shared data types (RolloutEvent)
│   ├── persistence/               # CRD-based hash and audit storage
│   ├── trace/                     # Custom slog trace level
│   ├── watcher/                   # Cluster watchers, manager, debouncer, filters
│   └── watchlist/                 # Dynamic app registration store + HTTP API
├── test/                          # E2E test scripts
├── Dockerfile                     # Container image build
├── go.mod                         # Go module definition
├── go.sum                         # Dependency checksums
├── CLAUDE.md                      # AI assistant instructions
├── PLAN.md                        # Development plan
├── README.md                      # Project readme
└── TESTING.md                     # Testing guide
```

## Directory Purposes

**`api/v1alpha1/`:**
- Purpose: Kubernetes CRD type definitions following kubebuilder conventions
- Contains: Go structs with kubebuilder markers, deepcopy generated code, scheme registration
- Key files:
  - `types.go`: ClusterRolloutState, RolloutRecord CRDs
  - `monitor_config.go`: MonitorConfig CRD
  - `app_watch_config.go`: AppWatchConfig CRD
  - `groupversion_info.go`: Scheme registration (group `deploy-monitor.io`, version `v1alpha1`)
  - `zz_generated_deepcopy.go`: Auto-generated DeepCopy methods

**`cmd/monitor/`:**
- Purpose: Main binary that wires all components together
- Contains: Single `main.go` file (379 lines)
- Key responsibilities: Config loading, component initialization, wiring, signal handling, shutdown

**`cmd/dispatcher/`:**
- Purpose: Standalone dispatcher binary for split-mode deployments
- Contains: Single `main.go` file (141 lines)
- Key responsibilities: Watch RolloutRecord CRDs, dispatch events, TTL cleanup

**`internal/config/`:**
- Purpose: All configuration loading and validation
- Contains: YAML config, env var loading, cluster config, per-app env configs, Slack routing
- Key files:
  - `config.go`: Main Config struct, Load(), validation, defaults
  - `kubeconfig.go`: ClusterInfo, directory loading, snapshot with file hashes
  - `env_config.go`: Per-app environment configs and namespace lookup
  - `config_test.go`, `kubeconfig_test.go`, `env_config_test.go`: Tests

**`internal/watcher/`:**
- Purpose: Core rollout detection engine
- Contains: Manager (multi-cluster lifecycle), ClusterWatcher (per-cluster informer), Debouncer, NamespaceFilter, ConfigWatcher
- Key files:
  - `manager.go`: Manager with reconcile loop (health/retry/rescan/watchlist)
  - `informer.go`: ClusterWatcher with SharedInformerFactory and template hash detection
  - `debouncer.go`: Per-key event coalescing
  - `namespace_filter.go`: Thread-safe allow/deny filter
  - `config_watcher.go`: Watches MonitorConfig CRD for runtime namespace config
  - `manager_test.go`, `informer_test.go`, `debouncer_test.go`: Tests

**`internal/dispatch/`:**
- Purpose: Event notification fan-out
- Contains: Dispatcher (worker pool), Target implementations, RecordWatcher (split mode), TTLCleaner
- Key files:
  - `dispatcher.go`: Dispatcher struct, Target interface, worker loop
  - `log.go`: LogTarget (structured log output)
  - `slack.go`: SlackTarget (incoming webhook)
  - `slack_bot.go`: SlackBotTarget (Bot API per-channel routing)
  - `audit_target.go`: AuditTarget (writes RolloutRecord CRDs)
  - `record_watcher.go`: RecordWatcher (split mode CRD consumer with optimistic locking)
  - `ttl_cleaner.go`: TTLCleaner (deletes old terminal records)

**`internal/investigation/`:**
- Purpose: Post-rollout investigation with multiple backends
- Contains: Orchestrator (concurrency + supersede), Investigator interface + impls, SlackReporter, StatusCache
- Key files:
  - `orchestrator.go`: Orchestrator with bounded concurrency and supersede semantics
  - `investigator.go`: Investigator interface definition
  - `runbook.go`: RunbookInvestigator (wraps diagnostic.RolloutAnalyzer)
  - `holmes.go`: HolmesInvestigator (external AI API)
  - `slack_reporter.go`: Posts DiagnosticReport to Slack
  - `target.go`: InvestigationTarget (dispatch.Target adapter)
  - `status_api.go`: HTTP handler for investigation status
  - `status_cache.go`: In-memory investigation status cache

**`internal/diagnostic/`:**
- Purpose: Rollout health analysis engine (polls K8s API)
- Contains: RolloutAnalyzer (soak, poll, detect failures), ClusterRegistry, DiagnosticReport, AsyncDiagnosticTarget (legacy)
- Key files:
  - `analyzer.go`: RolloutAnalyzer with configurable timeouts and polling
  - `registry.go`: ClusterRegistry (maps clusterID to kubernetes.Interface)
  - `report.go`: DiagnosticReport struct with result types
  - `target.go`: AsyncDiagnosticTarget (legacy dispatch.Target, replaced by investigation)
  - `testdata/`: YAML fixtures for deployment/pod/event test scenarios

**`internal/models/`:**
- Purpose: Shared data types
- Contains: Single `event.go` file with RolloutEvent struct
- Key: Leaf package with no internal dependencies

**`internal/persistence/`:**
- Purpose: CRD-based storage
- Contains: HashStore (batched template hash persistence), AuditRecorder (RolloutRecord CRUD)
- Key files:
  - `hash_store.go`: Batched flush loop, upsert ClusterRolloutState CRDs
  - `audit_recorder.go`: Create/update RolloutRecord CRDs
  - `names.go`: K8s resource name sanitization

**`internal/watchlist/`:**
- Purpose: Dynamic app watch registration
- Contains: Store (in-memory index + CRD backing), Handler (HTTP API)
- Key files:
  - `store.go`: Store with O(1) lookup, conflict detection, cluster validation
  - `handler.go`: HTTP handler for GET/PUT/DELETE `/api/v1/watchlist`

**`internal/trace/`:**
- Purpose: Custom slog trace level
- Contains: Single `level.go` file defining `LevelTrace` constant

**`chart/deploy-monitor/`:**
- Purpose: Helm chart for production deployment
- Contains: CRD manifests, Deployment templates, RBAC, ServiceAccounts
- Key files:
  - `values.yaml`: Default Helm values
  - `crds/`: CRD YAML manifests (clusterrolloutstates, monitorconfigs, rolloutrecords)
  - `templates/deployment.yaml`: Monitor Deployment
  - `templates/deployment-dispatcher.yaml`: Dispatcher Deployment (split mode)

**`deploy/`:**
- Purpose: Raw Kubernetes manifests (alternative to Helm)
- Contains: `deployment.yaml` (Deployment, SA, ClusterRole, ClusterRoleBinding), `crds.yaml`

## Key File Locations

**Entry Points:**
- `cmd/monitor/main.go`: Monitor binary (combined or split-mode monitor)
- `cmd/dispatcher/main.go`: Dispatcher binary (split-mode dispatcher)

**Configuration:**
- `internal/config/config.go`: Config struct with all settings, YAML + env var loading
- `internal/config/kubeconfig.go`: Multi-cluster kubeconfig loading
- `/etc/rollout-monitor/config.yaml`: Default config file path (in container)

**Core Logic:**
- `internal/watcher/informer.go`: Rollout detection (template hash comparison)
- `internal/watcher/manager.go`: Multi-cluster watcher lifecycle
- `internal/dispatch/dispatcher.go`: Event fan-out with Target interface

**CRD Definitions:**
- `api/v1alpha1/types.go`: ClusterRolloutState, RolloutRecord
- `api/v1alpha1/monitor_config.go`: MonitorConfig
- `api/v1alpha1/app_watch_config.go`: AppWatchConfig

**Testing:**
- `internal/watcher/*_test.go`: Watcher/manager/debouncer tests
- `internal/dispatch/*_test.go`: Slack, SlackBot, RecordWatcher, TTLCleaner tests
- `internal/config/*_test.go`: Config and kubeconfig tests
- `internal/diagnostic/*_test.go`: Analyzer, scenarios, soak tests
- `internal/watchlist/*_test.go`: Store and handler tests
- `internal/investigation/*_test.go`: Orchestrator and SlackReporter tests
- `internal/diagnostic/testdata/`: YAML fixtures for test scenarios

## Naming Conventions

**Files:**
- Snake_case: `hash_store.go`, `audit_recorder.go`, `config_watcher.go`
- Test files: `*_test.go` co-located with source (e.g., `manager_test.go`)
- Generated: `zz_generated_deepcopy.go` (kubebuilder convention)

**Directories:**
- Lowercase single words: `config`, `watcher`, `dispatch`, `models`
- Multi-word lowercase: `watchlist`, `diagnostic`, `investigation`, `persistence`

**Go packages:**
- Package name matches directory name
- Internal packages under `internal/` (not importable outside module)
- CRD types under `api/v1alpha1/` (importable, follows kubebuilder layout)

**Types:**
- PascalCase exported types: `Manager`, `ClusterWatcher`, `Dispatcher`, `HashStore`
- Interface suffix convention: `Target`, `Investigator`, `Reporter`, `HashObserver`
- CRD types match K8s naming: `ClusterRolloutState`, `RolloutRecord`, `MonitorConfig`

**Functions:**
- PascalCase exported: `NewManager()`, `LoadClusters()`, `Start()`, `Stop()`
- camelCase unexported: `reconcileLoop()`, `startWatcherLocked()`, `templateHash()`
- Constructor pattern: `NewXxx(deps...) *Xxx`

**Constants:**
- PascalCase exported: `DispatchSlack`, `PhaseDetected`, `InvestigationRunbook`
- Type-based enums: `DispatchMode`, `RolloutPhase`, `InvestigationMode`

## Where to Add New Code

**New Dispatch Target:**
1. Create `internal/dispatch/your_target.go` implementing `Target` interface
2. Add tests in `internal/dispatch/your_target_test.go`
3. Register in `cmd/monitor/main.go` based on config condition (see `SlackTarget` registration pattern at line 190)

**New Investigation Backend:**
1. Create `internal/investigation/your_investigator.go` implementing `Investigator` interface
2. Add a new `InvestigationMode` constant in `internal/config/config.go`
3. Add switch case in `cmd/monitor/main.go` (see line 208)

**New CRD Type:**
1. Add type definition in `api/v1alpha1/your_type.go` with kubebuilder markers
2. Register in `api/v1alpha1/groupversion_info.go` SchemeBuilder
3. Run deepcopy generation (update `zz_generated_deepcopy.go`)
4. Add CRD YAML to `chart/deploy-monitor/crds/` and `deploy/crds.yaml`

**New Config Option:**
1. Add field to `Config` struct in `internal/config/config.go` with `yaml` tag
2. Add env var override in `applyEnvOverrides()` or `loadFromEnvVars()`
3. Add default in `applyDefaults()`
4. Add validation in `validate()` if needed
5. Add tests in `internal/config/config_test.go`

**New HTTP API Endpoint:**
- Watchlist API: Add handler in `internal/watchlist/handler.go` (see existing pattern)
- Status API: Add handler in `internal/investigation/status_api.go`
- New API: Create new package under `internal/`, register HTTP server in `cmd/monitor/main.go`

**New Internal Package:**
1. Create `internal/your_package/`
2. Follow existing patterns: constructor `NewXxx()`, `Start(ctx)`, `Stop()` lifecycle
3. Wire in `cmd/monitor/main.go`

## Special Directories

**`internal/diagnostic/testdata/`:**
- Purpose: YAML fixtures representing K8s resources in various states (crashloop, OOM, image pull backoff, etc.)
- Generated: No (hand-crafted test fixtures)
- Committed: Yes

**`chart/deploy-monitor/crds/`:**
- Purpose: CRD YAML manifests installed before chart templates
- Generated: No (manually maintained, should match `api/v1alpha1/` types)
- Committed: Yes

**`.planning/`:**
- Purpose: GSD planning and codebase analysis documents
- Generated: By tooling
- Committed: Yes

---

*Structure analysis: 2026-07-26*
