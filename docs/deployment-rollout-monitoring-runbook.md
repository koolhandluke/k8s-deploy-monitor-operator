# Deployment Rollout Monitoring Runbook

## Background

This operator watches Kubernetes Deployment rollouts across clusters and, when
something goes wrong, automatically gathers failure context and posts a
diagnostic report to Slack.

---

## Overview

The operator is **read-only** — it never mutates cluster state. The monitoring
process has two phases:

- **Phase 1 — Monitor the rollout:** Poll the Deployment and determine whether
  it succeeded or failed.
- **Phase 2 — Gather failure context:** Collect events, pod status, and logs,
  then post a diagnostic report to Slack. Only runs on non-success outcomes.

Code references:
- Phase 1 + 2 implementation: `internal/diagnostic/analyzer.go`
- Orchestration (supersede, concurrency): `internal/investigation/orchestrator.go`
- Slack reporting: `internal/investigation/slack_reporter.go`

---

## Workflow

```
Template Hash Changed (watcher informer)
        |
        v
  +-- Poll Deployment (every 10s) --+
  |                                  |
  |  Deleted? --> DELETED (stop)     |
  |                                  |
  |  Generation gate passed?         |
  |     no --> wait                  |
  |     yes                          |
  |       |                          |
  |  Paused?                         |
  |     yes --> suspend (keep        |
  |             polling until        |
  |             unpaused or timeout) |
  |     no                           |
  |       |                          |
  |  Failure condition?              |
  |     yes --> FAILED               |----> Phase 2
  |     no                           |      Collect events
  |       |                          |      Inspect pod status
  |  Replicas converged?             |      Collect logs
  |     no --> check stall timeout   |      Filter PII
  |            STALLED if no         |----> Post to Slack
  |            progress for 5min     |
  |     yes                          |
  |       |                          |
  |  Soak period (60s)               |
  |     regression? --> UNSTABLE  ---|
  |     no regression --> SUCCESS    |
  |                                  |
  +---- Absolute timeout (10min) ----+
         |
         v
    INCONCLUSIVE (still progressing)
    STALLED (no progress)
    PAUSED (paused at timeout)
```

---

## Phase 1: Monitor Rollout

**Trigger:** Template hash change detected by the existing watcher informer.
The dispatcher receives a `RolloutEvent` and starts polling.

**Supersede rule:** If a new template hash change arrives for the same
deployment while monitoring is in progress, the orchestrator cancels the current
investigation and starts a new one. The previous rollout is abandoned — no
terminal status is emitted for it.

> Code: `orchestrator.go:53-97` — `Investigate()` cancels existing context via
> `active[key]` before starting a new goroutine.

**Polling interval:** 10 seconds (`AnalyzerConfig.PollInterval`). Each cycle
is a direct `GET` on the Deployment object — not an informer. The analyzer
watches one specific Deployment per active investigation.

### Deletion check

On each poll, if the Deployment returns `404 Not Found`, classify as `DELETED`
and stop. No diagnostics are gathered — the resources are gone.

> Code: `analyzer.go:147-149`

### Generation gate

```
Wait until:
  deployment.status.observedGeneration >= deployment.metadata.generation
```

Until this is true, all status fields are stale from the previous rollout. Do
not evaluate anything else. The generation mismatch resets the progress timer
(the controller hasn't caught up yet — that's not a stall).

**Why this matters (the "Two Clocks" problem):**

```
t=0s   Deploy v2 requested         -> generation=2
t=5s   Controller picks it up      -> observedGeneration=2
t=10s  Pods terminating/starting   -> replicas mismatched
t=60s  New pods ready              -> stable
```

If you check at t=3s without the gate, you see the OLD replica counts and may
conclude it's already stable.

> Code: `analyzer.go:157-166`

### Paused check

```
IF deployment.spec.paused == true
   -> suspend monitoring, do not evaluate further
```

A paused deployment will never converge. Without this check, the monitor would
poll until a timeout fires and misclassify as `STALLED` or `INCONCLUSIVE`.

