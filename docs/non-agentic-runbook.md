# Non-Agentic Deployment Rollout Monitoring Runbook

## Overview

This runbook describes how the operator monitors Kubernetes Deployment rollouts
and gathers failure context. The operator is read-only — it never mutates
cluster state.

The process has two phases:

- **Phase 1** — Monitor the rollout and determine success or failure
- **Phase 2** — Gather failure context and post to Slack (only on failure)

---

## Workflow

Template Hash Changed
        │
        ▼
Wait for observedGeneration
        │
        ▼
Paused? ──yes──► Suspend monitoring
        │                (resume when unpaused)
        no
        │
        ▼
Observe rollout
        │
        ├──────────────► Failure Condition
        │                     │
        │                     ▼
        │                Gather Diagnostics
        │
        ▼
Rollout Complete?
        │
        ▼
Soak
        │
        ├──────────────► Regression
        │                     │
        │                     ▼
        │                Gather Diagnostics
        │
        ▼
Success


## Phase 1: Monitor Rollout

**Trigger:** Template hash change detected by the existing watcher informer.

**Supersede rule:** If a new template hash change is detected for the same
deployment while monitoring is already in progress, cancel the current
observation and start fresh from Step 1. The previous rollout is abandoned —
Kubernetes itself stops reconciling it once a newer spec is applied. No
terminal status is emitted for the cancelled observation; the new rollout
inherits monitoring responsibility.

**Polling interval:** Once triggered, the dispatcher polls the Deployment
object every 10 seconds using the kubeconfig for the cluster identified in
the `RolloutRecord`. Steps 1–5 are evaluated on each poll cycle. This is a
direct API GET against the target cluster, not an informer — the dispatcher
watches one specific Deployment per active rollout.

### Step 0 — Check deployment exists

On each poll, if the Deployment returns `404 Not Found`, the deployment
was deleted mid-rollout. Stop monitoring and classify as `DELETED` — no
diagnostics are gathered (the resources needed for Phase 2 are gone too).
Update the `RolloutRecord` status accordingly.

### Step 1 — Gate on generation match

```
Wait until:
  deployment.status.observedGeneration == deployment.metadata.generation
```

Until this is true, all status fields are stale from the previous rollout.
Do not evaluate anything else.

**Why this matters (the "Two Clocks" problem):**

```
t=0s   Deploy v2 requested         → generation=2
t=5s   Controller picks it up      → observedGeneration=2
t=10s  Pods terminating/starting   → replicas mismatched
t=60s  New pods ready              → stable
```

If you start checking at t=3s without the generation gate, you see the OLD
replica counts and may conclude it's already stable. Both fields live on
the Deployment object — no need to inspect ReplicaSets or Pods for this check.

### Step 2 — Check for paused deployment

```
IF deployment.spec.paused == true
   → PAUSED — suspend monitoring, do not evaluate further
```

A paused deployment will never converge — the controller stops reconciling
it. Without this check the monitor would keep polling until the inactivity
or absolute timeout fires and misclassify the rollout as `STALLED` or
`INCONCLUSIVE`.

The dispatcher continues polling the Deployment every 10 seconds while
paused — Step 2 simply blocks progress to Step 3 on each cycle.

**MVP:** Paused time counts against the absolute timeout. A deployment
paused longer than 10 minutes will be classified as `INCONCLUSIVE`. This
is a known false positive — acceptable for MVP.

**Post-MVP:** Time spent paused does not count against the inactivity or
absolute timeouts. The timeout clocks pause while `spec.paused` is true
and resume when unpaused. A hard upper bound of 12 hours applies to total
paused time — if the deployment remains paused beyond this, monitoring
stops and the rollout is classified as `PAUSED` with no diagnostics
gathered.

In both cases, when `spec.paused` flips to false, the next poll passes
Step 2 and monitoring continues. The generation gate (Step 1) naturally
re-synchronises as the controller resumes reconciliation.

