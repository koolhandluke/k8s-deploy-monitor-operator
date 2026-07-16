# Central Rollout Monitor — Design Spec

**Date:** 2026-07-15
**Status:** Draft (post-review)
**Language:** Go (POC and production — no rewrite needed)
**Scope:** Standalone Go service running on a central Rancher-managed cluster that monitors Deployment rollouts across ~125 remote spoke clusters and dispatches to Holmes or Slack.

---

## 1. Problem Statement

Teams has workloads across ~125 spoke clusters via Rancher. When deployments roll out on spoke clusters and something goes wrong (crashloops, failed readiness, bad config), broken rollouts go unnoticed. The spoke cluster teams lack access and tooling to catch and diagnose failures themselves.

Deploying monitoring (e.g., Holmes) directly to spoke clusters is not an option — it requires approvals, roadmap reviews, and change management from teams that own those clusters. This has been ruled out.

The central rollout monitor solves this by running a single service on infrastructure the CD team already controls (the central Rancher cluster), using Rancher's existing read access to spoke clusters. No deployment to spoke clusters required.

**Trigger:** `apps/v1/Deployment` rollouts only (spec change detected via `spec.template` SHA256 hash change).
**Out of scope for POC:** StatefulSets, DaemonSets, Argo Rollouts, CronJobs.

---

## 2. Investigation Strategy: Agentic vs Non-Agentic

The monitor detects rollouts. The question is what happens next: does Holmes investigate (agentic), or does the monitor pre-collect data itself (non-agentic)?

| | Agentic (Holmes) | Non-Agentic (Pipeline) |
|---|---|---|
| **How it works** | Monitor detects rollout, fires `POST /api/chat` with deployment context. Holmes LLM uses kubectl tools to discover pods, collect logs, and diagnose. | Monitor detects rollout, then itself finds the new ReplicaSet, waits for pods, collects logs, and dispatches the pre-collected data. |
| **Requires Holmes to reach spokes** | Yes — Holmes must have network path + credentials to spoke clusters via Rancher proxy | No — monitor does all k8s API calls itself |
| **Investigation depth** | Deep — Holmes can follow leads, check events, describe pods, inspect configs, correlate across resources | Shallow — fixed pipeline collects logs only, no adaptive investigation |
| **LLM cost per rollout** | ~$0.01–0.05 (tool calls + analysis) | Zero (no LLM involved) |
| **Monitor complexity** | Low — detect + dispatch (3 components) | High — detect + RS finder + pod poller + log collector + dispatch (8 components) |
| **Slack-only mode** | Notification only ("rollout detected: X v1→v2") — no diagnostics without Holmes | Can include log snippets in Slack message |
| **Failure diagnosis quality** | High — LLM can reason about errors, correlate symptoms, suggest fixes | None — raw logs dumped, human must interpret |

**Decision: Agentic (Holmes) for POC.** Delivers diagnostic value with half the code. Namespace filtering (section 7) and debouncing (section 4.3) bound cost. Non-agentic fallback in Appendix A.

---

## 3. Architecture

```
Central Cluster
+---------------------------------------------------------+
|                                                         |
|  rollout-monitor (standalone pod)                       |
|  +---------------------------------------------------+  |
|  |                                                   |  |
|  |  ClusterWatchManager                              |  |
|  |  +---------------------------------------------+  |  |
|  |  | goroutine: watch c-m-abc123 (filtered ns)   |  |  |
|  |  | goroutine: watch c-m-def456 (filtered ns)   |  |  |
|  |  | ...x125 clusters (staggered 1s startup)     |  |  |
|  |  +------------------+------- ------------------+  |  |
|  |                     | RolloutEvent                |  |
|  |                     v                              |  |
|  |       Debouncer (30s per deployment key)           |  |
|  |                     |                              |  |
|  |                     v                              |  |
|  |       chan RolloutEvent (buffered, cap=100)        |  |
|  |                     |                              |  |
|  |  Dispatcher (N=3 goroutines)                      |  |
|  |  +----------------------------------------------+  |  |
|  |  |        +----------+---------+                |  |  |
|  |  |        v                    v                |  |  |
|  |  |  Holmes POST          Slack POST             |  |  |
|  |  |  /api/chat            webhook                |  |  |
|  |  +----------------------------------------------+  |  |
|  |                                                   |  |
|  |  Mounted kubeconfigs (one per spoke cluster)      |  |
|  +---------------------------------------------------+  |
|                                                         |
|  Holmes API Server (existing, unchanged)                |
+---------------------------------------------------------+
           | Rancher k8s proxy per cluster
           v
   Spoke clusters (x125)
   apps/v1/Deployments (filtered namespaces)
```

