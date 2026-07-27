<!-- refreshed: 2026-07-26 -->
# Architecture

**Analysis Date:** 2026-07-26

## System Overview

```text
┌─────────────────────────────────────────────────────────────────────┐
│                     HTTP APIs (optional)                            │
│  Watchlist API (:8080)  `internal/watchlist/handler.go`             │
│  Status API (:8081)     `internal/investigation/status_api.go`     │
└──────┬──────────────────────────────────────────────────────────────┘
       │ PUT/DELETE triggers reconcile
       ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     Watch Manager                                   │
│  `internal/watcher/manager.go`                                      │
│  - Starts 1 ClusterWatcher per cluster (staggered 1s)              │
│  - reconcileLoop: health-check, retry, rescan, watchlist sync      │
│  - Exponential backoff retry (10s→5min cap)                        │
├─────────┬──────────┬──────────────────────┬─────────────────────────┤
│ Watcher │ Watcher  │ Watcher              │ ... (1 per cluster)    │
│ `informer.go`      │ `informer.go`        │                        │
└────┬────┴────┬─────┴──────────┬───────────┴─────────────────────────┘
     │ onUpdate (hash changed)  │
     ▼                          ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     Debouncer                                       │
│  `internal/watcher/debouncer.go`                                    │
│  Per-key timer reset; non-blocking emit to eventCh                 │
└──────────────────────────┬──────────────────────────────────────────┘
                           │ eventCh (buffered, QUEUE_MAX_SIZE=100)
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     Dispatcher (worker pool)                        │
│  `internal/dispatch/dispatcher.go`                                  │
│  Fan-out to all registered Targets                                 │
├─────────┬──────────┬───────────┬────────────┬───────────────────────┤
│ LogTarget│ SlackTarget│ AuditTarget│ InvestigationTarget│ SlackBotTarget│
│ `log.go` │ `slack.go` │`audit_target.go`│`investigation/target.go`│`slack_bot.go`│
└─────────┴──────────┴───────────┴────────────┴───────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     Investigation Orchestrator                      │
│  `internal/investigation/orchestrator.go`                           │
│  Bounded concurrency, supersede semantics per deployment            │
│  Delegates to Investigator (Runbook or Holmes)                     │
│  Reports to Slack via SlackReporter                                │
└─────────────────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     CRD Persistence (optional)                      │
│  HashStore     `internal/persistence/hash_store.go`                │
│  AuditRecorder `internal/persistence/audit_recorder.go`            │
│  WatchlistStore `internal/watchlist/store.go`                      │
└─────────────────────────────────────────────────────────────────────┘
```

## Deployment Modes

The system supports two deployment modes controlled by `DISPATCHER_SPLIT`:

**Combined mode** (default): Single `cmd/monitor` binary detects rollouts and dispatches notifications directly.

**Split mode**: Two separate binaries:
- `cmd/monitor` detects rollouts, writes `RolloutRecord` CRDs only
- `cmd/dispatcher` watches `RolloutRecord` CRDs and dispatches notifications (supports horizontal scaling via optimistic locking)

## Component Responsibilities