Polling continues every 10 seconds while paused — the check simply blocks
progress to the failure/convergence steps on each cycle.

**MVP behaviour:** Paused time counts against the absolute timeout. A deployment
paused longer than 10 minutes is classified as `PAUSED` (the code checks
`lastSeenPaused` at absolute timeout). This is a known false positive —
acceptable for MVP.

**Post-MVP:** Paused time should not count against the inactivity or absolute
timeouts. The timeout clocks pause while `spec.paused` is true and resume when
unpaused. A hard upper bound of 12 hours applies to total paused time — if the
deployment remains paused beyond this, monitoring stops and the rollout is
classified as `PAUSED` with no diagnostics gathered.

**Mid-rollout pause:** A deployment can be paused partway through a rolling
update. Replica counts are intentionally mismatched in that state — treating it
as a failure would be wrong.

> Code: `analyzer.go:169-176`

### Failure conditions

On every poll after the generation gate and paused check, evaluate
deployment-level and pod-level signals.

**Deployment condition (Kubernetes verdict)**

```
IF condition type=Progressing, status=False
   AND reason == ProgressDeadlineExceeded
   -> FAILED
```

This is Kubernetes declaring the rollout failed. Definitive — act on it
immediately.

> Code: `analyzer.go:228-237`

**Pod-level fast-fail checks**

**Identifying the new ReplicaSet:** List ReplicaSets matching the Deployment's
label selector. The new ReplicaSet is the one whose `ownerReferences` points to
the Deployment AND whose `deployment.kubernetes.io/revision` annotation matches
the Deployment's own revision annotation.

> Code: `analyzer.go:601-623` — `findNewReplicaSet()`

For pods owned by the new ReplicaSet, check each container (including init
containers):

```
Check 1: Container never starts
  IF container.state.waiting != nil
     AND container.state.waiting.reason != ""
     AND container.state.waiting.reason != "ContainerCreating"
     AND container.restartCount == 0
     AND time since analyzer first observed this state > 90s
     -> FAILED

Check 2: Containers crash repeatedly (aggregate)
  IF sum of restartCount across ALL containers in ALL pods >= 3
     AND this threshold was crossed within the restart window (5 min)
     -> FAILED
```

**Why two checks instead of a reason list:**

Check 1 catches every case where a container is stuck before starting:
`InvalidImageName`, `ImagePullBackOff`, `ErrImagePull`,
`CreateContainerConfigError`, `CreateContainerError`, and any future kubelet
error. We don't match on reason strings — if the container has been in `waiting`
with zero restarts for 90 seconds, it's stuck.

`ContainerCreating` is explicitly excluded from Check 1. It means the kubelet
is actively working (pulling image, setting up volumes/network) — not stuck.
Multi-GB images (ML workloads) regularly exceed 90s for a normal pull. The
stall timeout and `ProgressDeadlineExceeded` still catch cases where
`ContainerCreating` hangs indefinitely.

Check 2 catches crash loops. The threshold is aggregate across containers and
pods — a single pod with 3 restarts, or three pods each with 1 restart, both
trigger it. The restart window prevents stale restart counts from a previous
rollout from triggering a false positive (new ReplicaSet pods start at 0, but
the window adds a time bound).

The 90s confirmation window in Check 1 avoids false positives from:
- Transient registry issues (retry succeeds within seconds)
- Secrets/ConfigMaps created alongside the Deployment (appear within seconds)
- Normal image pulls on cold nodes (typically < 30s)

**Backstop:** If pod-level checks don't trigger (e.g. pod never gets
scheduled, so no `containerStatuses` exist), the deployment-level signals still
catch it: `ProgressDeadlineExceeded`, stall timeout, or absolute timeout.

> Code: `analyzer.go:219-305` — `checkFailureConditions()`

### Replica convergence

```
desiredReplicas     = deployment.spec.replicas
updatedReplicas     = deployment.status.updatedReplicas
availableReplicas   = deployment.status.availableReplicas
unavailableReplicas = deployment.status.unavailableReplicas

IF updatedReplicas     == desiredReplicas
   AND availableReplicas   == desiredReplicas
   AND unavailableReplicas == 0 (or absent)
   -> converged, enter soak period
```