**Two layers:**
- **Watch layer** — `ClusterWatchManager` detects rollouts and emits events. It does nothing else.
- **Dispatch layer** — debounces events, then dispatches to Holmes and/or Slack.

---

## 4. Components

### 4.1 `RolloutEvent` (Go struct)

```go
type RolloutEvent struct {
    ClusterID       string    // Rancher cluster ID e.g. "c-m-abc123"
    ClusterName     string    // Human-readable e.g. "prod-us-east-1"
    Namespace       string
    DeploymentName  string
    OldTemplateHash string    // SHA256 of previous spec.template
    NewTemplateHash string    // SHA256 of new spec.template
    OldImages       []string  // container images before rollout
    NewImages       []string  // container images after rollout
    Timestamp       time.Time
}
```

Note: `DeploymentUID` removed — not needed since the monitor no longer finds ReplicaSets or Pods. Holmes discovers those itself during investigation.

### 4.2 Cluster Credentials

Kubeconfigs mounted as a volume from a Secret. Read from disk at startup — no Rancher API calls needed.

> **TODO:** Determine kubeconfig layout — one file per cluster (e.g., `/etc/kubeconfigs/c-m-abc123.yaml`) or a single merged kubeconfig with multiple contexts?
>
> **TODO:** How does the monitor obtain the cluster name and/or Rancher proxy URL for the Holmes `cluster_context` field? Options: derive from kubeconfig context name, add a config map/env var mapping cluster ID to name, or embed in kubeconfig file naming convention.

`internal/config/kubeconfig.go` — loads `*rest.Config` per cluster from mounted kubeconfig files using `clientcmd.RESTConfigFromKubeConfig()` or `clientcmd.BuildConfigFromFlags()`.

### 4.3 `ClusterWatchManager` (`internal/watcher/manager.go`)

One goroutine per spoke cluster. Uses `client-go` `SharedInformerFactory` for LIST+WATCH lifecycle, reconnect, and local cache.

**Staggered startup:** 1s delay between clusters to avoid hammering Rancher with 125 simultaneous LISTs.

**Per-cluster startup:** LIST seeds `templateCache` with current hashes (no rollout emitted). Watch resumes from LIST's `resourceVersion`. See section 6 for full initialization sequence.

**On `UpdateFunc`:**

```go
newDeploy := newObj.(*appsv1.Deployment)
key       := clusterID + "/" + newDeploy.Namespace + "/" + newDeploy.Name
newHash   := sha256(json.Marshal(newDeploy.Spec.Template))
oldHash   := templateCache[key]   // already seeded at startup

templateCache[key] = newHash

if newHash == oldHash {
    return  // status update, scale change -- template unchanged
}

// rollout detected -- send to debouncer
debouncer.Submit(key, RolloutEvent{
    ClusterID:       clusterID,
    ClusterName:     clusterName,
    Namespace:       newDeploy.Namespace,
    DeploymentName:  newDeploy.Name,
    OldTemplateHash: oldHash,
    NewTemplateHash: newHash,
    OldImages:       extractImages(oldObj.(*appsv1.Deployment)),
    NewImages:       extractImages(newDeploy),
    Timestamp:       time.Now(),
})
```

**On `DeleteFunc`:** remove `templateCache[key]` to prevent unbounded growth.

**Shutdown:** cancel `stopCh` → informers stop gracefully.

### 4.4 Debouncer (`internal/watcher/debouncer.go`)

Per deployment key (`cluster/namespace/name`): on event arrival, starts a 30s timer. Subsequent events for the same key reset the timer and replace the pending event. On timer expiry, the latest event is emitted to the dispatch channel.

```go
type Debouncer struct {
    window  time.Duration          // 30s
    timers  map[string]*time.Timer // keyed by cluster/namespace/name
    pending map[string]RolloutEvent
    mu      sync.Mutex
    out     chan<- RolloutEvent
}
```

