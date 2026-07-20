# Architecture

## Overview

The deploy monitor is a standalone Go service that watches Kubernetes Deployment rollouts across one or more clusters and dispatches events to configurable targets (stdout, Holmes API, Slack). It detects rollouts by hashing the pod template spec — only actual template changes (image, env, volume) trigger events, not status updates or scale changes.

## Operational Flow

Step-by-step lifecycle from operator startup through rollout detection and dispatch.

### 1. Operator Starts

The monitor process starts, loads configuration from environment variables, and builds a `*rest.Config` per cluster from mounted kubeconfigs (or the current kubeconfig context in local dev mode).

If persistence is enabled, it connects to the central cluster's API server and reads any existing `ClusterRolloutState` CRDs to recover previously cached template hashes.

### 2. LIST All Deployments

For each cluster, the monitor creates a `SharedInformerFactory` and starts it. The informer issues a LIST request to the cluster's API server, returning every `apps/v1/Deployment` across all (non-filtered) namespaces.

Each Deployment from the LIST triggers the `AddFunc` handler, which computes `SHA256(json(deployment.Spec.Template))` and stores it in the in-memory `templateCache`:

```
templateCache["minikube/default/checkout"]    = "aaa111..."
templateCache["minikube/default/payments"]    = "bbb222..."
templateCache["minikube/production/api"]      = "ccc333..."
```

This is the baseline. No rollout events are emitted — the monitor is just learning the current state of the world.

If persisted hashes were loaded in step 1, the `AddFunc` compares the LIST hash against the persisted hash. A mismatch means a rollout happened while the monitor was down — this is detected and dispatched as a gap-detection event.

### 3. Start Watch

After the LIST completes and the cache is synced, the informer transitions to a long-lived WATCH connection. The API server streams change events for Deployments in real time. The informer handles reconnects, backoff, and bookmark events automatically.

The monitor is now live — any Deployment change on the cluster will arrive as an event.

### 4. Event Received (UpdateFunc Triggers)

A Deployment changes. The Kubernetes API server sends an UPDATE event through the watch stream. The informer calls `UpdateFunc` with the old and new Deployment objects.

Not every update is a rollout. The API server sends events for status changes (replica counts updating), scale operations, label/annotation edits, and actual template changes. They all arrive through the same `UpdateFunc`.

### 5. Hash Comparison

The monitor computes `SHA256(json(newDeployment.Spec.Template))` and looks up the cached hash for this Deployment:

```
newHash = SHA256(newDeployment.Spec.Template)  →  "ddd444..."
oldHash = templateCache["minikube/default/checkout"]  →  "aaa111..."
```

**If `newHash == oldHash`**: Not a rollout. The change was a status update, scale event, or metadata edit — `spec.template` is unchanged. The event is silently dropped. No further processing.

**If `newHash != oldHash`**: Rollout detected. The `spec.template` changed — this means a new image, environment variable, volume mount, or other pod-level configuration change. The monitor builds a `RolloutEvent` with the cluster, namespace, deployment name, old/new images, and old/new hashes.

The cache is updated: `templateCache["minikube/default/checkout"] = "ddd444..."`.

### 6. Debounce

The `RolloutEvent` is submitted to the debouncer under the key `minikube/default/checkout`. The debouncer starts a 30-second timer for this key.

If another template change arrives for the same Deployment within 30 seconds (e.g., a rapid `kubectl apply` correction), the timer resets and the pending event is replaced with the latest one. Only the final event is emitted when the timer expires.

### 7. Dispatch

When the debounce timer fires, the event is placed on a buffered channel. A dispatcher worker goroutine picks it up and routes it to all configured targets:

- **Log**: Writes a structured JSON line to stdout with cluster, namespace, deployment, image diff, and hash diff
- **Holmes**: POSTs to `/api/chat` with a natural-language query: *"Deployment checkout in namespace default on cluster minikube rolled out: checkout:v1.9 → checkout:v2.0. Analyse the rollout health."* — Holmes investigates autonomously
- **Slack**: POSTs a formatted message to the webhook: *"Rollout detected: checkout (default) on minikube — checkout:v1.9 → checkout:v2.0"*

### 8. Record and Persist

If persistence is enabled:

- A `RolloutRecord` CRD is created with phase `Detected`, then updated to `Dispatched` (or `Failed`) after all targets have been called
- The updated template hash is buffered for the next `ClusterRolloutState` flush (every 5 seconds)

The `RolloutRecord` is the audit trail. The `ClusterRolloutState` is the baseline for surviving the next restart.