If not converged, track `lastProgressAt` — the last time the rollout moved
*forward*. Progress occurs whenever ANY of:

- `updatedReplicas` increases (new-version pods created)
- `availableReplicas` increases (pods became ready)
- `unavailableReplicas` decreases (gap to desired closing)

What does NOT count as progress:

- Backward movement — `availableReplicas` dropping or `unavailableReplicas`
  rising (thrashing, not forward motion)
- `updatedReplicas` decreasing (rollback or scale-down)

If no forward progress for 5 minutes (inactivity timeout) -> `STALLED`.

> Code: `analyzer.go:71-88` — `progressState.recordProgress()`,
> `analyzer.go:183-215` — convergence check

### Soak period

After replicas converge, wait 60 seconds (`AnalyzerConfig.SoakPeriod`), then
re-check:

1. Replica counts still match (updated == desired, available == desired,
   unavailable == 0)?
2. Any container restart counts increased compared to pre-soak snapshot?
3. Any pods dropped out of Ready?

```
IF all still good -> SUCCESS
IF regression     -> UNSTABLE
```

**Why:** Kubernetes considers a pod available once it passes readiness and
`minReadySeconds`. The soak period catches post-ready failures like OOMKilled,
slow memory leaks, and crash-after-first-request scenarios.

> Code: `analyzer.go:309-398` — `soak()`

### Timeouts

| Timeout | Duration | Classification |
|---|---|---|
| Inactivity | 5 min no forward progress | `STALLED` |
| Absolute | 10 min total | `INCONCLUSIVE` (if still progressing), `STALLED` (if not), `PAUSED` (if paused) |

**Interaction with `progressDeadlineSeconds`:** Kubernetes has its own deadline
(default 600s). The failure condition check catches `ProgressDeadlineExceeded`
on every poll before evaluating stall timeouts. If Kubernetes fires first
(short deadline), the rollout is `FAILED`. If our inactivity timeout fires
first (default case — 5 min < 10 min deadline), the rollout is `STALLED`.

> Code: `analyzer.go:117-143`

### What "Stable" means

A deployment is stable when ALL of:

1. Controller has observed the latest spec (`observedGeneration >= generation`)
2. No failure conditions present
3. `updatedReplicas == desiredReplicas` (all pods on new version)
4. `availableReplicas == desiredReplicas` (all pods ready and past `minReadySeconds`)
5. `unavailableReplicas == 0`
6. Survived the soak period without regression

---

## Phase 2: Gather Failure Context

Only runs when Phase 1 result is **not** `SUCCESS`.

> Code: `analyzer.go:106-109` — conditional call to `gatherDiagnostics()`

### Failure classification

The core distinction is **who declared the outcome**: Kubernetes
(authoritative) or our monitor (inferred).

| Classification | Source | Meaning |
|---|---|---|
| `FAILED` | Kubernetes | `Progressing=False, reason=ProgressDeadlineExceeded`, or pod-level fast-fail (Check 1/2) |
| `STALLED` | Monitor | No forward progress for 5 min. Our call — the controller hasn't rendered a verdict. |
| `INCONCLUSIVE` | Monitor | Absolute timeout while still making forward progress. Genuinely slow, not stuck. |
| `UNSTABLE` | Monitor | Passed replica convergence, then regressed during soak (restart, pod not Ready). |
| `PAUSED` | Monitor | `deployment.spec.paused` was true when absolute timeout fired. |
| `DELETED` | Kubernetes | Deployment returned 404 during polling. No diagnostics gathered. |

> Code: `internal/diagnostic/report.go:9-27` — `Result` constants

### Collect Kubernetes events

List Warning-type events in the deployment's namespace, filtered by deployment
name prefix (matches events for the Deployment, its ReplicaSets, and its Pods
since they share the name prefix).