| Component | Responsibility | File |
|-----------|----------------|------|
| Manager | Lifecycle of all ClusterWatchers, reconcile loop (health/retry/rescan/watchlist) | `internal/watcher/manager.go` |
| ClusterWatcher | Watches Deployments on one cluster via SharedInformerFactory, detects template hash changes | `internal/watcher/informer.go` |
| NamespaceFilter | Thread-safe allow/deny namespace filter, runtime-updatable | `internal/watcher/namespace_filter.go` |
| ConfigWatcher | Watches MonitorConfig CRD to hot-reload namespace filtering | `internal/watcher/config_watcher.go` |
| Debouncer | Coalesces rapid events per deployment key with timer reset | `internal/watcher/debouncer.go` |
| Dispatcher | Worker pool consuming eventCh, fans out to Target implementations | `internal/dispatch/dispatcher.go` |
| LogTarget | Structured log output of events | `internal/dispatch/log.go` |
| SlackTarget | Sends to Slack incoming webhook | `internal/dispatch/slack.go` |
| SlackBotTarget | Sends to per-channel Slack via Bot API (chat.postMessage) | `internal/dispatch/slack_bot.go` |
| AuditTarget | Writes RolloutRecord CRDs via AuditRecorder | `internal/dispatch/audit_target.go` |
| InvestigationTarget | Delegates to Orchestrator for async investigation | `internal/investigation/target.go` |
| Orchestrator | Manages concurrent investigations with supersede semantics | `internal/investigation/orchestrator.go` |
| RunbookInvestigator | Built-in diagnostic analyzer (polls deployment/pod/event status) | `internal/investigation/runbook.go` |
| HolmesInvestigator | Delegates to external Holmes AI API | `internal/investigation/holmes.go` |
| SlackReporter | Posts investigation reports to Slack | `internal/investigation/slack_reporter.go` |
| RolloutAnalyzer | Full rollout health analysis (soak period, pod status, logs) | `internal/diagnostic/analyzer.go` |
| ClusterRegistry | Maps clusterID to kubernetes.Interface for diagnostic queries | `internal/diagnostic/registry.go` |
| HashStore | Batched CRD persistence of template hashes (flush every 5s) | `internal/persistence/hash_store.go` |
| AuditRecorder | Creates and updates RolloutRecord CRDs | `internal/persistence/audit_recorder.go` |
| WatchlistStore | In-memory index + CRD backing for dynamic app registrations | `internal/watchlist/store.go` |
| WatchlistHandler | HTTP API for GET/PUT/DELETE app watch registrations | `internal/watchlist/handler.go` |
| RecordWatcher | Watches RolloutRecord CRDs, dispatches via optimistic locking (split mode) | `internal/dispatch/record_watcher.go` |
| TTLCleaner | Deletes terminal RolloutRecords older than TTL | `internal/dispatch/ttl_cleaner.go` |
| Config | YAML config file + env var overrides + validation | `internal/config/config.go` |
| EnvConfig | Per-app environment configs and Slack routing | `internal/config/env_config.go` |
| Kubeconfig | Multi-cluster kubeconfig directory loading | `internal/config/kubeconfig.go` |

## Pattern Overview

**Overall:** Event-driven pipeline with fan-out dispatch

**Key Characteristics:**
- Read-only with respect to watched clusters (never mutates Deployments)
- Writes its own CRDs for persistence and audit when enabled
- All concurrency is goroutine-based with channels for communication
- Backpressure via non-blocking sends (drop with warning rather than block)
- Interface-based extension points (Target, Investigator, Reporter)

## Layers

**Configuration:**
- Purpose: Load and validate all settings from YAML + env vars
- Location: `internal/config/`
- Contains: Config struct, cluster loading, env config parsing, Slack routing
- Depends on: Nothing (leaf package)
- Used by: `cmd/monitor/main.go`, `cmd/dispatcher/main.go`

**Watcher (Detection):**
- Purpose: Detect deployment rollouts across multiple clusters
- Location: `internal/watcher/`
- Contains: Manager, ClusterWatcher, Debouncer, NamespaceFilter, ConfigWatcher
- Depends on: `internal/config`, `internal/models`, `internal/persistence`, `internal/watchlist`
- Used by: `cmd/monitor/main.go`

**Dispatch (Notification):**
- Purpose: Fan out events to notification targets
- Location: `internal/dispatch/`
- Contains: Dispatcher, Target implementations (Log, Slack, SlackBot, Audit), RecordWatcher, TTLCleaner
- Depends on: `internal/models`, `internal/persistence`
- Used by: `cmd/monitor/main.go`, `cmd/dispatcher/main.go`

**Investigation (Post-rollout analysis):**
- Purpose: Async investigation of rollout health with reporting
- Location: `internal/investigation/`
- Contains: Orchestrator, Investigator interface, Runbook/Holmes backends, SlackReporter, StatusCache
- Depends on: `internal/diagnostic`, `internal/models`
- Used by: `cmd/monitor/main.go` (registered as a dispatch Target)

**Diagnostic (Health analysis engine):**
- Purpose: Polls K8s API to determine rollout outcome (success/failure/degraded)
- Location: `internal/diagnostic/`
- Contains: RolloutAnalyzer, ClusterRegistry, DiagnosticReport, AsyncDiagnosticTarget (legacy)
- Depends on: `internal/models`
- Used by: `internal/investigation/`

**Persistence (CRD storage):**
- Purpose: Batched CRD-based storage for template hashes and audit records
- Location: `internal/persistence/`
- Contains: HashStore, AuditRecorder
- Depends on: `api/v1alpha1`
- Used by: `internal/watcher/`, `internal/dispatch/`, `cmd/monitor/main.go`

**Watchlist (Dynamic registration):**
- Purpose: HTTP API and CRD-backed store for runtime app watch registration
- Location: `internal/watchlist/`
- Contains: Store (in-memory index + CRD backing), Handler (HTTP API)
- Depends on: `api/v1alpha1`
- Used by: `internal/watcher/manager.go`, `cmd/monitor/main.go`

