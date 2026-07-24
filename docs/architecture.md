<!-- generated-by: gsd-doc-writer -->
# Architecture

## System Overview

The deploy monitor is a standalone Go service that watches Kubernetes Deployment rollouts across one or more clusters and dispatches events to configurable targets (stdout, Slack, Holmes AI, or a built-in runbook investigator). It detects rollouts by hashing `spec.template` — only actual template changes (image, env, volume) trigger events, not status updates or scale changes. The system is read-only with respect to watched clusters; it only writes its own CRDs (`ClusterRolloutState`, `RolloutRecord`, `MonitorConfig`) when persistence is enabled.

Two deployment modes exist: **combined mode** (the monitor detects and dispatches directly) and **split mode** (`DISPATCHER_SPLIT=true`), where the monitor writes `RolloutRecord` CRDs and a separate dispatcher binary handles notifications. Split mode enables independent scaling and upgrade of the detection and dispatch concerns.

## Component Diagram

```
kubeconfigs → Manager → ClusterWatcher (1 per cluster) → Debouncer → eventCh → Dispatcher → Targets
               ↕                ↕                                                    ↕
         reconcileLoop    HashStore (CRD)                                    AuditRecorder (CRD)
                           ConfigWatcher ← MonitorConfig CRD


                  [Split mode: separate binary]
                  RolloutRecord CRDs → RecordWatcher → StandaloneDispatcher → Targets
```

**Key packages:**

- `cmd/monitor/` — combined monitor binary; wires all components
- `cmd/dispatcher/` — standalone dispatcher binary for split mode
- `internal/config/` — YAML config loading with env var overrides; cluster credential loading; env-config and Slack routing
- `internal/watcher/` — `Manager` + `ClusterWatcher`; rollout detection via template hashing; `Debouncer`; `NamespaceFilter`; `ConfigWatcher`
- `internal/dispatch/` — `Dispatcher` worker pool; `Target` interface; `LogTarget`, `SlackTarget`, `SlackBotTarget`, `AuditTarget`; `RecordWatcher` (split mode); `TTLCleaner`
- `internal/investigation/` — `Orchestrator` for concurrent investigations with supersede semantics; `RunbookInvestigator`; `HolmesInvestigator`; `SlackReporter`; `StatusCache` + HTTP status API
- `internal/diagnostic/` — `RolloutAnalyzer` implementing the two-phase runbook; `ClusterRegistry`; `DiagnosticReport`
- `internal/persistence/` — `HashStore` (batched CRD writes); `AuditRecorder`
- `internal/models/` — `RolloutEvent` shared struct
- `api/v1alpha1/` — CRD types: `ClusterRolloutState`, `RolloutRecord`, `MonitorConfig`

## Data Flow

A typical rollout event moves through the system as follows:

1. **Config load** — On startup `config.Load()` reads `/etc/rollout-monitor/config.yaml` (or `CONFIG_FILE`), then applies env var overrides for secrets. Optional per-app env configs (`ENV_CONFIG_DIR`) and Slack routing files (`SLACK_ROUTING_FILE`) are loaded to enable app-name resolution and per-channel Slack routing.

2. **Cluster credential loading** — `config.LoadClusters()` reads kubeconfig files from `KUBECONFIG_DIR`. Each filename stem becomes the cluster ID. A `*rest.Config` is produced per cluster.

3. **Hash seeding** — If persistence is enabled, each `ClusterWatcher` loads its last-known `ClusterRolloutState` CRD before starting. This seeds the `templateCache` with hashes from the previous run, enabling gap detection.

4. **LIST (baseline)** — Each `ClusterWatcher` starts a `SharedInformerFactory` which issues a full LIST of all `apps/v1/Deployments`. The `onAdd` handler computes `SHA256(json(deployment.Spec.Template))` and stores it in the in-memory `templateCache`. No rollout events are emitted at this stage. If a persisted hash exists for a deployment and the LIST hash differs, a gap-detection rollout event is fired immediately.

5. **WATCH (live)** — After cache sync, the informer transitions to a long-lived WATCH stream. Every `UpdateFunc` call computes a new hash and compares it to the cached value. If the hash changed, a `RolloutEvent` is constructed with cluster, namespace, deployment name, old/new images, and old/new hashes. The event is optionally enriched with `App` and `SlackChannel` fields via the `EventEnricher` callback (resolved from env configs + Slack routing).