### 9. Steady State

The monitor continues watching. Steps 4–8 repeat for every Deployment change. The informer maintains the watch connection, the debouncer coalesces rapid changes, and the dispatcher routes events to targets.

On shutdown (SIGTERM/SIGINT), the monitor cancels all informer contexts, stops the debouncer (dropping pending events), and if persistence is enabled, flushes any buffered hash updates to the `ClusterRolloutState` CRD.

### 10. Credential Rotation and Cluster Discovery

When running with `KUBECONFIG_DIR` (e.g., volume-mounted Rancher kubeconfigs), credentials can rotate and new clusters can appear at any time. The monitor runs a periodic reconcile loop to handle this without requiring a restart.

**How it works:**

The Manager re-reads the kubeconfig directory on a configurable interval (`RESCAN_INTERVAL_SECONDS`, default 600, `0` = disabled). Each file is hashed with SHA256 to detect changes efficiently — if a file's content hasn't changed, its watcher is left alone.

On each rescan, three things can happen:

- **New file appeared** — A new kubeconfig file was added to the directory (new cluster onboarded). The manager starts a fresh ClusterWatcher for it. The informer issues an initial LIST to seed the template cache, then transitions to WATCH. No false rollout events are emitted — the new watcher treats the LIST as baseline, same as initial startup.

- **File content changed** — A kubeconfig was updated (token rotation, server URL change, certificate renewal). The manager stops the old ClusterWatcher, creates a new one with the updated `*rest.Config`, and starts it. The new watcher re-LISTs deployments to rebuild its template cache. Because it starts fresh, no stale credentials are used.

- **File removed** — A cluster was decommissioned. The manager stops the watcher and removes it from the map. Any pending debounced events for that cluster are dropped when the debouncer is next cleaned up.

**What doesn't happen:**

If the directory is temporarily unreadable (e.g., during a volume remount), the reconcile logs an error and skips the cycle. Existing watchers continue running undisturbed — the monitor never tears down working watchers because of a transient read failure.

**Configuration:**

| Variable | Default | Description |
|---|---|---|
| `RESCAN_INTERVAL_SECONDS` | `600` | How often to re-read `KUBECONFIG_DIR` for changes. `0` disables rescanning. |

This feature only activates when `KUBECONFIG_DIR` is set. Single-kubeconfig mode (`KUBECONFIG`) does not rescan.

---

## Core Components

```
┌─────────────────────────────────────────────────────┐
│  ClusterWatchManager                                │
│  ┌───────────────────────────────────────────────┐  │
│  │ ClusterWatcher (one per cluster)              │  │
│  │  SharedInformerFactory → LIST+WATCH           │  │
│  │  templateCache: map[key]sha256                │  │
│  │  UpdateFunc: hash comparison → RolloutEvent   │  │
│  └──────────────────┬────────────────────────────┘  │
│                     │                               │
│              Debouncer (30s per deployment)          │
│                     │                               │
│              chan RolloutEvent (buffered)            │
│                     │                               │
│  ┌──────────────────▼────────────────────────────┐  │
│  │ Dispatcher (N worker goroutines)              │  │
│  │  ├─ LogTarget (always on)                     │  │
│  │  ├─ HolmesTarget (POST /api/chat)            │  │
│  │  └─ SlackTarget (POST webhook)               │  │
│  └───────────────────────────────────────────────┘  │
│                                                     │
│  Persistence Store (optional)                       │
│  ├─ ClusterRolloutState CRD (hash persistence)      │
│  └─ RolloutRecord CRD (event history)               │
└─────────────────────────────────────────────────────┘
```

### Watch Layer

Each cluster gets its own `ClusterWatcher` running a `client-go` `SharedInformerFactory`. The informer handles the full LIST+WATCH lifecycle: initial list to seed the cache, long-lived watch for updates, automatic reconnect with backoff on failures.

**Rollout detection** works by computing `SHA256(json(deployment.Spec.Template))` on every update event. If the hash changed from the cached value, it's a rollout. This filters out noise — status updates, scale changes, label edits, and annotation changes all leave the template hash unchanged.

On startup, the initial LIST seeds the cache with current hashes (baseline). No rollout events are emitted for the baseline — only subsequent changes trigger events.

**Staggered startup** spaces cluster watchers 1 second apart to avoid hammering the API server with simultaneous LIST calls across all clusters.

### Resource Profile: SharedInformerFactory Watch per Cluster