### 4.5 Dispatcher (`internal/dispatch/dispatcher.go`)

Three goroutines consuming from a buffered `chan RolloutEvent` (size 100).

Per-event dispatch based on `DISPATCH_MODE`:

- `"holmes"` -> `POST {HOLMES_API_URL}/api/chat` with body:
  ```json
  {
    "cluster_context": "c-m-abc123",
    "query": "Deployment checkout in namespace default on cluster prod-us-east-1 rolled out: checkout:v1.9 -> checkout:v2.0. Analyse the rollout health."
  }
  ```
  Holmes investigates autonomously on the spoke cluster.

- `"slack"` -> `POST {SLACK_WEBHOOK_URL}` — notification only, no diagnostics:
  ```
  Rollout detected: checkout (default) on prod-us-east-1
  checkout:v1.9 -> checkout:v2.0
  ```

- `"both"` -> both Holmes and Slack.

---

## 5. Persistence — `ClusterRolloutState` CRD (Production Only)

**Not in POC.** POC uses in-memory `templateCache` — hashes lost on restart, monitor re-seeds from current state.

For production: one CRD per spoke cluster on the **central cluster**. Survives restarts and enables gap detection.

```yaml
apiVersion: ep.netskope.com/v1alpha1
kind: ClusterRolloutState
metadata:
  name: prod-us-east-1      # Rancher cluster.name, sanitized
  namespace: rollout-monitor
spec:
  clusterID: "c-m-abc123"
  templateHashes:
    "default/checkout":    "aaa111bbb222..."
    "default/payments":    "ccc333ddd444..."
    "production/api":      "eee555fff666..."
```

**Production considerations:**
- CRD writes should be batched with jitter (buffer 5s per cluster, flush with random jitter up to 10s) to prevent thundering herd during mass rollouts
- `DeleteFunc` should remove entries from the CRD to prevent unbounded growth
- If CRD is missing on startup, first run proceeds without gap detection (safe)

---

## 6. Rollout Detection Logic

### MUST: Use `client-go` `SharedInformerFactory`

Hard requirement. Handles LIST+WATCH lifecycle, reconnect with backoff, local cache, and provides both old/new objects in `UpdateFunc`. Do not hand-roll watches, use polling, or use third-party k8s client libraries.

### Implementation Patterns

Follow from the start — skipping any causes subtle production bugs.

**1. Correct initialization order**

This exact sequence is required. Wrong order silently drops events or causes data races.

```go
// Step 1: Create factory
factory := informers.NewSharedInformerFactoryWithOptions(
    clientset,
    0, // resync disabled — see pattern #4
    informers.WithTransform(stripUnneededFields), // see pattern #3
)

// Step 2: Get ALL informers BEFORE starting
deployInformer := factory.Apps().V1().Deployments().Informer()

// Step 3: Set watch error handler BEFORE starting (race condition if after)
deployInformer.SetWatchErrorHandler(func(r *cache.Reflector, err error) {
    watchErrors.WithLabelValues(clusterID).Inc()
    if apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
        log.Error(err, "permanent watch error — spoke credentials invalid", "cluster", clusterID)
    }
})

// Step 4: Add ALL event handlers BEFORE starting
handle, err := deployInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
    UpdateFunc: onUpdate,
    AddFunc:    onAdd,
    DeleteFunc: onDelete,
})
if err != nil {
    return fmt.Errorf("add event handler: %w", err)
}

// Step 5: Start (non-blocking)
factory.StartWithContext(ctx)

// Step 6: Wait for cache sync
result := factory.WaitForCacheSyncWithContext(ctx)
if err := result.AsError(); err != nil {
    return fmt.Errorf("cache sync failed for cluster %s: %w", clusterID, err)
}
```

**2. Handle tombstones in `DeleteFunc`**

After a watch gap, deleted objects arrive as `cache.DeletedFinalStateUnknown`. Without unwrapping, the type assertion panics.

```go
DeleteFunc: func(obj interface{}) {
    // ALWAYS unwrap tombstones first
    if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
        obj = d.Obj
    }
    deploy, ok := obj.(*appsv1.Deployment)
    if !ok {
        runtime.HandleError(fmt.Errorf("unexpected type in DeleteFunc: %T", obj))
        return
    }
    key := clusterID + "/" + deploy.Namespace + "/" + deploy.Name
    mu.Lock()
    delete(templateCache, key)
    mu.Unlock()
},
```

