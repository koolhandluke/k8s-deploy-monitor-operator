# Codebase Concerns

**Analysis Date:** 2026-07-26

## Tech Debt

**Shutdown ordering — cancel() called after component cleanup:**
- Issue: In `cmd/monitor/main.go` (lines 340-351), `cancel()` is called *after* `manager.Stop()`, `close(eventCh)`, `dispatcher.Wait()`, and `orchestrator.Stop()`. Since these components receive `ctx` at start time, `cancel()` should fire first to signal all goroutines before draining. The current order works only because `Stop()` methods use their own internal stop mechanisms, but `hashStore.FlushLoop` and `configWatcher.Start` depend on `ctx` cancellation.
- Files: `cmd/monitor/main.go`
- Impact: On shutdown, the hashStore flush loop and config watcher may not terminate promptly; the final flush in `FlushLoop` uses `context.Background()` as a workaround (line 94 in `internal/persistence/hash_store.go`).
- Fix approach: Move `cancel()` before `manager.Stop()` and restructure shutdown to use the cancelled context as the primary stop signal.

**Persistence client uses first cluster's REST config for CRD operations:**
- Issue: `initK8sClients` in `cmd/monitor/main.go` (line 358-378) always uses `clusters[0].RestConfig` for the persistence client. If the CRDs live on a different cluster (e.g. a management cluster), this silently uses the wrong config.
- Files: `cmd/monitor/main.go`
- Impact: Multi-cluster deployments where CRDs are not on the first alphabetically-sorted cluster will fail to persist state.
- Fix approach: Add a `PERSISTENCE_KUBECONFIG` env var or config field to explicitly specify the target cluster for CRD operations, falling back to in-cluster config.

**Dispatcher only captures last target error:**
- Issue: `DispatchEvent` in `internal/dispatch/dispatcher.go` (line 82) overwrites `dispatchErr` on each target failure, so only the last failing target's error is recorded.
- Files: `internal/dispatch/dispatcher.go`
- Impact: When multiple targets fail, the audit record only shows the last error, losing diagnostic information.
- Fix approach: Collect all errors into a slice and join them for the dispatch error string.

**Holmes result classification is naive substring matching:**
- Issue: `internal/investigation/holmes.go` (lines 116-119) classifies a Holmes investigation as failed if the response contains "fail", "error", or "crash" anywhere in the text. This produces false positives (e.g. "no errors found" maps to `ResultFailed`).
- Files: `internal/investigation/holmes.go`
- Impact: Investigation reports may incorrectly classify healthy rollouts as failed.
- Fix approach: Have Holmes API return a structured `result` field, or use more nuanced NLP/regex patterns that account for negation.

**Silent int parse fallback:**
- Issue: `envInt` in `internal/config/config.go` (line 326) silently returns the default value when an env var contains a malformed integer. This is documented behavior but makes typos invisible (e.g. `WORKER_COUNT=3x` silently becomes 3).
- Files: `internal/config/config.go`
- Impact: Misconfigured deployments run with unexpected defaults.
- Fix approach: Log a warning when parsing fails so operators can catch typos.

**Duplicated Slack message formatting:**
- Issue: `SlackTarget.Dispatch` (`internal/dispatch/slack.go`) and `SlackBotTarget.Dispatch` (`internal/dispatch/slack_bot.go`) contain identical message formatting logic.
- Files: `internal/dispatch/slack.go`, `internal/dispatch/slack_bot.go`
- Impact: Message format changes must be applied in two places.
- Fix approach: Extract shared message formatting into a helper function.

**Legacy diagnostic target coexists with investigation system:**
- Issue: `cmd/monitor/main.go` (lines 228-235) maintains backward-compat for a legacy `DiagnosticEnabled` flag that creates an `AsyncDiagnosticTarget`. This path is redundant now that the investigation orchestrator exists.
- Files: `cmd/monitor/main.go`, `internal/diagnostic/target.go`
- Impact: Two code paths achieve the same goal; increases maintenance burden.
- Fix approach: Deprecate `DIAGNOSTIC_ENABLED` with a migration guide pointing to `investigationMode: runbook`.