Each `ClusterWatcher` uses a `SharedInformerFactory` which maintains a long-lived HTTP/2 Watch stream to the API server. Steady-state cost per cluster:

- **Goroutines (~2):** one Reflector goroutine (drives the List+Watch HTTP loop) and one controller/processor goroutine (drains the internal delta FIFO and calls `onAdd`/`onUpdate`/`onDelete`). Plus one transient goroutine per pending debouncer timer.
- **TCP connections (1):** one persistent HTTP/2 watch stream per cluster. The informer watches all Deployments cluster-wide through this single connection — it is not per-namespace or per-deployment.
- **Memory:** dominated by the informer's in-memory store — one full Deployment object per watched deployment, after `stripUnneededFields` strips `managedFields` and `last-applied-configuration`. Roughly 2–5 KB per deployment post-stripping; 1000 deployments ≈ 2–5 MB. The `templateCache` map (64-char SHA256 string per key) is negligible. Resync is disabled (`0`), so no periodic full re-LIST memory spikes.
- **CPU:** essentially idle. The Watch connection blocks on the API server. CPU use comes only from deserializing events and computing `templateHash` (JSON marshal → SHA256), both trivially cheap.

#### Scaling estimates

| Clusters | Deployments (total) | Goroutines | TCP connections | Estimated memory (informer stores) |
|---|---|---|---|---|
| 10 | 2,000 | ~20 | 10 | ~10 MB |
| 50 | 10,000 | ~100 | 50 | ~50 MB |
| 100 | 20,000 | ~200 | 100 | ~100 MB |

Memory is the binding constraint. The deployed limit of 128Mi is workable up to ~50 clusters with typical deployment counts. Beyond that, raise the memory limit or shard watchers across replicas.

#### How field stripping affects memory

`stripUnneededFields` removes `managedFields` and `last-applied-configuration` via the informer's `WithTransform` option. Published benchmarks show this reduces per-object memory by 26–50%:

