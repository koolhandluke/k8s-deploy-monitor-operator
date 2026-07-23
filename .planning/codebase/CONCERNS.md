# Codebase Concerns

**Analysis Date:** 2026-07-23

## Tech Debt

**Committed binaries in repo root:**
- Issue: Two large Go binaries (`monitor` at 61MB, `dispatcher` at 46MB) are tracked in git at the repo root. `.gitignore` only ignores `rollout-monitor` (not `monitor` or `dispatcher`).
- Files: `/monitor`, `/dispatcher`, `.gitignore`
- Impact: Bloats repo clone size by ~107MB. Every clone downloads these binaries. Git history permanently stores them.
- Fix approach: Add `monitor` and `dispatcher` to `.gitignore`, remove them with `git rm --cached monitor dispatcher`, and force-push or use BFG to clean history.

**go.mod requires nonexistent Go version:**
- Issue: `go.mod` declares `go 1.25.6` which does not exist. CLAUDE.md documents this as a known issue (previously the Dockerfile used `golang:1.22-alpine` but has been updated to `golang:1.25-alpine` which also does not exist).
- Files: `go.mod` (line 3), `Dockerfile` (line 1)
- Impact: Docker builds fail when using standard Go images. Local builds work because the local Go toolchain ignores the directive if it is newer. CI would break.
- Fix approach: Set `go.mod` to a real Go version (e.g., `go 1.22`) and match the Dockerfile base image (`golang:1.22-alpine`).

**Legacy diagnostic path still wired in:**
- Issue: `cmd/monitor/main.go` lines 197-204 have a "legacy diagnostic target (backward compat)" branch for `DiagnosticEnabled` that creates an `AsyncDiagnosticTarget` separately from the newer `InvestigationMode` system. The config also has backward-compat mapping in `applyDefaults()` (lines 238-247) that translates old `DISPATCH_MODE` values to `InvestigationMode`.
- Files: `cmd/monitor/main.go` (lines 197-204), `internal/config/config.go` (lines 238-247), `internal/diagnostic/target.go`
- Impact: Two parallel code paths for the same feature. Maintenance burden and confusion about which path is active. The backward-compat mapping uses `slog.Warn` but the deprecation is never enforced.
- Fix approach: Set a timeline to remove `DiagnosticEnabled` and the `DISPATCH_MODE` backward-compat mapping. Migrate any users to `investigationMode` config.

**No persistence tests:**
- Issue: `internal/persistence/` has zero test files. `HashStore` (batched flush with re-queue on failure), `AuditRecorder` (CRD creation and status updates), and `sanitizeName` are all untested.
- Files: `internal/persistence/hash_store.go`, `internal/persistence/audit_recorder.go`, `internal/persistence/names.go`
- Impact: The flush/re-queue logic in `HashStore.flush()` is non-trivial (re-queuing failed updates without overwriting newer pending values) and has no coverage. Bugs here cause silent data loss of hash state, which would trigger false rollout detections on restart.
- Fix approach: Add unit tests using `fake` controller-runtime client. Test flush, re-queue, create-vs-update paths, and `sanitizeName` edge cases.

**No cmd/ tests:**
- Issue: Neither `cmd/monitor/` nor `cmd/dispatcher/` have test files. The wiring in `main.go` (313 lines for monitor, 140 for dispatcher) is validated only by manual testing.
- Files: `cmd/monitor/main.go`, `cmd/dispatcher/main.go`
- Impact: Configuration validation, target registration order, and shutdown sequencing are untested. Integration issues (e.g., wrong shutdown order) can only be caught in production.
- Fix approach: Extract wiring into a `run()` function that accepts dependencies and add integration-style tests.

**Debouncer.Stop() drops pending events:**
- Issue: `Debouncer.Stop()` cancels all pending timers and deletes pending events without emitting them. On graceful shutdown, any events still in the debounce window are silently lost.
- Files: `internal/watcher/debouncer.go` (lines 79-88)
- Impact: Rollouts detected within `DEBOUNCE_SECONDS` of shutdown are never dispatched. With the default 30s debounce, this is a meaningful window.
- Fix approach: Add a `Flush()` method that emits all pending events immediately, and call it during shutdown before `Stop()`.