**Mid-rollout pause:** A deployment can be paused partway through a rolling
update. In that state, replica counts are intentionally mismatched (some old,
some new) and `unavailableReplicas > 0` is expected. Treating this as a
failure would be wrong — the operator chose to pause.

### Step 3 — Check for failure conditions

On every poll, check deployment-level and pod-level signals.

**3a — Deployment condition (Kubernetes verdict)**

```
IF condition type=Progressing, status=False
   AND reason == ProgressDeadlineExceeded
   → FAILED — go to Phase 2
```

This is Kubernetes declaring the rollout failed. Definitive — act on it
immediately.

**3b — Pod-level fast-fail checks**

**Identifying the new ReplicaSet:** List ReplicaSets matching the
Deployment's label selector. The new ReplicaSet is the one whose
`metadata.ownerReferences` points to the Deployment AND whose
`metadata.annotations["deployment.kubernetes.io/revision"]` matches the
Deployment's own revision annotation. This check runs after the generation
gate (Step 1), so the controller has already observed the current spec and
set the revision accordingly.

For pods owned by the new ReplicaSet, check each container (including init
containers). Two checks, no hardcoded reason lists:

```
Check 1: Container never starts
  IF container.state.waiting != nil
     AND container.restartCount == 0
     AND now - pod.startTime > 90s
     → FAILED — go to Phase 2

Check 2: Container starts and crashes repeatedly
  IF container.restartCount >= 3
     → FAILED — go to Phase 2
```

**Why two checks instead of a reason list:**

Check 1 catches every case where the container is stuck before starting:
`InvalidImageName`, `ImagePullBackOff`, `ErrImagePull`,
`CreateContainerConfigError`, `CreateContainerError`, and any future kubelet
error reason. We don't match on reason strings — if the container has been
in `waiting` with zero restarts for 90 seconds, it's stuck. Normal startup
(`ContainerCreating`) resolves in seconds and never hits the window.

Check 2 catches every case where the container starts and crashes:
`CrashLoopBackOff` and any other crash pattern. The container did start
(`restartCount > 0`), but it keeps failing. A single crash is transient
(slow dependency, cold cache). Three crashes is a persistent loop. New
ReplicaSet pods always start with `restartCount = 0`, so the absolute
count equals the number of crashes during this rollout — no windowing or
baseline tracking needed.

The 90s confirmation window in Check 1 avoids false positives from:
- Transient registry issues (retry succeeds within seconds)
- Secrets/ConfigMaps created alongside the Deployment (appear within seconds)
- Normal image pulls on cold nodes (typically < 30s)

**Backstop:** If pod-level checks don't trigger (e.g. pod never gets
scheduled, so no `containerStatuses` exist), the deployment-level signals
still catch it: `ProgressDeadlineExceeded` (3a), stall timeout (Step 4),
or absolute timeout.

### Step 4 — Check replica convergence

```
desiredReplicas     = deployment.spec.replicas
updatedReplicas     = deployment.status.updatedReplicas
availableReplicas   = deployment.status.availableReplicas
unavailableReplicas = deployment.status.unavailableReplicas

IF updatedReplicas     == desiredReplicas
   AND availableReplicas   == desiredReplicas
   AND unavailableReplicas == 0 (or absent)
   → STABLE — go to Step 5
```

If not converged, track `lastProgressAt` — the last time the rollout moved
*forward*. Progress occurs whenever ANY of:

- `updatedReplicas`     increases  (new-version pods created)
- `availableReplicas`   increases  (pods became ready)
- `unavailableReplicas` decreases  (gap to desired closing)

What does NOT count as progress:

- Backward oscillation — `availableReplicas` dropping or `unavailableReplicas`
  rising. That's thrashing (pods crashing/restarting), not forward motion.
- Scaling down old replicas — old pods terminating lowers `availableReplicas`
  without advancing the new version. A decrease is never progress.
