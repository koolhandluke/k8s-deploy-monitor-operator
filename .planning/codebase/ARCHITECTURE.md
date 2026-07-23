# Architecture

<!-- refreshed: 2026-07-23 -->
**Analysis Date:** 2026-07-23

## System Overview

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Two Binary Architecture                            │
│                                                                             │
│  cmd/monitor/main.go (primary)           cmd/dispatcher/main.go (split)     │
│  ┌──────────────────────────────┐        ┌──────────────────────────┐       │
│  │ Watches K8s Deployments      │        │ Watches RolloutRecord    │       │
│  │ Detects rollouts via hash    │  CRDs  │ CRDs and dispatches      │       │
│  │ Dispatches OR writes CRDs ───┼───────►│ notifications            │       │
│  └──────────────────────────────┘        └──────────────────────────┘       │
└─────────────────────────────────────────────────────────────────────────────┘

Monitor Pipeline (combined mode):

  kubeconfigs/     NamespaceFilter    Debouncer     eventCh       Dispatcher
  KUBECONFIG_DIR ──► Manager ──► ClusterWatcher ──► (30s) ──► (buffered) ──► Targets
                      │              │                                        │
                  reconcileLoop  templateCache                          ┌─────┼─────┐
                  (health+retry)  (SHA256 map)                         │     │     │
                      │              │                                 Log  Slack  Inv
                      │         HashObserver                                      │
                      │              │                                    Orchestrator
                      ▼              ▼                                       │
                ConfigWatcher   HashStore (CRD)                        Investigator
                ← MonitorConfig   flush every 5s                       │         │
                                     │                             Runbook    Holmes
                                     ▼                               │         │
                            ClusterRolloutState CRD              DiagnosticReport
                                                                     │
                                                               SlackReporter
                                                                     │
                               AuditRecorder ──► RolloutRecord CRD   │
                                                                     ▼
                                                              Slack Webhook
