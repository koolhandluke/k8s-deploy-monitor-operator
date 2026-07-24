<!-- generated-by: gsd-doc-writer -->
# Testing

## Test framework and setup

The project uses the Go standard library `testing` package — no third-party test framework. Tests are in-process and require no running cluster or external services.

Dependencies used in tests:
- `k8s.io/client-go/kubernetes/fake` — in-memory Kubernetes clientset for watcher and diagnostic tests
- `net/http/httptest` — local HTTP server for Slack and Holmes dispatch target tests
- `k8s.io/client-go/rest` — minimal REST config construction for cluster registry tests

No `envtest`, no live cluster, and no Docker is required to run the unit tests.

## Running tests

Run the full suite:

```bash
go test ./...
```

Run with the race detector (recommended — the codebase uses concurrent maps, atomic counters, and timers):

```bash
go test ./... -race
```

Run a single package:

```bash
go test ./internal/watcher/
go test ./internal/diagnostic/
go test ./internal/dispatch/
go test ./internal/investigation/
go test ./internal/config/
```

Run a single test by name:

```bash
go test ./internal/watcher/ -run TestClusterWatcher_DetectsRollout -v
go test ./internal/diagnostic/ -run TestCheckFailureConditions -v
go test ./internal/investigation/ -run TestOrchestrator_SupersedeCancellation -v
```

## Test file naming and structure

Test files follow the standard Go convention: `{package}_test.go` or `{feature}_test.go`, co-located with the package they test. The `package` declaration uses the package under test (white-box access) rather than an external `_test` package.

| Package | Test files | What they cover |
|---------|------------|-----------------|
| `internal/watcher` | `informer_test.go`, `debouncer_test.go`, `manager_test.go` | Rollout detection, debouncing, multi-cluster reconcile loop |
| `internal/diagnostic` | `analyzer_test.go`, `diagnostics_test.go`, `scenarios_test.go`, `soak_test.go`, `monitor_test.go`, `fixtures_test.go` | Failure condition detection, pod status collection, event filtering |
| `internal/dispatch` | `slack_test.go`, `slack_bot_test.go`, `record_watcher_test.go`, `ttl_cleaner_test.go` | Slack webhook target, Slack bot target, dispatch helpers |
| `internal/investigation` | `orchestrator_test.go`, `slack_reporter_test.go` | Investigation lifecycle, supersede cancellation, concurrency limit |
| `internal/config` | `config_test.go`, `kubeconfig_test.go`, `env_config_test.go` | Env var loading, kubeconfig parsing, per-app env config, Slack routing |

## Key test patterns

**Fake clientsets** — watcher and diagnostic tests use `fake.NewSimpleClientset(objects...)` to pre-seed in-memory state. Tests update the fake clientset via `clientset.AppsV1().Deployments(ns).Update(...)` to trigger watcher callbacks without a live cluster.

**httptest servers** — Slack and Holmes dispatch tests spin up a local `httptest.NewServer` and point the target at it. This verifies the exact HTTP method, headers, and body sent without needing external services.

**Test doubles** — investigation tests use `fakeInvestigator` and `fakeReporter` structs that implement the `Investigator` and `Reporter` interfaces, with configurable delays and recorded calls.

**Temp directories** — manager tests use `t.TempDir()` to write minimal kubeconfig YAML files and verify that the reconcile loop picks up adds, removes, and file hash changes.

**YAML fixtures** — diagnostic tests in `internal/diagnostic/testdata/` contain YAML-encoded Kubernetes objects (Deployments, ReplicaSets, Pods, Events) for reproducible failure scenarios such as `PodCrashloop`, `PodOOMKilled`, `PodConfigError`, `PodInvalidImage`, and `PodInitCrash`.

## What the unit tests cover

**Rollout detection (`internal/watcher`)**
- `TestClusterWatcher_DetectsRollout` — image update in `spec.template` emits a `RolloutEvent`
- `TestClusterWatcher_IgnoresStatusUpdates` — status-only changes produce no event
- `TestClusterWatcher_NamespaceFilter` — updates in filtered namespaces are suppressed
- `TestTemplateHash_*` — SHA256 hashing is stable and differentiates image changes
- `TestHealthStatus_*` — consecutive error counting, permanent error flag, counter reset