## Known Bugs

**Shutdown ordering: cancel() called after close(eventCh):**
- Symptoms: In `cmd/monitor/main.go` lines 275-286, shutdown calls `manager.Stop()`, `close(eventCh)`, `dispatcher.Wait()`, then `cancel()`. But `cancel()` is the context cancel that the `HashStore.FlushLoop` and `ConfigWatcher` goroutines depend on. The `FlushLoop` does a final flush on context cancellation (line 94 of hash_store.go), but by the time `cancel()` is called, the event channel is already closed and dispatcher workers have drained. Meanwhile, the `FlushLoop` goroutine started with `go hashStore.FlushLoop(ctx, 5*time.Second)` is still running and might try to flush while the manager is already stopped.
- Files: `cmd/monitor/main.go` (lines 275-286), `internal/persistence/hash_store.go` (lines 86-100)
- Trigger: Graceful shutdown via SIGTERM/SIGINT.
- Workaround: In practice the flush loop's final flush uses `context.Background()` and the controller-runtime client should still work, so this is unlikely to cause actual data loss. But the ordering is logically incorrect.

**Orchestrator cleanup has dead code in defer:**
- Symptoms: In `internal/investigation/orchestrator.go` lines 103-118, the defer cleanup checks `if cancel, ok := o.active[key]; ok && cancel == nil` (line 106) which will never be true because the value stored at line 92 is always non-nil (`invCancel`). Then it immediately checks `if _, ok := o.active[key]; ok` (line 110) and deletes — making the first check dead code.
- Files: `internal/investigation/orchestrator.go` (lines 103-118)
- Trigger: Every investigation completion.
- Workaround: The second check handles cleanup correctly, so this is cosmetic.

**Holmes result classification is naive:**
- Symptoms: `internal/investigation/holmes.go` lines 116-120 classify Holmes results by checking if the response contains "fail", "error", or "crash" as substrings. This will false-positive on responses like "No errors detected" or "The deployment did not fail."
- Files: `internal/investigation/holmes.go` (lines 114-120)
- Trigger: Any Holmes response mentioning these words in a negative context.
- Workaround: None. Users see incorrect `ResultFailed` classifications.

## Security Considerations

**RBAC too narrow for diagnostic features:**
- Risk: `deploy/deployment.yaml` ClusterRole only grants `get/list/watch` on `apps/deployments`. But when `InvestigationMode` is enabled, the `RolloutAnalyzer` also needs: `get/list` on `apps/replicasets`, `get/list` on `core/pods`, `get` on `core/pods/log`, and `list` on `core/events`. The deployment manifest does not include these permissions.
- Files: `deploy/deployment.yaml` (lines 17-20), `internal/diagnostic/analyzer.go` (lines 607-651, 486-513, 516-604)
- Current mitigation: None. The diagnostic features will get 403 errors at runtime if the ClusterRole is applied as-is.
- Recommendations: Add the required RBAC rules to `deploy/deployment.yaml` or create a separate ClusterRole for the diagnostic feature. Also need CRD read/write permissions when `PERSISTENCE_ENABLED=true` (`deploy-monitor.io` API group).

**No health/readiness probes in deployment manifest:**
- Risk: The Kubernetes deployment has no liveness or readiness probes. If the process hangs (e.g., deadlock in informer cache sync), Kubernetes will not restart it.
- Files: `deploy/deployment.yaml`
- Current mitigation: The status API server (port 8081) exists when trace mode is enabled, but it is not configured as a probe endpoint and is only active with `TRACE=true`.
- Recommendations: Add a `/healthz` endpoint that checks watcher health status, and configure it as a liveness probe. Add a `/readyz` endpoint for readiness.

**Slack webhook URLs and bot tokens in config file:**
- Risk: The YAML config file at `/etc/rollout-monitor/config.yaml` can contain `slackWebhookURL` and `slackBotToken` directly. If this config is stored in a ConfigMap (not a Secret), these values are visible in plain text.
- Files: `internal/config/config.go` (lines 47-48, struct fields)
- Current mitigation: Env var overrides exist for these fields (lines 131-138), allowing Secret-based injection. The `applyEnvOverrides` doc comment mentions "supports Secret mounts."
- Recommendations: Document that sensitive fields should use env var overrides from Kubernetes Secrets, never be placed directly in the config file.