**3. Strip `managedFields` and `last-applied-configuration` via transform**

1-5KB per object, unused for rollout detection. Adds up across 125 clusters.

```go
func stripUnneededFields(obj interface{}) (interface{}, error) {
    if acc, ok := obj.(metav1.Object); ok {
        acc.SetManagedFields(nil)
        annotations := acc.GetAnnotations()
        delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
        acc.SetAnnotations(annotations)
    }
    return obj, nil
}
```

Set via `informers.WithTransform(...)` at factory construction. Must not be called after `Start()` — documented race ([kubernetes#117869](https://github.com/kubernetes/kubernetes/issues/117869)).

**4. Resync period = 0 (disabled)**

Resync re-delivers all cached objects as synthetic updates — pure noise since they all hit the `oldHash == newHash` return. Disable.

```go
factory := informers.NewSharedInformerFactory(clientset, 0) // 0 = no resync
```

**5. Per-cluster context and `factory.Shutdown()`**

Each cluster gets its own cancellable context and factory for independent teardown.

```go
type ClusterWatch struct {
    cancel  context.CancelFunc
    factory informers.SharedInformerFactory
}

// To remove a cluster:
func (w *ClusterWatch) Stop() {
    w.cancel()            // stops informer goroutines
    w.factory.Shutdown()  // blocks until all goroutines exit
}
```

Always `defer factory.Shutdown()` immediately after construction so it runs on any exit path.

**6. Never mutate informer cache objects**

Informer returns shared pointers — `DeepCopy()` before any mutation. Not needed here (read-only), but document for future contributors.

**7. `SetWatchErrorHandler` for spoke credential alerts**

401/403 errors indicate expired kubeconfig credentials — won't self-heal, need operator attention. Set before `Start()` (pattern #1). Alerting only — reconnection is automatic.

### Non-rollout events filtered out
- `status` field updates (e.g. `updatedReplicas` incrementing) — `spec.template` hash unchanged
- Scale changes (`spec.replicas` changed) — `spec.template` hash unchanged
- Label/annotation changes — `spec.template` hash unchanged

### Detection method

```go
sha256(json.Marshal(deployment.Spec.Template))
```

Rollout = this hash changed from the previously cached value for `clusterID/namespace/name`.

**Not used for detection:**
- `metadata.generation` — increments on any spec change, not just template changes
- `metadata.resourceVersion` — this is a stream cursor, not a rollout indicator
- `status.observedGeneration` — lags behind; only useful for completion detection

### Seed / baseline behaviour

On startup (LIST phase), the first hash for each Deployment is stored as baseline. It is never treated as a rollout. If the monitor restarts mid-rollout, the first event re-seeds the new hash as baseline and the in-progress rollout is missed. This is intentional for POC — gap detection requires the CRD persistence (production only).

**Rollout completion:** not tracked in POC. Monitor detects rollout start and dispatches; Holmes assesses health.

---

## 7. Configuration

All config via environment variables (POC):

```bash
# Cluster credentials (mounted kubeconfigs)
KUBECONFIG_DIR=/etc/kubeconfigs           # directory containing one kubeconfig per spoke cluster
# TODO: determine file naming convention and how cluster ID/name is derived

# Namespace filtering (at least one required)
NAMESPACE_ALLOWLIST=default,production,staging   # only watch these namespaces (if set, denylist is ignored)
NAMESPACE_DENYLIST=kube-system,kube-public,cattle-system,fleet-system   # watch all except these

# Dispatch
DISPATCH_MODE=holmes                      # "holmes" | "slack" | "both"
HOLMES_API_URL=http://holmes-svc:8080
SLACK_WEBHOOK_URL=https://hooks.slack.com/...

# Tuning
WORKER_COUNT=3
DEBOUNCE_SECONDS=30
QUEUE_MAX_SIZE=100
```

If `NAMESPACE_ALLOWLIST` is set, only those namespaces are watched. Otherwise, `NAMESPACE_DENYLIST` excludes listed namespaces.

---

## 8. Risk: Rancher Proxy Watch Reliability

125 concurrent long-lived watch streams through Rancher's k8s proxy (`/k8s/clusters/<id>/`). Known reliability issues:

- **nginx `proxy-read-timeout` defaults to 60s** — quiet clusters get their watch killed ([rancher/rancher#15044](https://github.com/rancher/rancher/issues/15044))
- **Streaming overload can make clusters unreachable** — P0 blocker ([rancher/rancher#37250](https://github.com/rancher/rancher/issues/37250))
- **FD exhaustion** in `cattle-cluster-agent` — fixed in Rancher v2.7.0 ([rancher/rancher#31324](https://github.com/rancher/rancher/issues/31324))
- **Watch timeout errors** repeating every 5-30s — triggers reconnects ([rancher/rancher#23957](https://github.com/rancher/rancher/issues/23957))

**Mitigations (required before production deployment):**

| Mitigation | Who | When |
|---|---|---|
| Set `proxy-read-timeout: "3600"` annotation on Rancher's nginx ingress | Ops team | Before POC |
| Set `proxy-send-timeout: "3600"` to match | Ops team | Before POC |
| If using HAProxy: `timeout tunnel 1h` | Ops team | Before POC |
| Verify Rancher version >= 2.7.0 (fd exhaustion fix) | Ops team | Before POC |
| Raise `cattle-cluster-agent` container ulimit to 65535 | Ops team | Before POC |

**Informer handles reconnect automatically.** The risk is not data loss but reconnect storms — 125 clusters re-LISTing simultaneously after a proxy restart. Add random jitter (0-30s) before reconnecting in production.

---

## 9. Error Handling

| Failure | POC behaviour |
|---|---|
| Mounted kubeconfig invalid or missing | Log error, skip cluster |
| Spoke cluster 401 (token expired) | Log error, skip cluster (requires kubeconfig remount) |
| Spoke cluster unreachable | Informer handles reconnect with exponential backoff automatically |
| Event channel full | Drop event, log warning with cluster/deployment name |
| Holmes API unreachable | Log error, retry once after 10s, then drop |
| Slack webhook failure | Log error, no retry (notification lost — acceptable for POC) |

**Production gaps:**
- No dead-letter queue for failed events
- No retry on Slack failure
- No persistent state (events lost on restart)
- Rollout completion not tracked
- Worker panics need structured error boundary (`recover()` per goroutine)

---

## 10. Data Flow: End-to-End Example

```
Developer: kubectl set image deployment/checkout checkout=v2.0 -n default
           (spoke cluster c-m-abc123 / "prod-us-east-1")

--- WATCH ---
Startup: templateCache["c-m-abc123/default/checkout"] = sha256(v1.9 template) = "aaa111"

UpdateFunc fires:
  newHash = sha256(v2.0 template) = "bbb222"  !=  oldHash "aaa111"
  -> debouncer.Submit("c-m-abc123/default/checkout", RolloutEvent{...})

30s debounce — no further events

--- DISPATCH ---
POST http://holmes-svc:8080/api/chat
{ "cluster_context": "c-m-abc123",
  "query": "Deployment checkout in namespace default on cluster
            prod-us-east-1 rolled out: checkout:v1.9 -> checkout:v2.0.
            Analyse the rollout health." }
-> Holmes investigates autonomously on spoke
```

---

## 11. Go Dependencies

```go
// go.mod
module github.com/netskope/rollout-monitor

require (
    // Core Kubernetes client + informer factory
    k8s.io/client-go                v0.29.x

    // Kubernetes API types (Deployment, etc.)
    k8s.io/api                      v0.29.x
    k8s.io/apimachinery             v0.29.x
)
```

**Production only:** add `sigs.k8s.io/controller-runtime v0.17.x` for CRD read/write.

No other external dependencies — HTTP dispatch, hashing, and concurrency use the standard library.

---

## 12. Files to Create

**Go module root:** `rollout-monitor/`

### POC files

| File | Purpose |
|---|---|
| `cmd/monitor/main.go` | Entry point: parse config, init components, start manager + dispatchers, block on signal |
| `internal/config/config.go` | Read and validate env vars, namespace filter logic |
| `internal/config/kubeconfig.go` | Load mounted kubeconfigs, build `*rest.Config` per cluster |
| `internal/watcher/manager.go` | `ClusterWatchManager` — staggered startup, one goroutine + informer per cluster |
| `internal/watcher/informer.go` | `SharedInformerFactory` setup, `UpdateFunc` hash comparison, debouncer submit |
| `internal/watcher/debouncer.go` | Per-deployment-key debounce with 30s window |
| `internal/models/event.go` | `RolloutEvent` struct |
| `internal/dispatch/dispatcher.go` | Goroutine pool consuming `chan RolloutEvent`, routes to Holmes/Slack |
| `internal/dispatch/holmes.go` | `POST /api/chat` to Holmes API |
| `internal/dispatch/slack.go` | `POST` to Slack webhook |
| `deploy/deployment.yaml` | Kubernetes Deployment + RBAC for the monitor pod |

### Test files

| File | Purpose |
|---|---|
| `internal/config/kubeconfig_test.go` | Unit tests: verify kubeconfig loading from mounted files |
| `internal/watcher/informer_test.go` | Unit tests: fake informer, verify hash comparison + event emission |
| `internal/watcher/debouncer_test.go` | Unit tests: verify debounce window, event replacement, timer reset |
| `internal/dispatch/holmes_test.go` | Unit tests: mock HTTP server, verify payload shape |
| `internal/dispatch/slack_test.go` | Unit tests: mock HTTP server, verify message format |

### Production-only files (not in POC)

| File | Purpose |
|---|---|
| `internal/persistence/crd_client.go` | Read/write `ClusterRolloutState` CRD on central cluster |
| `deploy/crd.yaml` | `ClusterRolloutState` CRD definition (`ep.netskope.com/v1alpha1`) |

---

## 13. Testing

- **Unit:** all components tested using `k8s.io/client-go/kubernetes/fake` (fake clientset) and `net/http/httptest` for HTTP mocking — no real cluster needed
- **Integration:** end-to-end with KIND cluster + mock Rancher HTTP server (`httptest.NewServer`) — create a Deployment, update image, verify `RolloutEvent` emitted and dispatcher called with correct payload
- **No LLM eval tests required** for POC (the monitor is infrastructure, not LLM logic)

---

## 14. What Is Not In Scope (POC)

- Argo Rollouts (`argoproj.io/v1alpha1/Rollout`)
- StatefulSets, DaemonSets
- Automatic Rancher cluster discovery (explicit list in config)
- CRD persistence / gap detection (production only)
- Rollout completion detection (watching until `observedGeneration` converges)
- Persistent event queue (Kafka, Redis)
- Helm chart (deploy manually with a basic manifest for POC)
- Per-spoke Prometheus / Loki integration
- Multi-Rancher instance support
- CRD write batching with jitter (production only)
- Reconnect jitter after proxy failures (production only)

---

## Appendix A: Alternative — Non-Agentic Pipeline

Fallback if Holmes cannot reach spokes or LLM costs must be zero. Replaces the dispatcher with a full worker pipeline:

### Additional Components

**`ReplicaSetFinder` (`internal/pipeline/replicaset_finder.go`)**

Find active RS by `ownerReferences[].UID`, pick highest `CreationTimestamp`.

**`LogCollector` (`internal/pipeline/log_collector.go`)**

List pods by label selector, tail 200 lines per pod (30s timeout). Also collect from failed init containers.

**Per-event pipeline:**

1. Find new ReplicaSet by Deployment owner UID
2. Wait for pods: poll 5s up to 60s until >= 1 pod is `Running` or `Terminated`
3. Collect logs, dispatch with full context

**Additional config:**
```bash
LOG_TAIL_LINES=200
POD_WAIT_TIMEOUT_SECONDS=60
```

**Additional files:**
| File | Purpose |
|---|---|
| `internal/pipeline/worker.go` | Worker pool — goroutines running the full pipeline |
| `internal/pipeline/replicaset_finder.go` | Find active ReplicaSet by owner UID |
| `internal/pipeline/log_collector.go` | Stream pod logs via `GetLogs` |
| `internal/pipeline/replicaset_finder_test.go` | Unit tests: fake clientset, verify RS selection |
| `internal/pipeline/log_collector_test.go` | Unit tests: fake pod log stream |

**Trade-off:** More code, more failure modes (RS not found, pods not ready, log timeout), but zero LLM cost and no dependency on Holmes reaching spoke clusters.