| Event reason | What it means |
|---|---|
| `FailedCreate` | Scheduling/resource issue, quota exceeded, admission webhook rejection |
| `BackOff` | Container crash looping |
| `Failed` | Image pull error |
| `Unhealthy` | Readiness/liveness probe failure |
| `FailedScheduling` | No nodes match requirements |
| `FailedMount` / `FailedAttachVolume` | Volume mount errors |
| `InsufficientCPU` / `InsufficientMemory` | Resource limits exceeded |

> Code: `analyzer.go:480-507` — `collectEvents()`

### Inspect pod status

List pods owned by the new ReplicaSet. For each pod, record status of both init
containers and regular containers: phase, waiting reason, restart count, ready
state.

| Reason | What it means |
|---|---|
| `ImagePullBackOff` / `ErrImagePull` | Bad image tag or registry auth failure |
| `CrashLoopBackOff` | App crashing on startup |
| `CreateContainerConfigError` | Bad ConfigMap or Secret reference |
| `OOMKilled` (in `lastState`) | Memory limit too low |

> Code: `analyzer.go:401-476` — `gatherDiagnostics()`

### Collect logs

Logs are collected from pods identified as failing by `isPodFailing()`, which
checks for: `restartCount > 0`, specific waiting reasons (`CrashLoopBackOff`,
`ImagePullBackOff`, `ErrImagePull`, `CreateContainerConfigError`,
`InvalidImageName`), or terminated with non-zero exit code.

> Code: `analyzer.go:662-679` — `isPodFailing()`

**Current container logs**

```
PodLogOptions:
  sinceTime:  rollout start time
  tailLines:  500
  container:  the failing container
```

For every container in every affected pod, including init containers. Init
container failures block the pod entirely and are often the root cause.

**Previous container logs**

```
PodLogOptions:
  previous:   true
  tailLines:  500
  container:  the failing container
```

Critical for crash loops. When a container has restarted, the current container
often has no output yet. The previous container's logs contain the actual crash.

**Log processing**

- Filter lines for: `error`, `fatal`, `panic`, `traceback`, `exception`
  (case-insensitive)
- Deduplicate repeated lines (annotate with "seen N times")
- Only error-matching lines are included in the report; total line count is
  reported for context

> Code: `analyzer.go:510-598` — `collectContainerLogs()`, `fetchLogs()`
> Code: `analyzer.go:682-710` — `filterErrorLines()`

### PII redaction

Before the report is assembled, all text fields that may contain application
output (log lines, event messages) are passed through a PII filter. The filter
redacts:

- **Email addresses** — `user@example.com` -> `[EMAIL]`
- **IP addresses** — IPv4 patterns -> `[IP]` (excludes common Kubernetes
  internal IPs like `10.x.x.x` cluster ranges when they appear in event
  object references)
- **Bearer/auth tokens** — `Bearer <token>` or `token=<value>` -> `[TOKEN]`
- **Passwords in connection strings** — `password=secret` -> `password=[REDACTED]`
- **AWS access keys** — `AKIA...` patterns -> `[AWS_KEY]`

**TODO:** PII filtering is not yet implemented in the codebase. This section
describes the target behaviour. Track implementation in the `diagnostic`
package as a `redactPII(string) string` function applied in
`slack_reporter.go` before assembling the Slack payload.

### Post to Slack

Assemble the report using Slack Block Kit and send via webhook.

**Message structure** (Block Kit sections):
- **Header:** emoji + result classification + deployment name
- **Context:** namespace, cluster, duration, failure reason
- **Pod errors:** pod name, container (init prefix if applicable), reason,
  restart count
- **Events:** deduplicated Warning events with occurrence count
- **Logs:** per container, labelled as current/previous, truncated at 3000
  characters

**Success messages** are a simple single-line text payload (no Block Kit).

**Log truncation:** Logs exceeding 3000 characters are truncated inline with a
note showing total line count. No file attachment upload is used.

> Code: `slack_reporter.go` — `PostReport()`, `failureMessage()`,
> `successMessage()`

**Example failure message:**