- `updatedReplicas` decreasing — that's a rollback or scale-down, not this
  rollout advancing.

If no forward progress for N minutes → STALLED, go to Phase 2.

### Step 5 — Soak period

After Step 4 passes, wait an additional period (e.g. 60s) and re-check:

- Replica counts still match?
- Any pod restart counts increased?
- Any pods dropped out of Ready?

```
IF all still good → SUCCESS — post to Slack
IF regression     → UNSTABLE — go to Phase 2
```

**Why:** Kubernetes counts a pod as available once it has satisfied readiness and remained Ready for at least minReadySeconds (0 by default). A soak period still helps catch failures that occur shortly after a pod becomes available.
The soak period catches post-ready failures like OOMKilled, slow memory
leaks, and crash-after-first-request scenarios.

### Timeouts

| Timeout | Duration | Meaning |
|---|---|---|
| Inactivity | 5 min no progress | Rollout is stuck, not slow |
| Absolute | 10 min total | Hard cap regardless of progress |

Both result in going to Phase 2.

**Interaction with `progressDeadlineSeconds`:** Kubernetes has its own
deadline (default 600s). Step 3a checks for `ProgressDeadlineExceeded` on
every poll before evaluating stall timeouts. If Kubernetes fires first
(short deadline), the rollout is classified as `FAILED` via Step 3a. If
our inactivity timeout fires first (default case — 5 min < 10 min
deadline), the rollout is classified as `STALLED`. Both are correct — the
distinction is whether Kubernetes rendered a verdict (`FAILED`) or we
inferred one (`STALLED`).

### What "Stable" means

A deployment is considered stable when ALL of:

1. Controller has observed the latest spec (`generation == observedGeneration`)
2. No failure conditions present
3. `updatedReplicas == desiredReplicas` (all pods on new version)
4. `availableReplicas == desiredReplicas` (all pods ready and past `minReadySeconds`)
5. `unavailableReplicas == 0`
6. Survived the soak period without regression

---

## Phase 2: Gather Failure Context

Only runs when Phase 1 result is **not** SUCCESS.

### Step 6 — Classify the failure

The core distinction is **who declared the outcome**: Kubernetes
(authoritative) or our monitor (inferred from lack of progress).

```
FAILED       — Kubernetes explicitly declared the rollout failed.
               The Progressing condition flipped to status=False with
               reason=ProgressDeadlineExceeded. Definitive — no inference.

STALLED      — Kubernetes has NOT declared failure, but we observed no
               forward progress for N minutes (inactivity timeout). Our
               call, not the controller's — the rollout may still be
               within its progress deadline.

INCONCLUSIVE — Absolute timeout hit while the rollout was still making
               forward progress. Genuinely slow, not stuck — we stopped
               watching, the controller never rendered a verdict.

UNSTABLE     — Passed the replica checks, then regressed during the soak
               period (pod restarted, dropped out of Ready, OOMKilled).

PAUSED       — deployment.spec.paused is true. The controller has stopped
               reconciling. Not a failure — monitoring is suspended until
               the deployment is unpaused, at which point monitoring
               resumes from Step 1.

DELETED      — The Deployment was deleted mid-rollout (404 on poll).
               Monitoring stops immediately. No diagnostics — the
               resources are gone.
```

`FAILED` is the only classification Kubernetes owns. `STALLED` and
`INCONCLUSIVE` are both "no verdict from the controller yet" — they differ
only in whether progress had ceased (`STALLED`) or was ongoing when our
timeout fired (`INCONCLUSIVE`). `UNSTABLE` is a post-success regression our
soak period caught that Kubernetes considers complete.

### Step 7 — Collect Kubernetes Events

List Warning-type events in the deployment's namespace, filtered to the
deployment, its ReplicaSets, and its pods.