## Known Bugs

**Orchestrator active map cleanup logic is redundant:**
- Symptoms: The deferred cleanup in `internal/investigation/orchestrator.go` (lines 103-118) has two consecutive `if _, ok := o.active[key]` checks. The first checks `cancel == nil` which will never match (cancel functions are non-nil), making it dead code. The second always deletes regardless of supersede.
- Files: `internal/investigation/orchestrator.go`
- Trigger: When a supersede happens, the old goroutine's defer may delete the new goroutine's entry from the active map, causing the new investigation to not be tracked.
- Workaround: The race window is small and the consequence is only a missed supersede log message, but it could cause a goroutine leak if `Stop()` is called at the wrong moment.

## Security Considerations

**HTTP APIs have no authentication or authorization:**
- Risk: The watchlist API (`:8080`) and status API (`:8081`) accept unauthenticated requests. The watchlist API allows PUT/DELETE operations that modify what the operator watches.
- Files: `internal/watchlist/handler.go`, `internal/investigation/status_api.go`
- Current mitigation: These ports are only exposed within the cluster (no Ingress in the deployment manifest).
- Recommendations: Add authentication middleware (e.g. bearer token validation, K8s service account token review) before exposing these APIs externally. At minimum, add rate limiting.

**No input validation on watchlist PUT body beyond `clusters` presence:**
- Risk: The PUT handler at `internal/watchlist/handler.go` (line 54) only validates that `clusters` is non-empty. Arbitrary strings for `slackChannel`, app name, cluster IDs, and namespace names are accepted without sanitization.
- Files: `internal/watchlist/handler.go`
- Current mitigation: CRD naming in the store layer sanitizes the name for K8s compatibility.
- Recommendations: Validate that cluster IDs, namespaces, and channel names match expected patterns. Reject injection-prone characters.

**Slack bot token stored in config struct without redaction:**
- Risk: The `SlackBotToken` field in the `Config` struct could be logged if the struct is ever serialized for debugging. The token is passed in the `Authorization` header (`internal/dispatch/slack_bot.go` line 68).
- Files: `internal/config/config.go`, `internal/dispatch/slack_bot.go`
- Current mitigation: Structured logging does not dump the full config struct; the startup log in `cmd/monitor/main.go` selectively logs safe fields.
- Recommendations: Add a `String()` or `MarshalJSON()` method to `Config` that redacts sensitive fields.

## Performance Bottlenecks

**TTL cleaner and stuck record recovery list all CRDs without field selectors:**
- Problem: `doRecoverStuck` (`internal/dispatch/record_watcher.go` line 208) and `cleanup` (`internal/dispatch/ttl_cleaner.go` line 60) list *all* RolloutRecord CRDs in the namespace, then filter in-memory.
- Files: `internal/dispatch/record_watcher.go`, `internal/dispatch/ttl_cleaner.go`
- Cause: No label selector or field selector is used to narrow the list to relevant records (Processing phase for stuck recovery, terminal phase for TTL).
- Improvement path: Add a phase label to RolloutRecord CRDs on creation/update, then use label selectors in list calls. For large-scale deployments with thousands of records, this prevents loading the entire CRD list into memory.

**LoadDirectorySnapshot called multiple times per reconcile cycle:**
- Problem: During a single reconcile tick, `LoadDirectorySnapshot` is called in `rescanDirectory`, `loadClusterFromDir`, and potentially once per pending retry entry (line 347 in `internal/watcher/manager.go`). Each call re-reads and parses all kubeconfig files.
- Files: `internal/watcher/manager.go`
- Cause: No caching of the directory snapshot within a reconcile pass.
- Improvement path: Load the snapshot once at the start of `reconcile()` and pass it to all phases.