6. **Debounce** — The `RolloutEvent` is submitted to the `Debouncer` under the key `clusterID/namespace/deployment`. Each new event for the same key resets a 30-second timer and replaces the pending event. Only the final event is emitted when the timer fires. The send to `eventCh` is non-blocking — a full queue drops the event with a warning rather than blocking the informer goroutine.

7. **Dispatch** — A pool of worker goroutines (default 3) reads from `eventCh` and calls `Dispatch()` on each registered `Target` in sequence. Targets receive every event:
   - `LogTarget` — always on; writes structured JSON to stdout
   - `AuditTarget` — present when persistence is enabled; creates a `RolloutRecord` CRD
   - `SlackTarget` — POST to an incoming webhook
   - `SlackBotTarget` — POST to a per-app Slack channel resolved from `SlackChannel` on the event
   - `InvestigationTarget` — delegates to the `Orchestrator` for async investigation

8. **Investigation** — `InvestigationTarget.Dispatch()` calls `Orchestrator.Investigate()` and returns immediately. The orchestrator runs the investigation asynchronously using bounded concurrency (`INVESTIGATION_MAX_CONCURRENT`, default 10). A new rollout for the same deployment cancels any in-flight investigation (supersede semantics). Two investigator backends are supported:
   - `RunbookInvestigator` — runs `RolloutAnalyzer` directly against the cluster API; two-phase: (1) poll for convergence with soak period, (2) gather diagnostics on failure
   - `HolmesInvestigator` — POSTs to Holmes API (`/api/chat`) with a natural-language query
   Results are posted to Slack via `SlackReporter`. If `TRACE=true`, results are stored in `StatusCache` and exposed via a status HTTP API on port 8081.

9. **Persistence** — Hash updates are buffered in `HashStore` and flushed to `ClusterRolloutState` CRDs every 5 seconds. On shutdown, a final flush runs to minimize data loss. The `AuditRecorder` creates and updates `RolloutRecord` CRDs with dispatch phase (`Detected` → `Dispatched` or `Failed`).

10. **Reconcile loop** — The `Manager` runs a background reconcile loop (ticks every 10 seconds) with three phases per tick:
    - **Directory rescan** (every `RESCAN_INTERVAL_SECONDS`) — re-reads `KUBECONFIG_DIR`; starts watchers for new files, recycles watchers for changed files, stops watchers for removed files
    - **Health check** — inspects running watchers for consecutive errors or permanent auth failures; unhealthy watchers are stopped and queued for retry
    - **Retry** — processes the pending-retry queue with exponential backoff (10s → 5m cap)

11. **Shutdown** — On SIGTERM/SIGINT, the monitor stops the status API, stops the manager (which stops all watchers and the debouncer), drains the event channel, waits for dispatcher workers, stops the investigation orchestrator, and cancels all contexts.

## Key Abstractions

| Abstraction | File | Description |
|---|---|---|
| `Manager` | `internal/watcher/manager.go` | Owns all per-cluster watchers; drives reconcile loop and retry queue |
| `ClusterWatcher` | `internal/watcher/informer.go` | Wraps a `SharedInformerFactory`; maintains `templateCache`; emits rollout events |
| `Debouncer` | `internal/watcher/debouncer.go` | Per-key timer-based coalescing; non-blocking send to `eventCh` |
| `NamespaceFilter` | `internal/watcher/namespace_filter.go` | Thread-safe allow/deny filter; runtime-updatable via `ConfigWatcher` |
| `ConfigWatcher` | `internal/watcher/config_watcher.go` | Watches `MonitorConfig` CRD to hot-reload namespace filtering |
| `Dispatcher` | `internal/dispatch/dispatcher.go` | Worker pool fanning events to registered `Target` implementations |
| `Target` | `internal/dispatch/dispatcher.go` | Interface (`Dispatch`, `Name`) for any notification destination |
| `RecordWatcher` | `internal/dispatch/record_watcher.go` | Split-mode: watches `RolloutRecord` CRDs; dispatches with optimistic locking |
| `Orchestrator` | `internal/investigation/orchestrator.go` | Manages concurrent investigations with supersede semantics and bounded concurrency |
| `Investigator` | `internal/investigation/investigator.go` | Interface (`Investigate`) for runbook and Holmes backends |
| `RolloutAnalyzer` | `internal/diagnostic/analyzer.go` | Two-phase runbook: convergence polling + soak period, then diagnostics on failure |
| `HashStore` | `internal/persistence/hash_store.go` | Batched writes of template hashes to `ClusterRolloutState` CRDs |
| `AuditRecorder` | `internal/persistence/audit_recorder.go` | Creates and updates `RolloutRecord` CRDs for each dispatched event |
| `RolloutEvent` | `internal/models/event.go` | Shared event struct; carries cluster, namespace, deployment, image diff, hashes, app, Slack channel |