| Event reason | What it means |
|---|---|
| `FailedCreate` | Scheduling or resource issue, quota exceeded, admission webhook rejection |
| `BackOff` | Container crash looping |
| `Failed` | Image pull error |
| `Unhealthy` | Readiness/liveness probe failure |
| `FailedScheduling` | No nodes match requirements |
| `FailedMount` / `FailedAttachVolume` | Volume mount errors (often show up here, not in application logs) |
| `InsufficientCPU` / `InsufficientMemory` | Resource limits exceeded |

### Step 8 — Inspect pod status

List pods owned by the new ReplicaSet. For each pod, check
`containerStatuses[].state.waiting.reason`:

| Reason | What it means |
|---|---|
| `ImagePullBackOff` / `ErrImagePull` | Bad image tag or registry auth failure |
| `CrashLoopBackOff` | App crashing on startup |
| `CreateContainerConfigError` | Bad ConfigMap or Secret reference |
| `OOMKilled` (in `lastState`) | Memory limit too low |

### Step 9 — Collect logs

Only for pods in `CrashLoopBackOff`, `Error`, or with restart count > 0.

**9a — Current container logs**

```
PodLogOptions:
  sinceTime:  rollout start time
  tailLines:  500
  container:  the failing container
```

For every container in every affected pod, including init containers.
Init container failures block the pod entirely and are often the root cause
(e.g. migration job failing, config validation failing).

**9b — Previous container logs**

```
PodLogOptions:
  previous:   true
  tailLines:  500
  container:  the failing container
```

Critical for crash loops. When a container has restarted, the current
container often has no output yet — it just started. The previous
container's logs contain the actual crash. Without `previous=true` you
get an empty log or just a startup banner.

**9c — Processing**

- Filter lines for: `error`, `fatal`, `panic`, `traceback`, `exception`
- Deduplicate repeated lines (report "seen N times" instead of N copies)
- Keep unfiltered logs available as attachment for full context

### Step 10 — Post to Slack

Assemble the failure report using Slack Block Kit and send via webhook.

**Message body** (Block Kit sections):
- Header: deployment name, namespace, cluster, failure classification
- Replica status: desired vs updated vs available
- Pod errors table: pod name, container, reason, restart count
- Kubernetes events: deduplicated, with occurrence count

**Log attachment:**
- If collected logs are short (< 3000 chars), inline in the message
- If longer, upload as a file attachment via `chat.postMessage` with
  `files_upload_v2` — keeps the main message scannable

**Alert deduplication:**
- Track per-workload alert state to avoid flapping
- Use a short debounce window (60-90s) before sending
- Worst case: a single duplicate alert, not a missed one

Example message:

```
Rollout FAILED: my-app in prod (cluster: eu-west-1)
Status: CrashLoopBackOff
Replicas: 3 desired, 1 updated, 0 available

Pod errors:
  my-app-7f8b9-xk2lm: CrashLoopBackOff (3 restarts)
  my-app-7f8b9-mn4pq: ImagePullBackOff

Events:
  BackOff: Back-off restarting failed container (x4)
  FailedCreate: quota exceeded (x1)

Logs (my-app-7f8b9-xk2lm, previous container):
  [FATAL] missing required env var DATABASE_URL

[Full logs attached as file]
```

---

## Summary

```
Phase 1: Is it working?
  generation gate → paused check → failure conditions → replica counts → soak

Phase 2: Why isn't it working? (only on failure)
  classify → events → pod status → logs → Slack
```

Phase 1 is cheap — one Deployment object polled every 10s.
Phase 2 is heavier (events, pods, logs) but only fires on failures.

---

## Failure Scenario Coverage

### Four buckets

Most failures collapse into four buckets:

| Bucket | What happens | Primary detection |
|---|---|---|
| Can't create pod | ReplicaSet can't create pods at all | Stall timeout (no `updatedReplicas` progress) |
| Can't schedule pod | Pod exists but stays Pending | Stall timeout (no `availableReplicas` progress) |
| Pod starts but crashes | Container runs briefly, exits, restarts | Check 2 (`restartCount >= 3`) |
| Pod runs but never becomes Ready | Container is running, probes fail | Stall timeout / `ProgressDeadlineExceeded` |