**Models (Shared types):**
- Purpose: Core data types shared across pipeline stages
- Location: `internal/models/`
- Contains: RolloutEvent struct
- Depends on: Nothing (leaf package)
- Used by: Everything

**CRD Types:**
- Purpose: Kubernetes CRD type definitions
- Location: `api/v1alpha1/`
- Contains: ClusterRolloutState, RolloutRecord, MonitorConfig, AppWatchConfig
- Depends on: k8s.io/apimachinery
- Used by: `internal/persistence/`, `internal/watchlist/`, `internal/watcher/`

## Data Flow

### Primary: Rollout Detection to Notification

1. `ClusterWatcher.onUpdate` detects template hash change (`internal/watcher/informer.go:197`)
2. Event enriched with app name and Slack channel via `eventEnricher` callback (`internal/watcher/informer.go:252`)
3. `Debouncer.Submit` starts/resets per-key timer (`internal/watcher/debouncer.go:35`)
4. After debounce window, `Debouncer.emit` does non-blocking send to `eventCh` (`internal/watcher/debouncer.go:53`)
5. `Dispatcher.worker` reads from `eventCh` (`internal/dispatch/dispatcher.go:91`)
6. `Dispatcher.DispatchEvent` iterates all Targets, calling `Target.Dispatch()` (`internal/dispatch/dispatcher.go:72`)
7. Post-dispatch callback updates RolloutRecord status if persistence enabled (`cmd/monitor/main.go:241`)

### Secondary: Split Mode (CRD-mediated dispatch)

1. Monitor writes RolloutRecord CRD via `AuditRecorder.RecordRollout` (`internal/persistence/audit_recorder.go:32`)
2. `RecordWatcher` informer detects new record with phase=Detected (`internal/dispatch/record_watcher.go:82`)
3. `claimRecord` sets phase=Processing via optimistic locking (`internal/dispatch/record_watcher.go:115`)
4. `Dispatcher.DispatchEvent` sends to all Targets synchronously (`internal/dispatch/record_watcher.go:107`)
5. `updateRecordStatus` sets final phase (Dispatched/Failed) (`internal/dispatch/record_watcher.go:160`)

### Secondary: Investigation Pipeline

1. `InvestigationTarget.Dispatch` calls `Orchestrator.Investigate` (`internal/investigation/target.go:27`)
2. Orchestrator supersedes any in-flight investigation for the same deployment (`internal/investigation/orchestrator.go:64`)
3. Acquires semaphore slot (bounded concurrency) (`internal/investigation/orchestrator.go:81`)
4. `Investigator.Investigate` runs (RunbookInvestigator or HolmesInvestigator) (`internal/investigation/orchestrator.go:123`)
5. `Reporter.PostReport` sends results to Slack (`internal/investigation/orchestrator.go:140`)

### Secondary: Reconcile Loop

1. Manager.reconcileLoop ticks every 10s (`internal/watcher/manager.go:254`)
2. Phase 0: Ensure watchlist-desired clusters have watchers (`internal/watcher/manager.go:287`)
3. Phase 1: Rescan kubeconfig directory for added/changed/removed clusters (`internal/watcher/manager.go:292`)
4. Phase 2: Health-check running watchers (consecutive errors, auth failures) (`internal/watcher/manager.go:301`)
5. Phase 3: Retry pending clusters with exponential backoff (`internal/watcher/manager.go:337`)

**State Management:**
- Template hashes: in-memory `templateCache` per ClusterWatcher, optionally persisted to `ClusterRolloutState` CRDs via batched `HashStore`
- Watchlist: in-memory index (O(1) lookup) backed by `AppWatchConfig` CRDs
- Namespace filter: in-memory `NamespaceFilter`, updated by `ConfigWatcher` from `MonitorConfig` CRD
- Investigation status: in-memory `StatusCache` (only when TRACE enabled)

## Key Abstractions

**Target Interface:**
- Purpose: Pluggable notification destination
- Examples: `internal/dispatch/log.go`, `internal/dispatch/slack.go`, `internal/dispatch/audit_target.go`, `internal/investigation/target.go`
- Pattern: Register in `cmd/monitor/main.go` based on config; Dispatcher fans out to all

```go
// internal/dispatch/dispatcher.go
type Target interface {
    Dispatch(ctx context.Context, event models.RolloutEvent) error
    Name() string
}
```

**Investigator Interface:**
- Purpose: Pluggable rollout investigation backend
- Examples: `internal/investigation/runbook.go`, `internal/investigation/holmes.go`
- Pattern: Selected by `InvestigationMode` config; wrapped by Orchestrator