**Debouncer (`internal/watcher`)**
- `TestDebouncer_ReplacesEvent` — rapid submits for the same key coalesce to the latest event
- `TestDebouncer_IndependentKeys` — different deployment keys are debounced independently
- `TestDebouncer_Stop` — pending timers are cancelled on `Stop()`

**Manager reconcile loop (`internal/watcher`)**
- `TestReconcile_AddsNewCluster`, `_RemovesCluster`, `_RecyclesChangedCluster` — kubeconfig directory changes are reflected in running watchers
- `TestReconcile_NoRestartOnUnchanged` — unchanged kubeconfig files do not restart watchers
- `TestStartup_FailedClusterQueuesRetry` — a failing clientset factory queues the cluster for retry instead of returning an error
- `TestRetryBackoff` — exponential backoff sequence (10s→20s→40s→80s→160s→5m cap)

**Failure detection (`internal/diagnostic`)**
- `TestCheckFailureConditions` — table-driven test covering ProgressDeadlineExceeded, CrashLoop restart threshold, CreateContainerConfigError time window, InvalidImageName, OOMKilled under threshold, and healthy baseline
- `TestConfigErrorTimerResets` — config-error timer resets when containers leave the Waiting state
- `TestGatherDiagnostics_*` — pod status collection, init container detection, event filtering by name prefix, log snippet collection for failing pods
- `TestIsPodFailing` — all failing conditions: CrashLoopBackOff, ImagePullBackOff, OOMKilled, restarts > 0

**Investigation orchestrator (`internal/investigation`)**
- `TestOrchestrator_SupersedeCancellation` — second investigate call for the same key cancels the in-flight first via context
- `TestOrchestrator_ConcurrencyLimit` — third simultaneous investigation is dropped when semaphore is full
- `TestOrchestrator_StopDrains` — `Stop()` waits for all goroutines before returning

**Dispatch targets (`internal/dispatch`)**
- `TestSlackTarget_Dispatch` — POST body contains cluster name, deployment name, and new image
- `TestSlackBotTarget_Dispatch` — Bearer token header set, channel routed correctly
- `TestSlackBotTarget_EmptyChannel_Skips` — no HTTP request made when `SlackChannel` is empty

**Config (`internal/config`)**
- `TestLoadEnvConfigs` — per-app YAML config parses cluster and namespace mappings
- `TestBuildNamespaceLookup` / `TestNamespaceLookup_GetSlackChannel` — namespace-to-app and app-to-Slack-channel lookups

## Coverage requirements

No coverage thresholds are configured. There is no `.nycrc`, `coverageThreshold` in a Jest config, or equivalent Go coverage enforcement in CI. To generate a coverage report locally:

```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## CI integration

No CI pipeline is configured in this repository (no `.github/workflows/` or equivalent). Tests are run manually as described above.

## E2E tests

The automated E2E test suite validates the full investigation pipeline against a local minikube cluster.

**Prerequisites:** minikube running, `kubectl`, `helm`, `jq` installed.

```bash
./test/e2e.sh
```

The script builds the Docker image, loads it into minikube, deploys via Helm with `investigation.mode=runbook` and `logging.trace=true`, then runs 6 scenarios:

| # | Scenario | Trigger | Expected result |
|---|----------|---------|-----------------|
| 1 | Healthy rollout | `nginx:1.25` → `nginx:1.26` | `SUCCESS` |
| 2 | Bad image tag | `nginx:doesnotexist` | `FAILED` |
| 3 | CrashLoopBackOff | `busybox:1.36` (exits immediately) | `FAILED` |
| 4 | Missing ConfigMap | `envFrom` referencing non-existent ConfigMap | `FAILED` |
| 5 | Deleted mid-rollout | Deployment deleted during investigation | `DELETED` |
| 6 | Supersede | Two rapid image changes; first cancelled | Log + status API both checked |

Tests 1-5 assert results via the status API (`GET /api/v1/investigations/{ns}/{name}`). Test 6 also greps monitor logs for `"superseding in-flight investigation"`.

**E2E environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `E2E_NAMESPACE` | `e2e-test` | Namespace for test deployments |
| `E2E_RELEASE` | `deploy-monitor` | Helm release name |
| `E2E_RELEASE_NS` | `rollout-monitor` | Namespace for the monitor |
| `E2E_TEST_TIMEOUT` | `240` | Per-test timeout in seconds |
| `E2E_LOCAL_PORT` | `18081` | Local port for status API port-forward |

## Manual integration testing

### Build and deploy locally

```bash
# Build image
docker build -t rollout-monitor:latest .