## Rollout Detection

A rollout is a change to `spec.template`, not any update. `ClusterWatcher` maintains a `templateCache` keyed by `clusterID/namespace/deploymentName` mapping to `SHA256(json(deployment.Spec.Template))`:

- `onAdd` seeds the cache from the initial LIST — no event emitted; this is baseline
- `onUpdate` emits only when the hash changed **and** a prior hash existed, filtering status updates, scale changes, label/annotation edits
- `onDelete` evicts the cache key (unwrapping `cache.DeletedFinalStateUnknown` tombstones) to bound cache growth

Informers apply `stripUnneededFields` via `WithTransform` to drop `managedFields` and `last-applied-configuration`. This reduces per-object memory by 26–50% at multi-cluster scale (deployed limit is 128 MiB).

## Split Mode

When `DISPATCHER_SPLIT=true` (requires `PERSISTENCE_ENABLED=true`), the monitor binary writes only `RolloutRecord` CRDs — it does not dispatch notifications. A separate `dispatcher` binary (`cmd/dispatcher/`) watches the same CRDs via `RecordWatcher` and handles all notification targets.

`RecordWatcher` uses optimistic locking to claim records: it sets `status.phase` from `Detected` to `Processing` via a compare-and-swap update. A 409 Conflict response means another dispatcher replica already claimed it. Records stuck in `Processing` for more than 10 minutes are reset to `Detected` by a periodic recovery scan. A `TTLCleaner` expires records older than `ROLLOUT_RECORD_TTL_DAYS` (default 7).

## Persistence

Without persistence, all `templateCache` hashes live in memory. On restart, the monitor re-seeds from the current cluster state via the informer LIST but has no prior hashes to compare against — rollouts that occurred during downtime are silently missed.

With `PERSISTENCE_ENABLED=true`, two CRDs address this:

**`ClusterRolloutState`** — one per watched cluster; stores the full `namespace/deployment → SHA256` map. Loaded before the informer starts, so the initial LIST can detect hash mismatches (gap detection). Updated every 5 seconds via batched writes.

**`RolloutRecord`** — one per detected rollout. Phase lifecycle: `Detected` → `Processing` (split mode only) → `Dispatched` or `Failed`. Carries labels `deploy-monitor.io/cluster`, `deploy-monitor.io/namespace`, and `deploy-monitor.io/deployment` for label-selector queries.

**`MonitorConfig`** — cluster-scoped CRD named `default`; overrides `NAMESPACE_ALLOWLIST` and `NAMESPACE_DENYLIST` at runtime without restart. Requires `PERSISTENCE_ENABLED=true`.

## Diagnostic Runbook

The `RolloutAnalyzer` implements a two-phase runbook invoked by `RunbookInvestigator`:

**Phase 1 — Monitor rollout** (up to `AbsoluteTimeout`, default 10m):
1. Poll the Deployment every `PollInterval` (default 10s)
2. Gate on `observedGeneration == generation` before evaluating replica state
3. Check failure conditions: `ProgressDeadlineExceeded` condition, containers stuck in `Waiting` state beyond `ConfigErrorWindow` (default 90s), restart count exceeding `RestartThreshold` (default 3) within `RestartWindow` (default 5m)
4. Detect inactivity stall: no forward progress in replica counts for `InactivityTimeout` (default 5m)
5. On convergence (`updatedReplicas == desired && availableReplicas == desired && unavailableReplicas == 0`), enter a `SoakPeriod` (default 60s) and re-check for regressions

**Phase 2 — Gather diagnostics** (only on non-SUCCESS results):
- Collect Warning events from the namespace related to the deployment
- Inspect pod and init container statuses from the new ReplicaSet
- Fetch and filter current and previous container logs for error patterns (`error`, `fatal`, `panic`, `traceback`, `exception`), with deduplication