**templateHash marshals the entire PodTemplateSpec every event:**
- Problem: `templateHash` in `internal/watcher/informer.go` (line 292) calls `json.Marshal` on `deploy.Spec.Template` for every add/update event.
- Files: `internal/watcher/informer.go`
- Cause: SHA256 needs bytes; JSON marshalling is the simplest way to get deterministic bytes.
- Improvement path: This is an acceptable tradeoff for correctness. If profiling shows this as hot, consider using `deploy.Spec.Template.String()` or a faster serializer.

## Fragile Areas

**Debouncer timer lifecycle during Stop:**
- Files: `internal/watcher/debouncer.go`
- Why fragile: `Stop()` calls `t.Stop()` for all timers, but a timer callback (`emit`) may already be running concurrently. The `emit` function acquires the lock and deletes from `pending`/`timers`, which races with `Stop`'s iteration if not careful. Currently safe because `Stop` holds the lock while iterating, but `emit` callbacks fired by `time.AfterFunc` before `Stop` acquires the lock could try to send on a closed `eventCh`.
- Safe modification: Always ensure `eventCh` is closed *after* debouncer.Stop() completes. The current shutdown order in `cmd/monitor/main.go` does this correctly (`manager.Stop()` calls `debouncer.Stop()` before `close(eventCh)`).
- Test coverage: `internal/watcher/debouncer_test.go` exists but does not test concurrent Stop + emit races.

**Manager lock release during staggered startup:**
- Files: `internal/watcher/manager.go` (lines 172-182)
- Why fragile: `Start()` temporarily releases `m.mu` during the 1-second stagger sleep between cluster startups. Any concurrent call that acquires `m.mu` during this window can observe partially-initialized state (some watchers started, others not yet).
- Safe modification: No external callers access the manager during `Start()` in practice, but if the reconcile loop or watchlist trigger fires early, it could see inconsistent state. The reconcile loop starts *after* `Start()` returns, so this is safe today.
- Test coverage: Manager tests use a fake clientset factory and don't test concurrent access during startup.

**Hash store flush re-queue on failure:**
- Files: `internal/persistence/hash_store.go` (lines 114-126)
- Why fragile: When `upsertClusterState` fails, pending hashes are re-queued. But the re-queue uses "don't overwrite newer" semantics that could lose a deletion signal (empty-string hash) if a new update for the same key arrived during the failed flush.
- Safe modification: Ensure empty-string (deletion) signals take priority over non-empty values during re-queue.
- Test coverage: No test covers the re-queue path for failed flushes.

## Scaling Limits

**Single-goroutine CRD write path for persistence:**
- Current capacity: Adequate for dozens of clusters with moderate deployment churn (flush every 5s batches well).
- Limit: With hundreds of clusters and thousands of deployments, the single `upsertClusterState` call per cluster per flush could bottleneck on API server latency. Each flush does a Get+Update (two API calls) per dirty cluster.
- Scaling path: Parallelize flush across clusters or use server-side apply (SSA) with patch to reduce to one API call per cluster.

**In-memory template cache unbounded within a cluster:**
- Current capacity: Memory usage is bounded by the number of deployments across all watched clusters. With `stripUnneededFields`, each cache entry is ~100 bytes (key + 64-char SHA256 hash).
- Limit: At 10,000 deployments per cluster across 50 clusters, the cache consumes ~50MB — well within the 128Mi limit. Beyond that, the informer's own cache (full Deployment objects) is the real bottleneck.
- Scaling path: The 128Mi memory limit in `deploy/deployment.yaml` should be reviewed if watching more than ~100 clusters.

## Dependencies at Risk

