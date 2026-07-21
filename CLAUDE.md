# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
go build ./...                              # build all packages
KUBECONFIG_DIR=./kubeconfigs go run ./cmd/monitor  # run locally (requires kubeconfig directory)
go test ./...                               # all tests
go test ./internal/watcher/ -run TestClusterWatcher_DetectsRollout -v   # single test
go test ./... -race                         # race detector (concurrent maps + timers make this worth running)
docker build -t rollout-monitor:latest .    # container image
kubectl apply -f deploy/deployment.yaml     # deploy (in-cluster: namespace, SA, ClusterRole, Deployment)
```

No linter or CI config exists in the repo yet.

## Architecture

Watches Kubernetes Deployments across one or more clusters, detects rollouts, and dispatches
notifications. Read-only with respect to watched clusters — never mutates Deployments or other
workload resources. Writes its own CRDs (`ClusterRolloutState`, `RolloutRecord`, `MonitorConfig`)
for persistence and audit when `PERSISTENCE_ENABLED=true`.

Pipeline, wired together in [cmd/monitor/main.go](cmd/monitor/main.go):

```
kubeconfigs → Manager → ClusterWatcher (1 per cluster) → Debouncer → eventCh → Dispatcher → Targets
                ↕                ↕                                                   ↕
          reconcileLoop    HashStore (CRD)                                   AuditRecorder (CRD)
                            ConfigWatcher ← MonitorConfig CRD
```

- **[internal/config/](internal/config/)** — env-var config plus cluster loading. `KUBECONFIG_DIR`
  is required (one file per cluster, multi-cluster).
- **[internal/watcher/](internal/watcher/)** — `Manager` starts one `ClusterWatcher` per cluster,
  staggered 1s apart. Each watcher runs a `SharedInformerFactory` on Deployments with resync
  disabled. Failed clusters are queued for retry with exponential backoff (10s→5m cap).
  The reconcile loop health-checks running watchers (consecutive errors, permanent auth
  failures) and retries pending clusters. Also contains `NamespaceFilter` (thread-safe,
  runtime-updatable allow/deny filter) and `ConfigWatcher` (watches `MonitorConfig` CRD to
  hot-reload namespace filtering).
- **[internal/dispatch/](internal/dispatch/)** — `Dispatcher` fans events to `Target`
  implementations via a worker pool. `LogTarget` is always registered; `AuditTarget` is added
  when persistence is enabled; Holmes and Slack are added based on `DISPATCH_MODE`.
- **[internal/persistence/](internal/persistence/)** — `HashStore` batches template hash writes to
  `ClusterRolloutState` CRDs (flush every 5s). `AuditRecorder` writes `RolloutRecord` CRDs for
  each dispatched event. Both use `controller-runtime` client.
- **[internal/models/](internal/models/)** — `RolloutEvent`, shared by watcher and dispatch.
- **[api/v1alpha1/](api/v1alpha1/)** — CRD types: `ClusterRolloutState` (persisted template
  hashes per cluster), `RolloutRecord` (audit trail), `MonitorConfig` (runtime namespace config).

### Rollout detection

A rollout is a change to `spec.template`, not any update. `ClusterWatcher` keeps a
`templateCache` of `clusterID/namespace/name` → SHA256 of the marshalled pod template:

- `onAdd` seeds the cache on the initial LIST — baseline, never emits an event.
- `onUpdate` emits only when the hash changed and a prior hash existed, which is what filters out
  status updates and scale changes.
- `onDelete` evicts the key (unwrapping `cache.DeletedFinalStateUnknown` tombstones) to bound
  cache growth.

Informers apply `stripUnneededFields` to drop `managedFields` and `last-applied-configuration`,
which matters for memory at multi-cluster scale (deployed limit is 128Mi).

### Backpressure

The `Debouncer` coalesces per deployment key: each event resets a timer and replaces the pending
event, so only the latest survives the window (`DEBOUNCE_SECONDS`, default 30). On expiry it does
a **non-blocking** send to `eventCh` — a full queue drops the event with a warning rather than
blocking the watcher. Queue depth is `QUEUE_MAX_SIZE` (default 100).

## Configuration (env vars)

| Var | Default | Notes |
| --- | --- | --- |
| `KUBECONFIG_DIR` | — | **required**; dir of `.yaml`/`.yml`/`.conf` files; filename stem becomes cluster ID |
| `DISPATCH_MODE` | `log` | `log`\|`holmes`\|`slack`\|`both`; invalid values fail startup |
| `HOLMES_API_URL` | — | required for `holmes`/`both`; posts to `/api/chat` |
| `SLACK_WEBHOOK_URL` | — | required for `slack`/`both` |
| `NAMESPACE_ALLOWLIST` | — | comma-separated; if set, denylist is ignored |
| `NAMESPACE_DENYLIST` | `kube-system,kube-public,kube-node-lease` | applies only when allowlist is empty |
| `WORKER_COUNT` | 3 | dispatcher workers |
| `DEBOUNCE_SECONDS` | 30 | |
| `QUEUE_MAX_SIZE` | 100 | |
| `RESCAN_INTERVAL_SECONDS` | 600 | how often to re-read `KUBECONFIG_DIR` for added/changed/removed clusters |
| `WATCHER_START_TIMEOUT_SECONDS` | 30 | timeout for initial cache sync per cluster; prevents hanging on unreachable clusters |
| `PERSISTENCE_ENABLED` | `false` | enable CRD-based hash persistence and audit recording |
| `PERSISTENCE_NAMESPACE` | `rollout-monitor` | namespace for `ClusterRolloutState` and `RolloutRecord` CRDs |
| `DEBUG` | `false` | set to `true` for debug-level logging |
| `TRACE` | `false` | set to `true` for trace-level logging (investigation pipeline detail) |

Malformed ints silently fall back to the default rather than erroring.

Namespace filtering can also be configured at runtime via a `MonitorConfig` CRD named `default`
(requires `PERSISTENCE_ENABLED=true`). CRD values override env vars when present.

## Conventions

- Structured logging via `log/slog` with a JSON handler; no logging library.
- Tests use `k8s.io/client-go/kubernetes/fake` clientsets and `httptest` for dispatch targets — no
  envtest, no live cluster needed.
- New dispatch destinations implement the `Target` interface and get registered in
  `NewDispatcher`.

## Known issue

`go.mod` requires go 1.25.6 but the [Dockerfile](Dockerfile) builds on `golang:1.22-alpine`, so
the image build fails on the toolchain version. Local builds are unaffected.