### Image / Container Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| Image pull failure (`ImagePullBackOff`, `ErrImagePull`) | `container.state.waiting`, `restartCount == 0` | **Check 1** — waiting + no restarts > 90s | ~90s |
| Image does not exist / wrong tag | Same as above — `ErrImagePull` then `ImagePullBackOff` | **Check 1** | ~90s |
| Private registry auth failure | Same — `ErrImagePull` with auth error in event message | **Check 1** | ~90s |
| Image architecture mismatch (ARM vs AMD) | Container starts, immediately crashes with exec format error | **Check 2** — `restartCount` climbs fast | ~2-3 min |
| Container exits immediately (`CrashLoopBackOff`) | `container.state.waiting.reason = CrashLoopBackOff`, `restartCount > 0` | **Check 2** — `restartCount >= 3` | ~2-3 min |
| Application startup error | Container starts, exits non-zero | **Check 2** | ~2-3 min |

### Configuration Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| Missing ConfigMap | `CreateContainerConfigError`, `restartCount == 0` | **Check 1** | ~90s |
| Missing Secret | `CreateContainerConfigError`, `restartCount == 0` | **Check 1** | ~90s |
| Invalid environment variables | Container starts, crashes on bad config | **Check 2** | ~2-3 min |
| Bad command / entrypoint | Container starts, exits immediately (exit code 1 or 127) | **Check 2** | ~2-3 min |
| Incorrect volume mounts (bad path in app) | Container starts, app crashes reading missing path | **Check 2** | ~2-3 min |
| Wrong application configuration | Container starts, app crashes or fails probes | **Check 2** or soak period | ~2-5 min |

### Resource Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| Insufficient CPU/memory on nodes | Pod stays `Pending`, `FailedScheduling` event | **Stall timeout** — no `updatedReplicas` progress | ~5 min |
| Memory limit exceeded (`OOMKilled`) | Container terminated with reason `OOMKilled`, restarts | **Check 2** if during rollout; **soak period** if after ready | ~2-3 min or ~60s post-ready |
| CPU throttling causing slow startup | Container running but slow to pass probes | **Stall timeout** or `ProgressDeadlineExceeded` | ~5-10 min |
| Pod cannot be scheduled (`Pending`) | `pod.status.phase = Pending`, no `containerStatuses` | **Stall timeout** — no `availableReplicas` progress | ~5 min |
| Resource quota exceeded | `FailedCreate` event on ReplicaSet, pod never created | **Stall timeout** — `updatedReplicas` never increases | ~5 min |

### Scheduling Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| No matching nodes | `Pending`, `FailedScheduling` event | **Stall timeout** | ~5 min |
| Node selector / affinity mismatch | `Pending`, `FailedScheduling` event | **Stall timeout** | ~5 min |
| Taints and tolerations mismatch | `Pending`, `FailedScheduling` event | **Stall timeout** | ~5 min |
| Node unavailable / NotReady | `Pending` or pod evicted | **Stall timeout** | ~5 min |
| Topology constraints cannot be satisfied | `Pending`, `FailedScheduling` event | **Stall timeout** | ~5 min |

### Health Check Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| Readiness probe failing (pod never becomes Ready) | Pod `Running` but not `Ready`, `Unhealthy` events | **Stall timeout** — `availableReplicas` never reaches desired | ~5 min |
| Liveness probe failing (container restarted) | `restartCount` increases, `Unhealthy` events | **Check 2** — `restartCount >= 3` | ~2-3 min |
| Startup probe failing (slow app killed too early) | Container terminated, `restartCount` increases | **Check 2** | ~2-3 min |

