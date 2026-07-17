# Non-Agentic Deployment Rollout Monitoring Runbook

## Overview

This runbook describes how the operator monitors Kubernetes Deployment rollouts
and gathers failure context. The operator is read-only — it never mutates
cluster state.

The process has two phases:

- **Phase 1** — Monitor the rollout and determine success or failure
- **Phase 2** — Gather failure context and post to Slack (only on failure)

---

## Phase 1: Monitor Rollout

**Trigger:** Template hash change detected by the existing watcher informer.

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

### Step 2 — Check for failure conditions

On every poll, check `deployment.status.conditions[]`:

```
IF condition type=Progressing, status=False
   AND reason == ProgressDeadlineExceeded
   → FAILED — go to Phase 2
```

Also check for early failure signals at the pod level (faster than waiting
for the deadline):

```
For pods owned by the new ReplicaSet:
  IF containerStatuses[].state.waiting.reason IN
     (CrashLoopBackOff, ImagePullBackOff, ErrImagePull,
      CreateContainerConfigError, InvalidImageName)
     → early failure signal

  IF restart count >= threshold (default: 3) within window (default: 5 min)
     → FAILED — go to Phase 2
```

Check conditions before replica counts. A failure condition is definitive.
The restart threshold prevents declaring failure on a single transient crash
while catching persistent crash loops minutes before `ProgressDeadlineExceeded`
would fire.

### Step 3 — Check replica convergence

```
desiredReplicas     = deployment.spec.replicas
updatedReplicas     = deployment.status.updatedReplicas
availableReplicas   = deployment.status.availableReplicas
unavailableReplicas = deployment.status.unavailableReplicas

IF updatedReplicas     == desiredReplicas
   AND availableReplicas   == desiredReplicas
   AND unavailableReplicas == 0 (or absent)
   → STABLE — go to Step 4
```

If not converged, track `lastProgressAt` — the last time any of these counts
changed. If no progress for N minutes → STALLED, go to Phase 2.

### Step 4 — Soak period

After Step 3 passes, wait an additional period (e.g. 60s) and re-check:

- Replica counts still match?
- Any pod restart counts increased?
- Any pods dropped out of Ready?

```
IF all still good → SUCCESS — post to Slack
IF regression     → UNSTABLE — go to Phase 2
```

**Why:** Kubernetes marks a pod "available" as soon as the readiness probe
passes once. A pod that OOMs 45 seconds later was counted as available.
The soak period catches post-ready failures like OOMKilled, slow memory
leaks, and crash-after-first-request scenarios.

### Timeouts

| Timeout | Duration | Meaning |
|---|---|---|
| Inactivity | 5 min no progress | Rollout is stuck, not slow |
| Absolute | 10 min total | Hard cap regardless of progress |

Both result in going to Phase 2.

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

### Step 5 — Classify the failure

```
FAILED       — ProgressDeadlineExceeded condition fired
STALLED      — No progress for N minutes (inactivity timeout)
INCONCLUSIVE — Absolute timeout hit, rollout still in progress
UNSTABLE     — Passed replica checks then regressed during soak
```

### Step 6 — Collect Kubernetes Events

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

### Step 7 — Inspect pod status

List pods owned by the new ReplicaSet. For each pod, check
`containerStatuses[].state.waiting.reason`:

| Reason | What it means |
|---|---|
| `ImagePullBackOff` / `ErrImagePull` | Bad image tag or registry auth failure |
| `CrashLoopBackOff` | App crashing on startup |
| `CreateContainerConfigError` | Bad ConfigMap or Secret reference |
| `OOMKilled` (in `lastState`) | Memory limit too low |

### Step 8 — Collect logs

Only for pods in `CrashLoopBackOff`, `Error`, or with restart count > 0.

**8a — Current container logs**

```
PodLogOptions:
  sinceTime:  rollout start time
  tailLines:  500
  container:  the failing container
```

For every container in every affected pod, including init containers.
Init container failures block the pod entirely and are often the root cause
(e.g. migration job failing, config validation failing).

**8b — Previous container logs**

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

**8c — Processing**

- Filter lines for: `error`, `fatal`, `panic`, `traceback`, `exception`
- Deduplicate repeated lines (report "seen N times" instead of N copies)
- Keep unfiltered logs available as attachment for full context

### Step 9 — Post to Slack

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
  generation gate → failure conditions → replica counts → soak

Phase 2: Why isn't it working? (only on failure)
  classify → events → pod status → logs → Slack
```

Phase 1 is cheap — one Deployment object polled every 10s.
Phase 2 is heavier (events, pods, logs) but only fires on failures.

---

## Common Failure Scenarios

### Bad image tag

```
T+0s    Rollout starts, new ReplicaSet created
T+2s    Kubelet: ErrImagePull
T+30s   Kubelet: ImagePullBackOff
T+10m   ProgressDeadlineExceeded fires
```

Old pods stay up throughout (rolling update won't kill them until new pods
are ready). Detection via pod status (Step 7) is faster than waiting for
the deadline.

### OOMKill after startup

```
T+0s    Rollout starts
T+30s   All pods pass readiness → Level 0 says "complete"
T+45s   Pod 1 OOMKilled, restarts
T+60s   Pod 2 OOMKilled, restarts
T+90s   All pods in CrashLoopBackOff
```

Level 0 (no soak) would report success at T+30s. The soak period catches
this at T+90s.

### Init container failure

```
T+0s    Rollout starts
T+2s    Init container starts (e.g. DB migration)
T+5s    Init container exits with error
T+5s    Pod stuck in Init:CrashLoopBackOff
        Main containers never start
```

The pod never reaches Ready. Without init container log collection you
see "pod not ready" but not why. The init container's logs contain the
actual error (e.g. "migration failed: column already exists").

### Bad ConfigMap or Secret reference

```
T+0s    Rollout starts
T+1s    Pod created
T+1s    CreateContainerConfigError — referencing non-existent Secret
        Container never starts, no logs exist
```

No logs to collect here — the container never ran. The pod status reason
and Kubernetes events are the only signals.

### Slow rollout vs stuck rollout

Track `lastProgressAt` to distinguish these. A slow rollout keeps making
progress (replica counts change). A stuck rollout has no changes for
minutes. The inactivity timeout catches stuck rollouts without penalising
large deployments that are legitimately slow.