```

## Component Responsibilities

| Component | Responsibility | File |
|-----------|----------------|------|
| Manager | Lifecycle of all ClusterWatchers; staggered startup, health monitoring, retry with backoff, kubeconfig rescan | `internal/watcher/manager.go` |
| ClusterWatcher | Watches Deployments on one cluster via SharedInformerFactory; detects rollouts by SHA256 hash of spec.template | `internal/watcher/informer.go` |
| Debouncer | Coalesces rapid events per deployment key; only latest event survives the window | `internal/watcher/debouncer.go` |
| NamespaceFilter | Thread-safe allow/deny filter; runtime-updatable via MonitorConfig CRD | `internal/watcher/namespace_filter.go` |
| ConfigWatcher | Watches MonitorConfig CRD "default" and hot-reloads NamespaceFilter | `internal/watcher/config_watcher.go` |
| Dispatcher | Fan-out worker pool consuming eventCh; dispatches to registered Targets | `internal/dispatch/dispatcher.go` |
| SlackTarget | Posts rollout notification to Slack incoming webhook | `internal/dispatch/slack.go` |
| SlackBotTarget | Posts rollout notification to per-app Slack channels via Bot API | `internal/dispatch/slack_bot.go` |
| LogTarget | Structured log output of events | `internal/dispatch/log.go` |
| AuditTarget | Writes RolloutRecord CRDs via AuditRecorder | `internal/dispatch/audit_target.go` |
| InvestigationTarget | Delegates to Orchestrator for async post-rollout investigation | `internal/investigation/target.go` |
| Orchestrator | Manages concurrent investigations with supersede semantics and bounded concurrency | `internal/investigation/orchestrator.go` |
| RunbookInvestigator | Built-in two-phase rollout analyzer (monitor + diagnose) | `internal/investigation/runbook.go` wrapping `internal/diagnostic/analyzer.go` |
| HolmesInvestigator | Delegates investigation to external Holmes AI API | `internal/investigation/holmes.go` |
| SlackReporter | Posts DiagnosticReport results to Slack | `internal/investigation/slack_reporter.go` |
| RolloutAnalyzer | Two-phase runbook: Phase 1 monitors replica convergence + soak; Phase 2 gathers diagnostics | `internal/diagnostic/analyzer.go` |
| HashStore | Batched persistence of template hashes to ClusterRolloutState CRDs | `internal/persistence/hash_store.go` |
| AuditRecorder | Creates/updates RolloutRecord CRDs for audit trail | `internal/persistence/audit_recorder.go` |
| RecordWatcher | Split-mode: watches RolloutRecord CRDs and dispatches via optimistic locking | `internal/dispatch/record_watcher.go` |
| TTLCleaner | Garbage-collects old RolloutRecord CRDs beyond TTL | `internal/dispatch/ttl_cleaner.go` |
| StatusCache + StatusHandler | In-memory cache + HTTP API for investigation status (trace mode only) | `internal/investigation/status_cache.go`, `internal/investigation/status_api.go` |

## Pattern Overview

**Overall:** Event-driven pipeline with fan-out dispatch

**Key Characteristics:**
- Read-only with respect to watched clusters -- never mutates Deployments
- One watcher goroutine per cluster, all feeding a shared eventCh
- Backpressure via non-blocking channel sends (drop with warning)
- Two deployment modes: combined (monitor + dispatch in one binary) and split (monitor writes CRDs, dispatcher reads them)

## Layers

**Configuration Layer:**
- Purpose: Load and validate all settings from YAML config file + env var overrides
- Location: `internal/config/`
- Contains: `Config` struct, cluster loading, env config loading, slack routing, namespace lookup
- Depends on: `k8s.io/client-go/tools/clientcmd`
- Used by: `cmd/monitor/main.go`, `cmd/dispatcher/main.go`

**Watcher Layer:**
- Purpose: Detect Deployment rollouts across multiple Kubernetes clusters
- Location: `internal/watcher/`
- Contains: Manager, ClusterWatcher, Debouncer, NamespaceFilter, ConfigWatcher
- Depends on: `internal/config`, `internal/models`, `internal/persistence`, `k8s.io/client-go/informers`
- Used by: `cmd/monitor/main.go`

**Dispatch Layer:**
- Purpose: Fan out rollout events to notification/audit targets
- Location: `internal/dispatch/`
- Contains: Dispatcher, Target interface, SlackTarget, LogTarget, AuditTarget, SlackBotTarget, RecordWatcher, TTLCleaner
- Depends on: `internal/models`, `internal/persistence`
- Used by: `cmd/monitor/main.go`, `cmd/dispatcher/main.go`

**Investigation Layer:**
- Purpose: Post-rollout analysis with concurrent orchestration and reporting
- Location: `internal/investigation/`
- Contains: Orchestrator, Investigator interface, RunbookInvestigator, HolmesInvestigator, SlackReporter, StatusCache, StatusHandler
- Depends on: `internal/diagnostic`, `internal/models`
- Used by: `cmd/monitor/main.go` (registered as dispatch Target)

**Diagnostic Layer:**
- Purpose: Two-phase rollout health analysis (monitor convergence, gather diagnostics)
- Location: `internal/diagnostic/`
- Contains: RolloutAnalyzer, DiagnosticReport, AsyncDiagnosticTarget, ClusterRegistry
- Depends on: `internal/models`, `k8s.io/client-go/kubernetes`
- Used by: `internal/investigation/`

**Persistence Layer:**
- Purpose: CRD-based storage for template hashes and audit records
- Location: `internal/persistence/`
- Contains: HashStore (batched writes), AuditRecorder
- Depends on: `api/v1alpha1`, `sigs.k8s.io/controller-runtime/pkg/client`
- Used by: `internal/watcher/`, `internal/dispatch/`, `cmd/monitor/main.go`

**API Types Layer:**
- Purpose: CRD type definitions for Kubernetes
- Location: `api/v1alpha1/`
- Contains: ClusterRolloutState, RolloutRecord, MonitorConfig types + deepcopy + scheme registration
- Depends on: `k8s.io/apimachinery`
- Used by: `internal/persistence/`, `internal/watcher/`, `internal/dispatch/`

**Models Layer:**
- Purpose: Shared data types for the pipeline
- Location: `internal/models/`
- Contains: RolloutEvent struct
- Depends on: nothing
- Used by: all internal packages

## Data Flow

### Primary Flow: Rollout Detection and Dispatch (Combined Mode)

1. Manager loads kubeconfigs from `KUBECONFIG_DIR`, creates one ClusterWatcher per cluster (`internal/watcher/manager.go:126`)
2. ClusterWatcher starts SharedInformerFactory watching Deployments; initial LIST seeds templateCache via `onAdd` (`internal/watcher/informer.go:172`)
3. On Deployment update, `onUpdate` computes SHA256 of `spec.template`; emits RolloutEvent only if hash changed and prior hash existed (`internal/watcher/informer.go:197`)
4. Event enricher (if configured) adds App name and SlackChannel from env config lookup (`cmd/monitor/main.go:251`)
5. ClusterWatcher submits event to Debouncer (`internal/watcher/debouncer.go:35`)
6. After debounce window (default 30s), Debouncer does non-blocking send to `eventCh` (`internal/watcher/debouncer.go:64`)
7. Dispatcher worker reads from `eventCh`, calls `Dispatch()` on each registered Target (`internal/dispatch/dispatcher.go:91`)
8. Targets execute: LogTarget logs, SlackTarget posts webhook, AuditTarget writes RolloutRecord CRD, InvestigationTarget triggers async investigation

### Secondary Flow: Post-Rollout Investigation

1. InvestigationTarget calls `Orchestrator.Investigate(event)` (`internal/investigation/target.go:27`)
2. Orchestrator supersedes any in-flight investigation for same deployment key (`internal/investigation/orchestrator.go:64`)
3. Acquires semaphore slot (bounded concurrency), spawns goroutine (`internal/investigation/orchestrator.go:82`)
4. Investigator (Runbook or Holmes) runs analysis (`internal/investigation/investigator.go:16`)
5. RunbookInvestigator delegates to RolloutAnalyzer.Analyze: Phase 1 polls deployment status until convergence or failure, Phase 2 gathers diagnostics (`internal/diagnostic/analyzer.go:91`)
6. SlackReporter posts DiagnosticReport to Slack webhook (`internal/investigation/slack_reporter.go`)

### Tertiary Flow: Split Mode (Dispatcher Service)

1. Monitor writes RolloutRecord CRDs with phase=Detected (`internal/persistence/audit_recorder.go:32`)
2. RecordWatcher (in dispatcher binary) watches RolloutRecord CRDs via dynamic informer (`internal/dispatch/record_watcher.go:49`)
3. On Detected record, RecordWatcher claims it via optimistic lock (phase=Processing) (`internal/dispatch/record_watcher.go:115`)
4. Dispatches event to targets synchronously, updates final status on CRD (`internal/dispatch/record_watcher.go:107`)
5. TTLCleaner garbage-collects old records (`internal/dispatch/ttl_cleaner.go`)

### Health and Rescan Flow

1. Manager.reconcileLoop ticks every 10s (`internal/watcher/manager.go:233`)
2. Phase 1: Rescans kubeconfig directory for added/changed/removed cluster files (at `rescanInterval`)
3. Phase 2: Health-checks running watchers (consecutiveErrors >= 5 or permanent auth failure = unhealthy)
4. Phase 3: Retries pending clusters with exponential backoff (10s to 5m cap)

**State Management:**
- Template hashes: in-memory `templateCache` per ClusterWatcher, optionally persisted to ClusterRolloutState CRDs via HashStore (batched flush every 5s)
- Namespace filtering: in-memory NamespaceFilter, hot-reloadable via MonitorConfig CRD
- Investigation state: in-memory `active` map in Orchestrator with cancel functions for supersede

## Key Abstractions

**Target Interface:**
- Purpose: Pluggable dispatch destination for rollout events
- Examples: `internal/dispatch/log.go`, `internal/dispatch/slack.go`, `internal/dispatch/audit_target.go`, `internal/investigation/target.go`, `internal/diagnostic/target.go`
- Pattern: Interface with `Dispatch(ctx, event) error` and `Name() string`; registered in slice at startup

**Investigator Interface:**
- Purpose: Pluggable rollout investigation backend
- Examples: `internal/investigation/runbook.go`, `internal/investigation/holmes.go`
- Pattern: Interface with `Investigate(ctx, event) (*DiagnosticReport, error)`

**ClientsetProvider Interface:**
- Purpose: Abstract cluster credential lookup for diagnostic analyzer
- Examples: `internal/diagnostic/registry.go` (ClusterRegistry)
- Pattern: Interface with `ClientsetFor(clusterID) (kubernetes.Interface, error)`

**HashObserver Interface:**
- Purpose: Decouples watcher from persistence; receives hash updates/deletes
- Examples: `internal/watcher/manager.go` (hashObserverAdapter wrapping HashStore)
- Pattern: Interface with `OnHashUpdate` and `OnHashDelete`

## Entry Points

**Monitor Binary:**
- Location: `cmd/monitor/main.go`
- Triggers: `go run ./cmd/monitor` or container entrypoint
- Responsibilities: Loads config, starts Manager with ClusterWatchers, starts Dispatcher with Targets, blocks on SIGTERM/SIGINT

**Dispatcher Binary:**
- Location: `cmd/dispatcher/main.go`
- Triggers: `go run ./cmd/dispatcher` or container entrypoint
- Responsibilities: Watches RolloutRecord CRDs, dispatches events, cleans up old records (split mode only)

## Architectural Constraints

- **Threading:** Multiple goroutines: one per ClusterWatcher informer, N dispatcher workers (default 3), one debounce timer per active deployment key, one reconcile loop. All coordination via channels and sync.Mutex.
- **Global state:** `slog.SetDefault()` sets global logger in main. No other global mutable state.
- **Circular imports:** None detected. Clean layered dependency graph: models <- config/persistence/diagnostic <- watcher/dispatch/investigation <- cmd.
- **Memory:** Informer transform (`stripUnneededFields`) drops managedFields and last-applied-configuration to reduce memory at multi-cluster scale (deployed limit 128Mi).
- **Read-only:** Monitor never mutates watched Deployments or workload resources. Only writes its own CRDs when persistence is enabled.

## Anti-Patterns

### Silently Swallowing Dropped Events

**What happens:** When `eventCh` is full, the Debouncer drops events with only a `slog.Warn` (`internal/watcher/debouncer.go:71`). Similarly, the Orchestrator drops investigations when the semaphore is full (`internal/investigation/orchestrator.go:84`).
**Why it's wrong:** Dropped rollout events are unrecoverable; no metric or alerting mechanism exposes the drop rate.
**Do this instead:** Expose drop counters via metrics or the status API so operators can detect capacity issues.

### Int Parsing Silently Falls Back

**What happens:** `envInt()` in `internal/config/config.go:319` returns default on parse error without logging.
**Why it's wrong:** A typo in an env var (e.g., `WORKER_COUNT=three`) silently uses the default, making misconfiguration invisible.
**Do this instead:** Log a warning when an env var is set but unparseable.

## Error Handling

**Strategy:** Log-and-continue for non-fatal errors; `os.Exit(1)` for startup failures in main.

**Patterns:**
- Watcher errors increment `consecutiveErrors` counter; >= 5 consecutive = unhealthy, triggers stop + retry with backoff
- Permanent errors (401/403) set `permanent` flag, preventing futile retries
- Dispatch errors are logged per-target but don't block other targets (fan-out continues)
- HashStore re-queues failed flush batches for next cycle (`internal/persistence/hash_store.go:115`)
- RecordWatcher uses optimistic locking (resourceVersion) for distributed claim; 409 Conflict is expected and handled gracefully

## Cross-Cutting Concerns

**Logging:** `log/slog` with JSON handler. Three levels: Info (default), Debug (`DEBUG=true`), Trace (`TRACE=true`, custom level at -8 in `internal/trace/level.go`). All log calls include structured key-value pairs with deployment keys.

**Validation:** Config validation at startup in `internal/config/config.go:251` (validate function). Invalid dispatch/investigation modes fail startup. Missing required URLs for enabled modes fail startup.

**Authentication:** Kubeconfig-based auth per cluster. Monitor uses per-cluster REST configs. Persistence uses first cluster's REST config for controller-runtime client. Dispatcher uses in-cluster config or KUBECONFIG fallback.

**Graceful Shutdown:** SIGTERM/SIGINT signal handling in main. Ordered teardown: stop status server, stop Manager (stops all watchers + debouncer), close eventCh, wait for Dispatcher workers, stop Orchestrator (drains in-flight investigations), cancel context.

---

*Architecture analysis: 2026-07-23*