# Load into minikube
minikube image load rollout-monitor:latest

# Deploy (basic — log dispatch only)
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never
```

### Trigger a test rollout

```bash
kubectl create deployment test-rollout --image=nginx:1.25 -n default
kubectl set image deployment/test-rollout nginx=nginx:1.26 -n default

# Events appear after the 30s debounce window
kubectl logs -n rollout-monitor -l app.kubernetes.io/name=deploy-monitor -f
```

### Trigger a failing rollout (runbook investigation)

Deploy with investigation mode enabled:

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never \
  --set investigation.mode=runbook \
  --set dispatch.slackWebhookUrl=TEST
```

Setting `SLACK_WEBHOOK_URL=TEST` enables test mode — the `SlackReporter` logs the full Block Kit JSON payload to stdout instead of posting to Slack.

```bash
kubectl create deployment test-fail --image=nginx:1.25 -n default

# Bad image — triggers ImagePullBackOff
kubectl set image deployment/test-fail nginx=nginx:doesnotexist -n default

# CrashLoopBackOff — container exits immediately
kubectl set image deployment/test-fail nginx=busybox -n default

# Missing ConfigMap — CreateContainerConfigError
kubectl patch deployment test-fail -n default --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/envFrom","value":[{"configMapRef":{"name":"does-not-exist"}}]}]'
```

After the 30s debounce window, monitor logs show investigation progress (poll every 10s) and a final result.

### Investigation status API

When deployed with `logging.trace=true`, the monitor exposes a status API on port 8081:

```bash
kubectl port-forward deploy/deploy-monitor-deploy-monitor 8081:8081 -n rollout-monitor

# List all results
curl localhost:8081/api/v1/investigations

# Query a specific deployment
curl localhost:8081/api/v1/investigations/default/test-fail
```

Response shape:

```json
{
  "deployment_key": "minikube/default/test-fail",
  "result": "FAILED",
  "failure_reason": "CrashLoopBackOff: restart threshold exceeded",
  "duration": "45s",
  "timestamp": "2026-07-23T12:00:00Z"
}
```

### Deploy with Holmes dispatch

```bash
# Add Robusta Helm repo
helm repo add robusta https://robusta-charts.storage.googleapis.com && helm repo update

# Create secret with LLM API key
kubectl create secret generic holmes-api-key \
  --from-literal=ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
  -n rollout-monitor

# Install Holmes
helm upgrade --install holmes robusta/holmes \
  --namespace rollout-monitor \
  --set 'additionalEnvVars[0].name=ANTHROPIC_API_KEY' \
  --set 'additionalEnvVars[0].valueFrom.secretKeyRef.name=holmes-api-key' \
  --set 'additionalEnvVars[0].valueFrom.secretKeyRef.key=ANTHROPIC_API_KEY' \
  --set 'additionalEnvVars[1].name=MODEL' \
  --set 'additionalEnvVars[1].value=anthropic/claude-sonnet-4-5-20250929'

# Deploy monitor pointing at Holmes
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never \
  --set investigation.mode=holmes \
  --set dispatch.holmesApiUrl=http://holmes-holmes.rollout-monitor:80 \
  --set dispatch.slackWebhookUrl=https://hooks.slack.com/services/YOUR/WEBHOOK/URL
```

Holmes API calls take 30-60s (LLM inference + tool calls). Confirm with:

```bash
kubectl logs -n rollout-monitor -l app.kubernetes.io/name=deploy-monitor -f
# Look for: "dispatched to holmes" with status 200
```

### Verify CRDs

```bash
kubectl get crs -n rollout-monitor   # ClusterRolloutState (hash baselines per cluster)
kubectl get rr -n rollout-monitor    # RolloutRecord (audit trail)
```

### Uninstall

```bash
helm uninstall deploy-monitor -n rollout-monitor

# CRDs are not removed by helm uninstall — remove manually if needed
kubectl delete crd clusterrolloutstates.deploy-monitor.io rolloutrecords.deploy-monitor.io
```