- Red Hat documented an operator going from 36 MiB to 14 MiB under load after adding `SetTransform` to strip unused fields ([source](https://developers.redhat.com/articles/2026/06/01/protect-your-kubernetes-operator-oomkill)).
- Kubernetes upstream benchmarks on 50,000 pods showed per-object memory dropping from 24.15 KB to 8.44 KB (65% reduction) when combining field stripping with string interning ([source](https://github.com/kubernetes/kubernetes/issues/137109)).
- KubeVela explicitly drops `managedFields` and `last-applied-configuration` from their informer cache, citing these as the largest per-object overhead contributors ([source](https://www.cncf.io/blog/2023/04/12/stability-and-scalability-assessment-of-kubevela/)).

#### Comparable systems at scale

These numbers come from published benchmarks, not estimates:

- **ArgoCD** (CNOE benchmark): 500 clusters / 50k apps on 3× m5.2xlarge nodes. At 10k apps the controller ran ~2,100–2,800 goroutines. Production rule of thumb: ~1 controller shard per 15–20 clusters ([source](https://cnoe.io/blog/argo-cd-application-scalability)).
- **Karmada** (CNCF test): 100 clusters × 5,000 nodes × 20,000 pods. Per-cluster agent memory: 266 MiB for a 5,000-node cluster. CPU: 40 millicores ([source](https://www.cncf.io/blog/2022/11/29/support-for-100-large-scale-clusters/)).
- **Rancher Fleet**: documented hard limit of 100,000 BundleDeployments (e.g., 150 bundles × 2,000 clusters) before a single controller becomes untenable ([source](https://fleet.rancher.io/tutorials/best-practices)).
- **KubeVela**: memory scales linearly — 1 GB at 3k apps, 4 GB at 12k apps, 160 GB (5 replicas × 32 GB) at 400k apps ([source](https://www.cncf.io/blog/2023/04/12/stability-and-scalability-assessment-of-kubevela/)).

#### Initial LIST cost

The most expensive moment is startup. The informer issues a full LIST of all Deployments per cluster. With staggered startup (1s between clusters), 100 clusters takes ~100 seconds to reach full watch coverage. The LIST response loads all Deployment objects into memory at once. For clusters with thousands of Deployments and large annotation payloads, this can cause a transient memory spike.

Kubernetes 1.27+ introduced WatchList (KEP-3157) which streams the initial sync instead of buffering it. In benchmarks, this reduced API server memory from 70–80 GB to 3 GB for concurrent 10× 1 GB LIST operations. Client-side benefits depend on the API server version ([source](https://kubernetes.io/blog/2025/05/09/kubernetes-v1-33-streaming-list-responses/)).

### Error Handling and Retry

**client-go Reflector (automatic, inside the informer):**

- **Watch disconnect** (TCP reset, timeout): Reflector backs off exponentially (capped ~5 min), re-opens Watch from the last known `resourceVersion`. No data loss.
- **410 Gone** (etcd compacted the requested `resourceVersion`): Reflector does a full re-LIST to obtain a fresh `resourceVersion`, then resumes Watch. `onAdd` re-seeds the `templateCache` with current state. Events during the gap are silently missed (see known gap below).
- **401/403** (expired creds, RBAC revoked): Reflector still retries forever. The custom `SetWatchErrorHandler` (`informer.go:91`) logs these as permanent errors. Recovery happens when creds rotate on disk and `reconcileLoop` recycles the watcher with a fresh clientset.
- **API server unreachable**: same exponential backoff; reconnects when the server returns.

**Application layer (Manager / ClusterWatcher):**

- **Initial cache sync failure**: `WaitForCacheSync` returns false → `Start()` returns an error. Manager logs it, skips that cluster, continues starting others.
- **Kubeconfig removed**: `reconcileLoop` detects missing file hash → calls `Stop()`, removes watcher.
- **Kubeconfig credentials rotated**: `reconcileLoop` detects changed file hash → stops old watcher, starts new one with fresh clientset. Re-seeds hashes from CRD if persistence is enabled.
- **Event channel full**: Debouncer does a non-blocking send; a full queue drops the event with a warning log rather than blocking the informer goroutine.

**Known gap:** On a 410-triggered re-LIST, intermediate rollout events that occurred between the old `resourceVersion` and the new LIST are missed. The `SeedHashes` + CRD persistence mechanism partially mitigates this across restarts (hash mismatch from persisted state triggers a rollout event), but within a running session a re-LIST resets the baseline silently.

### Debouncer

Rapid template changes to the same deployment (e.g., multiple `kubectl apply` in quick succession) are coalesced. The debouncer holds events for 30 seconds per deployment key. Each new event for the same key resets the timer and replaces the pending event. Only the latest event is emitted when the timer expires.

### Dispatcher

A pool of worker goroutines (default 3) consumes events from a buffered channel and routes them to configured targets. All targets receive every event. Targets are:

- **Log** — always on, writes structured JSON to stdout
- **Holmes** — POST to Holmes API with a natural-language query describing the rollout, so Holmes can investigate autonomously
- **Slack** — POST to a webhook with a formatted notification

### Namespace Filtering

Namespaces are filtered at the watcher level before any processing. Two modes:

- **Allowlist** — only watch listed namespaces (takes precedence)
- **Denylist** — watch everything except listed namespaces (default: `kube-system`, `kube-public`, `kube-node-lease`)

## Persistence

### Why It's Needed

Rollout detection works by comparing two `spec.template` hashes: the previously cached hash and the current one from the watch event. This is fundamentally a diff between two points in time — it requires a "before" state to compare against.

Without persistence, all hashes live in memory. When the monitor restarts — whether from a deploy, OOM kill, node drain, or crash — the "before" state is lost. The monitor re-seeds from the current state of each cluster via the informer's initial LIST, but it only has one point in time (now). There is no previous hash to compare against, so the comparison cannot happen. Any Deployment that changed its `spec.template` while the monitor was down is indistinguishable from a Deployment that was always in its current state.

Concrete example:

1. Monitor is running, has the Deployment `default/checkout` cached with template hash `aaa111` (image `checkout:v1.9`)
2. Monitor restarts
3. During downtime, someone runs `kubectl set image deployment/checkout checkout=checkout:v2.0` — the Deployment's `spec.template` changes, producing a new hash `bbb222`
4. Monitor comes back, initial LIST returns the Deployment with hash `bbb222` — this is stored as the baseline
5. The `v1.9 → v2.0` rollout is never detected because the monitor has no record of the previous hash `aaa111` to compare against

Note: scaling events, status updates, and label changes are already filtered out by design — the hash is `SHA256(spec.template)`, and fields like `spec.replicas` or `status` are outside the template. Persistence is specifically about preserving the "before" state across restarts so the comparison mechanism continues to work.

Persistence solves two problems:

1. **Gap detection** — On restart, the monitor loads the last-known hashes from before the shutdown. When the informer's initial LIST arrives with current state, any hash mismatches are real rollouts that happened while the monitor was down. In the example above, the monitor would load the persisted `v1.9` hash, compare it to the current `v2.0` hash from LIST, and fire the rollout event.

2. **Audit trail** — Every detected rollout is recorded as a Kubernetes custom resource. This gives operators a queryable history of what rolled out, when, where, and whether dispatch succeeded or failed.

### How It Works

Persistence uses two Kubernetes CRDs stored on the same cluster the monitor runs on.

**`ClusterRolloutState`** — one per watched cluster. Stores the full map of `namespace/deployment → SHA256 hash`. Updated via batched writes every 5 seconds to avoid excessive API calls. On startup, the monitor reads these before starting the informer, seeding the template cache with persisted hashes instead of starting blank.

```
kubectl get clusterrolloutstates -n rollout-monitor
NAME       CLUSTER    DEPLOYMENTS   LAST SYNC
minikube   minikube   9             2026-07-16T05:00:44Z
```

**`RolloutRecord`** — one per detected rollout. Created when the dispatcher picks up an event, then updated with dispatch status after all targets have been called. Tracks the full lifecycle:

- `Detected` — rollout seen, record created
- `Dispatched` — successfully sent to at least one target
- `Failed` — all dispatch targets failed

```
kubectl get rolloutrecords -n rollout-monitor
NAME                                       CLUSTER    NAMESPACE   DEPLOYMENT     PHASE        AGE
minikube-default-test-rollout-1784178041   minikube   default     test-rollout   Dispatched   7s
```

Each record carries labels (`deploy-monitor.io/cluster`, `deploy-monitor.io/namespace`, `deploy-monitor.io/deployment`) for filtering with label selectors.

**Batched writes with re-queue on failure** — Hash updates are buffered in memory and flushed to the `ClusterRolloutState` CRD every 5 seconds. If a write fails (API server unavailable, conflict), the updates are re-queued for the next flush cycle. A final flush runs on shutdown to minimize data loss.

Persistence is opt-in via `PERSISTENCE_ENABLED=true`. When disabled, the monitor behaves exactly as before — in-memory hashes, no CRDs, no API writes.

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|---|---|---|
| `KUBECONFIG_DIR` | — | Directory of kubeconfig files (multi-cluster mode) |
| `KUBECONFIG` | `~/.kube/config` | Single kubeconfig file (local dev mode) |
| `NAMESPACE_ALLOWLIST` | — | Comma-separated namespaces to watch (overrides denylist) |
| `NAMESPACE_DENYLIST` | `kube-system,kube-public,kube-node-lease` | Comma-separated namespaces to exclude |
| `DISPATCH_MODE` | `log` | `log`, `holmes`, `slack`, or `both` |
| `HOLMES_API_URL` | — | Holmes API base URL (required if dispatch includes holmes) |
| `SLACK_WEBHOOK_URL` | — | Slack incoming webhook URL (required if dispatch includes slack) |
| `WORKER_COUNT` | `3` | Number of dispatcher worker goroutines |
| `DEBOUNCE_SECONDS` | `30` | Debounce window per deployment |
| `QUEUE_MAX_SIZE` | `100` | Buffered event channel capacity |
| `RESCAN_INTERVAL_SECONDS` | `600` | How often to re-read `KUBECONFIG_DIR` for changes (`0` = disabled) |
| `PERSISTENCE_ENABLED` | `false` | Enable CRD-based persistence |
| `PERSISTENCE_NAMESPACE` | `rollout-monitor` | Namespace where CRDs are stored |

## Project Layout

```
cmd/monitor/main.go              Entry point — config, watcher, dispatcher, persistence wiring
api/v1alpha1/                    CRD type definitions (ClusterRolloutState, RolloutRecord)
internal/
  config/
    config.go                    Environment variable loading and namespace filtering
    kubeconfig.go                Cluster credential loading (single file or directory)
  models/
    event.go                     RolloutEvent struct
  watcher/
    manager.go                   Manages per-cluster watchers with staggered startup
    informer.go                  SharedInformerFactory setup, hash comparison, event emission
    debouncer.go                 Per-deployment-key debounce with configurable window
  dispatch/
    dispatcher.go                Worker pool routing events to targets
    log.go                       Stdout structured logging target
    holmes.go                    Holmes API HTTP client
    slack.go                     Slack webhook HTTP client
  persistence/
    store.go                     CRD read/write with batched flush and re-queue
deploy/
  crds.yaml                      CRD definitions (ClusterRolloutState, RolloutRecord)
  deployment.yaml                Kubernetes deployment manifest with RBAC
```