Results: `Success`, `Failed`, `Stalled`, `Unstable`, `Paused`, `Deleted`, `Inconclusive`. Reports are posted to Slack via `SlackReporter`.

## Resource Profile

Each `ClusterWatcher` holds one `SharedInformerFactory` maintaining a long-lived HTTP/2 watch stream per cluster.

**Steady-state per cluster:**
- ~2 goroutines (Reflector + processor); plus one transient goroutine per pending debounce timer
- 1 persistent TCP/HTTP/2 connection to the API server
- Memory: ~2–5 KB per deployment object post-stripping; 1,000 deployments ≈ 2–5 MB
- CPU: essentially idle between events; SHA256 computation per event is negligible

**Scaling estimates:**

| Clusters | Deployments (total) | Goroutines | TCP connections | Estimated memory |
|---|---|---|---|---|
| 10 | 2,000 | ~20 | 10 | ~10 MB |
| 50 | 10,000 | ~100 | 50 | ~50 MB |
| 100 | 20,000 | ~200 | 100 | ~100 MB |

Memory is the binding constraint. The deployed limit of 128 MiB supports up to ~50 clusters with typical deployment counts.

## Error Handling and Retry

**client-go Reflector (automatic):**
- Watch disconnect — exponential backoff, reconnects from last `resourceVersion`
- 410 Gone (etcd compaction) — full re-LIST to obtain a fresh `resourceVersion`; `onAdd` re-seeds the cache; events during the gap are missed within a session
- 401/403 — logged as permanent error; recovery happens when creds rotate and the reconcile loop recycles the watcher

**Manager / application layer:**
- Initial cache sync failure — cluster skipped, queued for retry
- Watcher unhealthy (consecutive errors, permanent auth failures) — stopped and queued for retry with exponential backoff (10s → 5m cap)
- Kubeconfig removed — watcher stopped on next directory rescan
- Kubeconfig credentials rotated — watcher recycled with fresh clientset on next rescan
- Event channel full — Debouncer drops the event with a warning log; informer goroutine is never blocked

**Known gap:** On a 410-triggered re-LIST, intermediate rollouts missed between the old `resourceVersion` and the new LIST are silently lost. CRD persistence mitigates this across restarts via hash mismatch detection, but not within a running session.

## Directory Structure

```
cmd/
  monitor/main.go          Monitor binary — config, watcher, dispatcher, persistence wiring
  dispatcher/main.go       Dispatcher binary — split mode CRD-driven dispatch

api/v1alpha1/
  types.go                 CRD types: ClusterRolloutState, RolloutRecord, phase constants
  monitor_config.go        MonitorConfig CRD type (runtime namespace filtering)

internal/
  config/
    config.go              YAML config loading, env var overrides, defaults, validation
    kubeconfig.go          Cluster credential loading (directory or single file)
    env_config.go          Per-app env configs, NamespaceLookup, SlackRouting

  models/
    event.go               RolloutEvent struct

  watcher/
    manager.go             ClusterWatcher lifecycle; reconcile loop; rescan; retry queue
    informer.go            SharedInformerFactory setup, hash comparison, event emission
    debouncer.go           Per-key debounce with configurable window; non-blocking send
    namespace_filter.go    Thread-safe allow/deny namespace filter
    config_watcher.go      Watches MonitorConfig CRD to hot-reload namespace filter

  dispatch/
    dispatcher.go          Worker pool; Target interface; DispatchEvent
    log.go                 LogTarget: structured JSON stdout
    slack.go               SlackTarget: incoming webhook
    slack_bot.go           SlackBotTarget: per-app channel via bot token
    audit_target.go        AuditTarget: creates RolloutRecord CRD on dispatch
    record_watcher.go      RecordWatcher: split-mode CRD-driven dispatch with optimistic lock
    holmes.go              HolmesTarget: POST to Holmes /api/chat
    ttl_cleaner.go         Expires RolloutRecord CRDs older than configured TTL

  investigation/
    investigator.go        Investigator interface (Runbook and Holmes implementations)
    orchestrator.go        Concurrent investigation with supersede semantics; bounded semaphore
    runbook.go             RunbookInvestigator: delegates to RolloutAnalyzer
    slack_reporter.go      SlackReporter: posts DiagnosticReport to Slack
    status_cache.go        In-memory last-1 cache of investigation results per deployment
    status_api.go          HTTP handler for /status endpoint (TRACE mode)
    target.go              InvestigationTarget: dispatch.Target wrapper for Orchestrator

  diagnostic/
    analyzer.go            RolloutAnalyzer: two-phase runbook (convergence + diagnostics)
    registry.go            ClusterRegistry: provides kubernetes.Interface per cluster ID
    report.go              DiagnosticReport, Result types, PodStatus, LogSnippet, K8sEvent

  persistence/
    hash_store.go          Batched template hash writes to ClusterRolloutState CRDs
    audit_recorder.go      Creates and updates RolloutRecord CRDs

deploy/
  crds.yaml                CRD manifests (ClusterRolloutState, RolloutRecord, MonitorConfig)
  deployment.yaml          Kubernetes Deployment with RBAC (Namespace, SA, ClusterRole, Deployment)
  monitor/                 Monitor-specific deploy manifests
  dispatcher/              Dispatcher-specific deploy manifests
```