```
:x: Rollout FAILED: my-app

Namespace: default | Cluster: eu-west-1
Duration: 2m30s
Reason: CrashLoopBackOff persisted for 90s in pod my-app-7f8b9-xk2lm container app

Pod Errors:
  my-app-7f8b9-xk2lm app: CrashLoopBackOff (restarts: 3)
  my-app-7f8b9-mn4pq (init) migrate: Error (restarts: 1)

Events:
  Pod/my-app-7f8b9-xk2lm BackOff: Back-off restarting failed container (x4)
  ReplicaSet/my-app-7f8b9 FailedCreate: quota exceeded (x1)

Logs my-app-7f8b9-xk2lm app [previous]:
  [FATAL] missing required env var DATABASE_URL
```

---

## Summary

```
Phase 1: Is it working?
  deletion check -> generation gate -> paused check ->
  failure conditions -> replica convergence -> soak

Phase 2: Why isn't it working? (only on non-success)
  classify -> events -> pod status -> logs -> PII filter -> Slack
```

Phase 1 is cheap — one Deployment GET every 10s.
Phase 2 is heavier (events, pods, logs) but only fires on failures.

---

## Failure Scenario Coverage

### Four buckets

| Bucket | What happens | Primary detection |
|---|---|---|
| Can't create pod | ReplicaSet can't create pods at all | Stall timeout (no `updatedReplicas` progress) |
| Can't schedule pod | Pod exists but stays Pending | Stall timeout (no `availableReplicas` progress) |
| Pod starts but crashes | Container runs briefly, exits, restarts | Check 2 (aggregate `restartCount >= 3`) |
| Pod runs but never becomes Ready | Container is running, probes fail | Stall timeout / `ProgressDeadlineExceeded` |

### Image / container issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| Image pull failure | `container.state.waiting`, `restartCount == 0` | Check 1 — waiting > 90s | ~90s |
| Image does not exist / wrong tag | `ErrImagePull` then `ImagePullBackOff` | Check 1 | ~90s |
| Private registry auth failure | `ErrImagePull` with auth error in event | Check 1 | ~90s |
| Architecture mismatch (ARM vs AMD) | Immediate crash, exec format error | Check 2 — restarts climb fast | ~2-3 min |
| Container exits immediately (`CrashLoopBackOff`) | `waiting.reason = CrashLoopBackOff`, `restartCount > 0` | Check 2 | ~2-3 min |
| Application startup error | Container starts, exits non-zero | Check 2 | ~2-3 min |

### Configuration issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| Missing ConfigMap | `CreateContainerConfigError`, `restartCount == 0` | Check 1 | ~90s |
| Missing Secret | `CreateContainerConfigError`, `restartCount == 0` | Check 1 | ~90s |
| Invalid environment variables | Container crashes on bad config | Check 2 | ~2-3 min |
| Bad command / entrypoint | Container exits immediately (exit 1/127) | Check 2 | ~2-3 min |
| Incorrect volume mounts (bad path) | App crashes reading missing path | Check 2 | ~2-3 min |
| Wrong application configuration | App crashes or fails probes | Check 2 or soak | ~2-5 min |

### Resource issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| Insufficient CPU/memory on nodes | Pod stays Pending, `FailedScheduling` | Stall timeout | ~5 min |
| OOMKilled | Container terminated, restarts | Check 2 (during rollout); soak (after ready) | ~2-3 min or ~60s post-ready |
| CPU throttling (slow startup) | Running but slow to pass probes | Stall timeout / `ProgressDeadlineExceeded` | ~5-10 min |
| Pod cannot be scheduled | `pod.status.phase = Pending`, no `containerStatuses` | Stall timeout | ~5 min |
| Resource quota exceeded | `FailedCreate` event, pod never created | Stall timeout | ~5 min |

### Scheduling issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| No matching nodes | Pending, `FailedScheduling` | Stall timeout | ~5 min |
| Node selector / affinity mismatch | Pending, `FailedScheduling` | Stall timeout | ~5 min |
| Taints and tolerations mismatch | Pending, `FailedScheduling` | Stall timeout | ~5 min |
| Node unavailable / NotReady | Pending or pod evicted | Stall timeout | ~5 min |
| Topology constraints unsatisfied | Pending, `FailedScheduling` | Stall timeout | ~5 min |