### Application Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| Application crashes on startup | `CrashLoopBackOff`, `restartCount > 0` | **Check 2** | ~2-3 min |
| Dependency unavailable (DB, API, queue) | App crashes or fails readiness probe | **Check 2** (crash) or **stall timeout** (probe fail) | ~2-5 min |
| Migration failure (init container) | Init container exits non-zero, pod stuck in `Init:CrashLoopBackOff` | **Check 2** — init container `restartCount >= 3` | ~2-3 min |
| Port not listening | Readiness probe fails, pod not `Ready` | **Stall timeout** — `availableReplicas` stuck | ~5 min |
| Invalid runtime arguments | Container starts, exits immediately | **Check 2** | ~2-3 min |
| Certificate/TLS issues | App crashes or probe fails on TLS handshake | **Check 2** (crash) or **stall timeout** (probe) | ~2-5 min |

### Storage Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| PVC not bound | Pod stays `Pending`, `FailedMount` event | **Stall timeout** | ~5 min |
| Volume mount failure | `container.state.waiting`, `FailedMount`/`FailedAttachVolume` event | **Check 1** (if container never starts) or **stall timeout** | ~90s or ~5 min |
| Storage class unavailable | PVC stays `Pending`, pod stays `Pending` | **Stall timeout** | ~5 min |
| Disk full | Container starts, crashes writing to disk | **Check 2** | ~2-3 min |

### Networking Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| Service misconfiguration | App starts fine, traffic doesn't route — no pod-level signal | **GAP** — not observable from pod/deployment status | N/A |
| DNS resolution failure | App crashes or probe fails due to DNS timeout | **Check 2** (crash) or **stall timeout** (probe) | ~2-5 min |
| Network policy blocking traffic | Probe fails (can't reach port), pod not `Ready` | **Stall timeout** | ~5 min |
| Ingress misconfiguration | No pod-level signal — ingress is a separate resource | **GAP** — not observable from pod/deployment status | N/A |

### Deployment / Rollout Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| New ReplicaSet cannot create pods | `FailedCreate` event, `updatedReplicas` stays 0 | **Stall timeout** | ~5 min |
| Old pods not terminating | `Replicas > UpdatedReplicas`, termination stuck | **Stall timeout** — `unavailableReplicas` not decreasing | ~5 min |
| Progress deadline exceeded | `Progressing=False`, `reason=ProgressDeadlineExceeded` | **Step 3a** — deployment condition check | Immediate |
| Insufficient available replicas | `availableReplicas < desired` | **Step 4** — replica convergence | Ongoing |
| Rolling update stuck | No forward progress in replica counts | **Stall timeout** | ~5 min |

### Cluster Issues

| Scenario | How Kubernetes surfaces it | Runbook detection | Timing |
|---|---|---|---|
| Node failures | Pods evicted or stuck `Pending` | **Stall timeout** | ~5 min |
| CNI / plugin failures | Pod stuck in `ContainerCreating`, network setup fails | **Check 1** — waiting > 90s | ~90s |
| Kubelet issues | No status updates, pods stuck | **Stall timeout** | ~5 min |
| API server issues | Informer disconnects, no events received | **GAP** — monitor itself may not be able to poll | N/A |
| Image registry outage | `ImagePullBackOff` across all pods | **Check 1** | ~90s |

---

### Gaps

| Gap | Why it's a gap | Mitigation |
|---|---|---|
| Service misconfiguration | Services and endpoints are separate resources — pod/deployment status looks healthy | Out of scope — would require watching Service/Endpoints resources |
| Ingress misconfiguration | Ingress is a separate resource with its own controller | Out of scope — would require watching Ingress resources |
| API server issues | If the API server is down, the monitor can't poll at all | Monitor should detect its own informer disconnect and alert on that separately |
| Scheduling issues detected late (~5 min) | No `containerStatuses` exist when pod can't be scheduled, so Check 1/2 don't apply | Could watch ReplicaSet events for `FailedCreate` to detect faster, but adds complexity |