## Performance Bottlenecks

**LoadDirectorySnapshot called on every retry tick:**
- Problem: During `reconcile()` Phase 3, every pending retry calls `config.LoadDirectorySnapshot(m.kubeconfigDir)` which reads and parses all kubeconfig files in the directory. With the reconcile loop ticking every 10s and multiple pending clusters, this means repeated full directory scans.
- Files: `internal/watcher/manager.go` (lines 316-326)
- Cause: Each retry entry reloads the full directory snapshot independently rather than sharing the result from Phase 1.
- Improvement path: Reuse the directory snapshot from the rescan phase (Phase 1) and pass it to Phase 3. Only call `LoadDirectorySnapshot` once per reconcile cycle.

**Unbounded log fetching in diagnostic analyzer:**
- Problem: `fetchLogs` reads up to `LogTailLines` (default 500) lines per container, per pod, for both current and previous logs. For deployments with many pods/containers, this generates significant API traffic and memory allocation.
- Files: `internal/diagnostic/analyzer.go` (lines 544-604)
- Cause: No limit on total log volume across all pods. A deployment with 10 pods and 3 containers each = 60 log API calls.
- Improvement path: Add a cap on total pods to inspect (e.g., 5) and total log bytes across all containers.

## Fragile Areas

**Debouncer timer race on Stop:**
- Files: `internal/watcher/debouncer.go`
- Why fragile: `time.AfterFunc` callbacks execute in their own goroutine. If `Stop()` is called while `emit()` is running, the `emit` goroutine has already been spawned and will try to send on the output channel. Since `Stop()` is called before `close(eventCh)` in shutdown (manager.Stop calls debouncer.Stop), this could panic if the channel is closed between the Stop and the emit completing.
- Safe modification: Always call `Stop()` before closing the output channel. The current shutdown order in `main.go` does this correctly (manager.Stop then close(eventCh)), but it is easy to break.
- Test coverage: `internal/watcher/debouncer_test.go` exists but does not test concurrent Stop+emit races.