## Configuration

Configuration is loaded from a YAML file (`/etc/rollout-monitor/config.yaml` or `CONFIG_FILE` env var), with env var overrides applied afterward for secrets and backward compatibility. If no config file exists, the monitor falls back to pure env var loading.

| Variable / YAML key | Default | Description |
|---|---|---|
| `KUBECONFIG_DIR` / `kubeconfigDir` | — | **Required.** Directory of kubeconfig files; filename stem becomes cluster ID |
| `DISPATCH_MODE` / `dispatchMode` | `log` | `log`, `holmes`, `slack`, or `both` |
| `HOLMES_API_URL` / `holmesAPIURL` | — | Required for `holmes`/`both` dispatch mode |
| `SLACK_WEBHOOK_URL` / `slackWebhookURL` | — | Required for `slack`/`both` dispatch mode and `runbook` investigation mode |
| `SLACK_BOT_TOKEN` / `slackBotToken` | — | Enables per-app channel routing via Slack bot |
| `NAMESPACE_ALLOWLIST` / `namespaceAllowlist` | — | Comma-separated; if set, denylist is ignored |
| `NAMESPACE_DENYLIST` / `namespaceDenylist` | `kube-system,kube-public,kube-node-lease` | Applies only when allowlist is empty |
| `INVESTIGATION_MODE` / `investigationMode` | `none` | `none`, `runbook`, or `holmes` |
| `INVESTIGATION_MAX_CONCURRENT` / `investigationMaxConcurrent` | `10` | Max concurrent investigations |
| `WORKER_COUNT` / `workerCount` | `3` | Dispatcher worker goroutines |
| `DEBOUNCE_SECONDS` / `debounceSeconds` | `30` | Debounce window per deployment |
| `QUEUE_MAX_SIZE` / `queueMaxSize` | `100` | Buffered event channel capacity |
| `RESCAN_INTERVAL_SECONDS` / `rescanIntervalSeconds` | `600` | How often to re-read `KUBECONFIG_DIR`; `0` disables |
| `WATCHER_START_TIMEOUT_SECONDS` / `watcherStartTimeoutSeconds` | `30` | Timeout for initial cache sync per cluster |
| `PERSISTENCE_ENABLED` / `persistenceEnabled` | `false` | Enable CRD-based hash persistence and audit recording |
| `PERSISTENCE_NAMESPACE` / `persistenceNamespace` | `rollout-monitor` | Namespace for CRDs |
| `DISPATCHER_SPLIT` / `dispatcherSplit` | `false` | Write CRDs only; delegate dispatch to separate dispatcher binary |
| `ENV_CONFIG_DIR` / `envConfigDir` | — | Directory of per-app env YAML configs for app-name resolution |
| `SLACK_ROUTING_FILE` / `slackRoutingFile` | — | YAML file mapping app names to Slack channel IDs |
| `STATUS_API_PORT` / `statusAPIPort` | `8081` | Port for investigation status HTTP API (TRACE mode only) |
| `ROLLOUT_RECORD_TTL_DAYS` / `rolloutRecordTTLDays` | `7` | RolloutRecord expiry (dispatcher binary only) |
| `DEBUG` / `debug` | `false` | Debug-level logging |
| `TRACE` / `trace` | `false` | Trace-level logging; enables status API |