**go.mod declares go 1.25.6 — non-existent Go version:**
- Risk: `go.mod` specifies `go 1.25.6` which does not exist (Go versions follow 1.2x.y pattern as of 2026). The Dockerfile uses `golang:1.25-alpine`. If this was meant to be `go 1.23.6` or a future version, the mismatch will cause build failures when the go directive is enforced.
- Impact: `go build` may fail depending on the local toolchain's version negotiation. Docker builds depend on whether `golang:1.25-alpine` resolves to a valid image tag.
- Migration plan: Pin `go.mod` to the actual Go version used in development and match it in the Dockerfile.

**gopkg.in/yaml.v3 used only via config, but yaml.v2 also in dependency tree:**
- Risk: Both `gopkg.in/yaml.v2` and `gopkg.in/yaml.v3` are indirect dependencies. `config.go` imports `gopkg.in/yaml.v3` directly (line 12). Having both versions increases binary size and potential confusion.
- Impact: Low — v2 is pulled transitively by K8s client-go libraries.
- Migration plan: No action needed; transitive dependency.

## Missing Critical Features

**No health/readiness endpoints:**
- Problem: Neither `cmd/monitor/main.go` nor `cmd/dispatcher/main.go` expose `/healthz` or `/readyz` endpoints.
- Blocks: Kubernetes liveness/readiness probes cannot be configured, so the pod stays "Running" even if all cluster watchers have failed permanently or the dispatcher cannot reach the API server.

**No metrics/observability:**
- Problem: No Prometheus metrics are exported. The only observability is structured log output.
- Blocks: Cannot create dashboards or alerts for: events dropped due to full queue, dispatch failures, watcher health, investigation durations, hash flush latency. Operators must grep logs for monitoring.

**No graceful drain for in-flight investigations on shutdown:**
- Problem: `Orchestrator.Stop()` calls `o.cancel()` and `o.wg.Wait()`, cancelling all investigations immediately. Investigations that are nearly complete lose their results.
- Blocks: Long-running Holmes investigations (up to 5-minute timeout) will always be cancelled on rolling updates.

## Test Coverage Gaps

**No tests for `cmd/monitor/main.go` or `cmd/dispatcher/main.go`:**
- What's not tested: Startup wiring, config validation interactions, shutdown sequencing, signal handling.
- Files: `cmd/monitor/main.go`, `cmd/dispatcher/main.go`
- Risk: Wiring bugs (wrong order of operations, nil pointer on disabled features) only caught at runtime.
- Priority: Medium

**No tests for `internal/persistence/hash_store.go`:**
- What's not tested: `FlushLoop`, `flush`, `upsertClusterState`, re-queue on failure, create vs update paths.
- Files: `internal/persistence/hash_store.go`
- Risk: Hash persistence is critical for gap detection across restarts. A bug here means missed rollout events.
- Priority: High

**No tests for `internal/persistence/audit_recorder.go`:**
- What's not tested: Audit record creation, status updates, CRD interaction.
- Files: `internal/persistence/audit_recorder.go`
- Risk: Audit trail could silently fail without detection.
- Priority: Medium

**No tests for `internal/watcher/config_watcher.go`:**
- What's not tested: Initial sync, dynamic informer event handling, status update write-back.
- Files: `internal/watcher/config_watcher.go`
- Risk: Runtime namespace filter changes via MonitorConfig CRD could break silently.
- Priority: Medium

**No tests for `internal/watcher/namespace_filter.go`:**
- What's not tested: Thread-safe `Allowed` and `Update` methods, allowlist-takes-precedence logic.
- Files: `internal/watcher/namespace_filter.go`
- Risk: Low — logic is simple, but concurrent update behavior is untested.
- Priority: Low

**No integration tests or end-to-end tests:**
- What's not tested: Full pipeline from kubeconfig loading through event detection, debounce, dispatch, and persistence.
- Files: All packages
- Risk: Unit tests cover individual components but cross-package interactions (e.g. enricher + dispatcher + persistence) are untested.
- Priority: Medium

---

*Concerns audit: 2026-07-26*