**Manager.Start unlocks/relocks mutex during staggered startup:**
- Files: `internal/watcher/manager.go` (lines 152-161)
- Why fragile: The staggered startup loop temporarily unlocks `m.mu` to sleep for 1s between clusters, then re-acquires it. During this window, `reconcileLoop` or `rescanDirectory` could run (though they shouldn't be started yet). More concerning: if the context is cancelled during the sleep, the function re-locks and returns, but some watchers may be partially started.
- Safe modification: The unlock/relock pattern is intentional to avoid holding the mutex during sleep. Keep the mutex acquisition order consistent and ensure no other goroutine accesses the manager before Start returns.
- Test coverage: `internal/watcher/manager_test.go` tests multi-cluster start but not concurrent access during staggered startup.

**Health-check recycles watchers without RestConfig:**
- Files: `internal/watcher/manager.go` (lines 270-305)
- Why fragile: When an unhealthy watcher is detected in Phase 2, it is removed and queued for retry. But the `retryEntry` is constructed with only `ID: w.clusterID` — the `RestConfig` is nil (line 291). Phase 3 then tries to reload from the directory, but if the kubeconfig file is missing or unreadable, the cluster enters an infinite retry loop with "nil RestConfig" warnings.
- Safe modification: Store the original `ClusterInfo` (including `RestConfig`) alongside the watcher so it can be reused for retry without re-reading from disk.
- Test coverage: `TestReconcile_HealthCheckRecyclesUnhealthyWatcher` exists but uses a mock clientset factory, so the nil RestConfig path is not exercised.

## Scaling Limits

**Template cache is unbounded per cluster:**
- Current capacity: One entry per deployment per cluster (key = `clusterID/namespace/name`, value = 64-char hex string). ~150 bytes per entry.
- Limit: With 1000+ deployments across many clusters, the in-memory cache grows linearly. At ~150 bytes/entry, 10,000 deployments = ~1.5MB which is fine, but the 128Mi memory limit leaves little headroom for informer caches and log buffers.
- Scaling path: The `stripUnneededFields` transform already reduces informer memory. If scaling beyond ~5,000 deployments, increase the memory limit or add cache eviction for deleted deployments (already handled by `onDelete`).

**Single-replica persistence writes:**
- Current capacity: `HashStore` and `AuditRecorder` use a single controller-runtime client connected to the first cluster. All CRD writes go to one API server.
- Limit: At high event rates, CRD update conflicts (optimistic locking) can cause re-queues in `flush()`. The 5s flush interval batches well, but burst rollouts across many clusters could saturate the API server.
- Scaling path: The `DispatcherSplit` mode exists for horizontal scaling, where the monitor only writes CRDs and a separate dispatcher service processes them.

## Dependencies at Risk

**go.mod Go version 1.25.6:**
- Risk: This Go version does not exist. All Go releases follow `1.xx` or `1.xx.y` versioning, and Go 1.25 has not been released as of the analysis date.
- Impact: `go mod tidy` on any system with a real Go toolchain may behave unpredictably. Docker builds with standard Go images fail.
- Migration plan: Change to a real Go version (e.g., `go 1.22` to match the k8s client-go v0.31.0 dependency requirements).

## Missing Critical Features

**No CI/CD pipeline:**
- Problem: No `.github/workflows/`, `.gitlab-ci.yml`, `Makefile`, or any CI configuration exists. CLAUDE.md explicitly states "No linter or CI config exists in the repo yet."
- Blocks: Automated testing, linting, image builds, and deployment are all manual. No gate prevents broken code from being merged.

**No metrics/observability:**
- Problem: No Prometheus metrics, no `/metrics` endpoint, no metric counters for key operations (events detected, events dispatched, events dropped, dispatch failures, active watchers, pending retries, queue depth).
- Blocks: Cannot monitor the monitor. No alerting on queue full, dropped events, or unhealthy watchers in production. All observability is through structured log output only.

**No graceful drain of in-flight events:**
- Problem: On shutdown, the debouncer discards pending events, in-flight dispatcher work completes but newly debounced events are lost. There is no mechanism to drain the pipeline before exit.
- Blocks: Clean shutdown without event loss during rolling updates.

## Test Coverage Gaps

**Persistence layer (zero coverage):**
- What's not tested: `HashStore` flush/re-queue logic, `AuditRecorder` create/update paths, `sanitizeName` edge cases (empty string, strings >63 chars, strings with only special chars).
- Files: `internal/persistence/hash_store.go`, `internal/persistence/audit_recorder.go`, `internal/persistence/names.go`
- Risk: The flush re-queue logic (lines 104-128 of hash_store.go) is the most critical untested code — bugs here cause either silent hash data loss or unbounded memory growth from accumulating failed updates.
- Priority: High

**Main wiring (zero coverage):**
- What's not tested: Config-to-component wiring, target registration order, shutdown sequencing, split-mode goroutine behavior.
- Files: `cmd/monitor/main.go`, `cmd/dispatcher/main.go`
- Risk: Shutdown ordering bugs (documented above) and configuration edge cases are not caught. The split-mode event loop (lines 142-149) has no error handling for channel closure.
- Priority: Medium

**Holmes investigator:**
- What's not tested: The retry logic, response parsing, and naive result classification in `HolmesInvestigator.Investigate()`.
- Files: `internal/investigation/holmes.go`
- Risk: The retry creates a new request but does not verify the body is re-readable (it uses `bytes.NewReader` which is safe, but the second `http.NewRequestWithContext` call at line 82 ignores its error). The result classification false-positive issue is untested.
- Priority: Medium

**ConfigWatcher:**
- What's not tested: `ConfigWatcher` has no dedicated test file. Initial sync, dynamic watch handling, and status update paths are untested.
- Files: `internal/watcher/config_watcher.go`
- Risk: Runtime namespace filter updates are a critical operational feature. Bugs here cause either watching too many or too few namespaces with no automated test coverage.
- Priority: Medium

---

*Concerns audit: 2026-07-23*