### Health check issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| Readiness probe failing (never Ready) | Running but not Ready, `Unhealthy` events | Stall timeout | ~5 min |
| Liveness probe failing (restarted) | `restartCount` increases, `Unhealthy` events | Check 2 | ~2-3 min |
| Startup probe failing (killed early) | Container terminated, `restartCount` increases | Check 2 | ~2-3 min |

### Application issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| Crashes on startup | `CrashLoopBackOff`, `restartCount > 0` | Check 2 | ~2-3 min |
| Dependency unavailable (DB, API) | Crashes or fails readiness probe | Check 2 (crash) or stall timeout (probe) | ~2-5 min |
| Migration failure (init container) | Init container exits non-zero, `Init:CrashLoopBackOff` | Check 2 — init container restarts | ~2-3 min |
| Port not listening | Readiness probe fails, not Ready | Stall timeout | ~5 min |
| Invalid runtime arguments | Exits immediately | Check 2 | ~2-3 min |
| Certificate/TLS issues | Crashes or probe fails on TLS handshake | Check 2 (crash) or stall timeout (probe) | ~2-5 min |

### Storage issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| PVC not bound | Pod Pending, `FailedMount` event | Stall timeout | ~5 min |
| Volume mount failure | Waiting, `FailedMount`/`FailedAttachVolume` | Check 1 (if never starts) or stall timeout | ~90s or ~5 min |
| Storage class unavailable | PVC Pending, pod Pending | Stall timeout | ~5 min |
| Disk full | Container crashes writing to disk | Check 2 | ~2-3 min |

### Networking issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| Service misconfiguration | No pod-level signal — traffic doesn't route | **Gap** | N/A |
| DNS resolution failure | Crashes or probe fails on DNS timeout | Check 2 (crash) or stall timeout (probe) | ~2-5 min |
| Network policy blocking traffic | Probe fails, pod not Ready | Stall timeout | ~5 min |
| Ingress misconfiguration | No pod-level signal — separate resource | **Gap** | N/A |

### Deployment / rollout issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| New RS cannot create pods | `FailedCreate` event, `updatedReplicas` stays 0 | Stall timeout | ~5 min |
| Old pods not terminating | Termination stuck | Stall timeout | ~5 min |
| Progress deadline exceeded | `Progressing=False, ProgressDeadlineExceeded` | Deployment condition check | Immediate |
| Rolling update stuck | No forward progress in replica counts | Stall timeout | ~5 min |

### Cluster issues

| Scenario | How Kubernetes surfaces it | Detection | Timing |
|---|---|---|---|
| Node failures | Pods evicted or stuck Pending | Stall timeout | ~5 min |
| CNI / plugin failures | Pod stuck in `ContainerCreating` | Stall timeout / `ProgressDeadlineExceeded` | ~5-10 min |
| Kubelet issues | No status updates, pods stuck | Stall timeout | ~5 min |
| API server issues | Informer disconnects, no events | **Gap** — monitor can't poll | N/A |
| Image registry outage | `ImagePullBackOff` across all pods | Check 1 | ~90s |

---

## Known Gaps

| Gap | Why | Mitigation |
|---|---|---|
| Service misconfiguration | Services/endpoints are separate resources — pod status looks healthy | Out of scope — would require watching Service/Endpoints |
| Ingress misconfiguration | Ingress has its own controller and resource type | Out of scope — would require watching Ingress |
| API server outage | If the API server is down, the monitor can't poll | Monitor should detect informer disconnect and alert separately |
| Scheduling detected late (~5 min) | No `containerStatuses` when pod can't be scheduled, so Check 1/2 don't apply | Could watch ReplicaSet events for `FailedCreate` to detect faster |
| PII in logs | Application logs may contain sensitive data | PII filter (described above, not yet implemented) |