```go
// internal/investigation/investigator.go
type Investigator interface {
    Investigate(ctx context.Context, event models.RolloutEvent) (*diagnostic.DiagnosticReport, error)
}
```

**HashObserver Interface:**
- Purpose: Notification sink for template hash changes (enables persistence)
- Examples: `hashObserverAdapter` in `internal/watcher/manager.go`
- Pattern: ClusterWatcher calls observer on every hash update/delete; adapter forwards to HashStore

```go
// internal/watcher/informer.go
type HashObserver interface {
    OnHashUpdate(clusterID, deployKey, hash string)
    OnHashDelete(clusterID, deployKey string)
}
```

**Reporter Interface:**
- Purpose: Pluggable investigation result output
- Examples: `internal/investigation/slack_reporter.go`

## Entry Points

**Monitor binary:**
- Location: `cmd/monitor/main.go`
- Triggers: Process start (typically via Kubernetes Deployment)
- Responsibilities: Wire all components, start watchers, dispatch events, serve HTTP APIs, handle shutdown

**Dispatcher binary:**
- Location: `cmd/dispatcher/main.go`
- Triggers: Process start (split mode deployment)
- Responsibilities: Watch RolloutRecord CRDs, dispatch to targets, TTL cleanup

**Watchlist HTTP API:**
- Location: `internal/watchlist/handler.go`
- Triggers: External HTTP requests to `:8080/api/v1/watchlist`
- Responsibilities: CRUD for app watch registrations; triggers manager reconcile on mutations

**Status API:**
- Location: `internal/investigation/status_api.go`
- Triggers: External HTTP requests to `:8081` (only when TRACE enabled + investigation mode)
- Responsibilities: Expose investigation status cache

## Architectural Constraints

- **Threading:** Single-process, goroutine-based concurrency. No multi-process coordination (except split mode via CRD optimistic locking).
- **Read-only watcher:** ClusterWatchers never mutate resources on watched clusters. Only CRDs in the persistence namespace are written.
- **Global state:** `slog.SetDefault` is set once at startup (`cmd/monitor/main.go:50`). No other module-level mutable singletons.
- **Memory bound:** Informers use `stripUnneededFields` transform to drop `managedFields` and `last-applied-configuration` annotations. Deployed memory limit is 128Mi.
- **Circular imports:** None detected. Clean dependency graph from `models` (leaf) through layers.
- **CRD namespace isolation:** All CRDs (ClusterRolloutState, RolloutRecord, AppWatchConfig) live in `PERSISTENCE_NAMESPACE` (default `rollout-monitor`).

## Anti-Patterns

### Blocking the Watcher

**What happens:** Writing synchronous HTTP calls or slow processing in informer event handlers
**Why it's wrong:** Blocks the informer's event processing goroutine, causing event queue backlog and potential missed events
**Do this instead:** Use the Debouncer to buffer events and the non-blocking channel send pattern in `internal/watcher/debouncer.go:64`

### Mutating Watched Clusters

**What happens:** Writing or patching resources on the clusters being monitored
**Why it's wrong:** The monitor is designed as a read-only observer; mutations could interfere with watched workloads and require elevated RBAC
**Do this instead:** Only write CRDs to the persistence namespace on the management cluster

## Error Handling

**Strategy:** Log-and-continue with structured slog. No panics. Failed operations are retried or dropped with warnings.

**Patterns:**
- Watcher failures: exponential backoff retry via Manager reconcile loop (10s initial, 5min cap)
- Dispatch failures: logged per-target, non-blocking; post-dispatch callback records failure status
- Persistence failures: HashStore re-queues failed updates for next flush cycle
- Channel full: non-blocking send drops event with `slog.Warn` rather than blocking
- Investigation failures: logged, investigation marked failed, does not affect other targets

## Cross-Cutting Concerns

**Logging:** `log/slog` with JSON handler set at startup. Debug level via `DEBUG=true`, trace level via `TRACE=true` using custom `trace.LevelTrace` (`internal/trace/level.go`).

**Validation:** Config validated at startup in `config.validate()` (`internal/config/config.go:258`). Invalid dispatch modes fail hard. Missing required URLs (Holmes, Slack) fail hard.

**Authentication:** No authentication on Watchlist or Status HTTP APIs. Kubernetes auth via kubeconfig files in `KUBECONFIG_DIR`. Slack auth via webhook URL or bot token.

**Graceful Shutdown:** SIGTERM/SIGINT → HTTP server shutdown (5s timeout) → Manager.Stop → close eventCh → Dispatcher.Wait → Orchestrator.Stop → context cancel (`cmd/monitor/main.go:322-351`).

---

*Architecture analysis: 2026-07-26*
