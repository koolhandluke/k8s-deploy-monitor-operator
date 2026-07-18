# Async Diagnostic Target — Design Spec

## Overview

The `AsyncDiagnosticTarget` adds rollout outcome tracking to the monitor. When a template hash change is detected, it runs the [non-agentic runbook](non-agentic-runbook.md) asynchronously to determine whether the rollout succeeded or failed, then gathers failure context.

## Problem

The monitor detects rollouts (template hash changes) and fires events, but has no visibility into whether those rollouts succeed or fail. Operators learn about failures through separate alerting channels with no correlation to the detection event.

## Design

### Package: `internal/diagnostic`

Four files implementing the runbook as a dispatch target:

```
internal/diagnostic/
├── report.go      # Result enum, DiagnosticReport, K8sEvent, PodStatus, LogSnippet types
├── registry.go    # ClusterRegistry: clusterID → rest.Config mapping
├── analyzer.go    # RolloutAnalyzer: two-phase runbook implementation
├── target.go      # AsyncDiagnosticTarget: dispatch.Target with bounded concurrency
└── analyzer_test.go
```

### Data flow

```
Dispatcher → AsyncDiagnosticTarget.Dispatch()
                 │
                 ├─ semaphore full? → drop with warning
                 │
                 └─ acquire semaphore → goroutine
                         │
                         ├─ Phase 1: Monitor rollout (poll every 10s)
                         │     ├─ Gate on observedGeneration == generation
                         │     ├─ Check ProgressDeadlineExceeded
                         │     ├─ Check pod failure signals (InvalidImageName, CreateContainerConfigError, restarts)
                         │     ├─ Check replica convergence
                         │     └─ Soak period (60s) for post-ready regression
                         │
                         ├─ Phase 2: Gather diagnostics (non-SUCCESS only)
                         │     ├─ Warning events from namespace
                         │     ├─ Pod statuses from new ReplicaSet
                         │     └─ Filtered container logs (current + previous)
                         │
                         └─ Log DiagnosticReport via slog
```

### Result classifications

| Result | Meaning | Source |
|--------|---------|--------|
| `SUCCESS` | Rollout converged and survived soak period | Monitor |
| `FAILED` | Kubernetes declared failure (ProgressDeadlineExceeded) or terminal pod condition | K8s / Monitor |
| `STALLED` | No forward progress for 5 minutes | Monitor |
| `INCONCLUSIVE` | Absolute timeout (10 min) hit while still making progress | Monitor |
| `UNSTABLE` | Converged, then regressed during soak (restart, pod dropped Ready) | Monitor |

### Concurrency model

- `Dispatch()` returns `nil` immediately — analysis runs in a background goroutine
- Bounded by a semaphore channel (default capacity: 10)
- Full semaphore → event dropped with warning log (no backpressure on dispatcher)
- Goroutines use `context.Background()` — in-flight analyses survive dispatcher shutdown

### Cluster access

`ClusterRegistry` holds `rest.Config` per cluster ID, built from `[]config.ClusterInfo` at startup. The analyzer calls `ClientsetFor(clusterID)` to build a fresh `kubernetes.Interface` per analysis.

The registry has an `Update()` method for future rescan support (thread-safe via `sync.RWMutex`).

### Timeouts

| Timeout | Duration | Behavior |
|---------|----------|----------|
| Poll interval | 10s | How often deployment status is checked |
| Inactivity | 5 min | No forward progress → `STALLED` |
| Absolute | 10 min | Hard cap → `STALLED` or `INCONCLUSIVE` |
| Soak period | 60s | Post-convergence regression check |
| Config error confirm | 90s | CreateContainerConfigError must persist before fast-fail |
| Restart threshold | 3 restarts | Within the progress window → `FAILED` |

### Log collection

- Fetches current logs (since rollout start, tail 500 lines) and previous container logs (tail 500 lines)
- Covers init containers — often the actual root cause (migration failures, config validation)
- Filters for: `error`, `fatal`, `panic`, `traceback`, `exception`
- Deduplicates identical lines (reports "seen N times")

## Configuration

| Env var | Default | Notes |
|---------|---------|-------|
| `DIAGNOSTIC_ENABLED` | `false` | Enable async rollout diagnostics |
| `DIAGNOSTIC_MAX_CONCURRENT` | `10` | Max concurrent analyses |

## Modified files

| File | Change |
|------|--------|
| `internal/config/config.go` | Added `DiagnosticEnabled`, `DiagnosticMaxConcurrent` fields |
| `internal/dispatch/dispatcher.go` | Added `AddTarget()` method for external target registration |
| `cmd/monitor/main.go` | Builds registry, analyzer, target; registers with dispatcher |

## Future work

- **Slack/webhook reporting:** Send `DiagnosticReport` to Slack with Block Kit formatting (the runbook's Step 9)
- **CRD persistence:** `DiagnosticRecord` CRD for audit trail
- **Graceful drain:** Replace `context.Background()` with a managed context and drain on shutdown
- **Registry rescan:** Update `ClusterRegistry` when the manager rescans kubeconfig directory
