# Local Development & Testing

## Prerequisites

- minikube (or any local k8s cluster)
- Helm 3
- Docker (for building the image)

All commands below assume you are in the repo root:

```bash
cd ~/git/k8s-deploy-monitor-operator
```

## Build the image

```bash
# Build locally
docker build -t rollout-monitor:latest .

# Load into minikube
minikube image load rollout-monitor:latest
```

## Deploy

```bash
# Idempotent — installs or upgrades
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never
```

## Deploy with non-agentic investigation (runbook mode)

Runbook mode runs the built-in diagnostic runbook on every rollout: it polls the
Deployment every 10s, detects failures (CrashLoopBackOff, ImagePullBackOff,
ProgressDeadlineExceeded, stalls, etc.), gathers events/pod status/logs, and
posts a failure report to Slack. No external AI service required.

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never \
  --set investigation.mode=runbook \
  --set dispatch.slackWebhookUrl=https://hooks.slack.com/services/YOUR/WEBHOOK/URL
```

To test without Slack (dumps the full report payload to stdout):

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never \
  --set investigation.mode=runbook \
  --set dispatch.slackWebhookUrl=TEST
```

> **Note:** Setting `SLACK_WEBHOOK_URL=TEST` enables test mode — the
> `SlackReporter` logs the full Block Kit JSON payload to stdout instead
> of posting to Slack.

### Trigger a failing rollout (runbook investigation)

```bash
# Create a deployment with a valid image
kubectl create deployment test-fail --image=nginx:1.25 -n default

# Wait for it to stabilise, then trigger a rollout with a bad image
kubectl set image deployment/test-fail nginx=nginx:doesnotexist -n default

# Watch the monitor logs — after the 30s debounce you should see:
#   1. "investigation started" with mode=runbook
#   2. Poll logs every 10s (generation gate, failure checks)
#   3. "investigation complete" with result=FAILED and diagnostic details
kubectl logs -n rollout-monitor -l app.kubernetes.io/name=deploy-monitor -f
```

Other failure scenarios to try:

```bash
# CrashLoopBackOff — container starts but exits immediately
kubectl set image deployment/test-fail nginx=busybox -n default
# (busybox exits immediately since there's no long-running command)

# Missing ConfigMap — CreateContainerConfigError
kubectl patch deployment test-fail -n default --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/envFrom","value":[{"configMapRef":{"name":"does-not-exist"}}]}]'
```

Clean up test deployments:

```bash
kubectl delete deployment test-fail -n default
```

## Investigation Status API (trace mode)

When deployed with `logging.trace=true` and an investigation mode enabled, the monitor exposes a lightweight HTTP API on port 8081 for inspecting investigation results:

```bash
# Port-forward to the status API
kubectl port-forward deploy/deploy-monitor-deploy-monitor 8081:8081 -n rollout-monitor

# List all investigation results
curl localhost:8081/api/v1/investigations

# Query a specific deployment (matches by namespace/name suffix)
curl localhost:8081/api/v1/investigations/default/nginx
```

Response shape:

```json
{
  "deployment_key": "minikube/default/nginx",
  "result": "SUCCESS",
  "failure_reason": "",
  "duration": "62s",
  "timestamp": "2026-07-20T12:00:00Z"
}
```

The cache stores the last result per deployment key. It has zero cost when trace is disabled — no `StatusCache` is allocated and no HTTP server starts.

## E2E Tests

The automated E2E test suite validates the investigation pipeline against a local minikube cluster:

```bash
./test/e2e.sh
```

This builds the image, deploys via Helm with `investigation.mode=runbook` and `logging.trace=true`, then runs 6 scenarios:

1. **Healthy rollout** — image update succeeds, expects `SUCCESS`
2. **Bad image tag** — non-existent image, expects `FAILED`
3. **CrashLoopBackOff** — container exits immediately, expects `FAILED`
4. **Missing ConfigMap** — references non-existent ConfigMap, expects `FAILED`
5. **Deleted mid-rollout** — deployment deleted during investigation, expects `DELETED`
6. **Supersede** — two rapid image changes, first cancelled, second completes

Tests 1-5 validate results via the status API (`curl /api/v1/investigations/{ns}/{name}`). Test 6 also checks logs for the supersede cancellation message.

Environment variable overrides:

| Var | Default | Description |
|-----|---------|-------------|
| `E2E_NAMESPACE` | `e2e-test` | Namespace for test deployments |
| `E2E_RELEASE` | `deploy-monitor` | Helm release name |
| `E2E_RELEASE_NS` | `rollout-monitor` | Namespace for the monitor |
| `E2E_TEST_TIMEOUT` | `240` | Per-test timeout in seconds |
| `E2E_LOCAL_PORT` | `18081` | Local port for status API port-forward |

## Deploy with Holmes dispatch

To test Holmes dispatch locally, first deploy HolmesGPT on the same cluster:

```bash
# Add the Robusta Helm repo (if not already added)
helm repo add robusta https://robusta-charts.storage.googleapis.com && helm repo update

# Create a secret with your LLM API key
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
```

The Holmes service will be available at `http://holmes-holmes.rollout-monitor:80`.

Then deploy the monitor pointing at it:

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never \
  --set investigation.mode=holmes \
  --set dispatch.holmesApiUrl=http://holmes-holmes.rollout-monitor:80 \
  --set dispatch.slackWebhookUrl=https://hooks.slack.com/services/YOUR/WEBHOOK/URL
```

Holmes API calls take 30-60s (LLM inference + tool calls). Check the monitor logs for `dispatched to holmes` with status 200 to confirm the integration is working.

## Trigger a test rollout

```bash
# Create a deployment
kubectl create deployment test-rollout --image=nginx:1.25 -n default

# Update the image (triggers rollout detection)
kubectl set image deployment/test-rollout nginx=nginx:1.26 -n default

# Check monitor logs (events appear after the 30s debounce window)
kubectl logs -n rollout-monitor -l app.kubernetes.io/name=deploy-monitor -f
```

## Verify CRDs

```bash
# Check ClusterRolloutState (hash baselines per cluster)
kubectl get crs -n rollout-monitor

# Check RolloutRecords (audit trail)
kubectl get rr -n rollout-monitor
```

## Uninstall

```bash
helm uninstall deploy-monitor -n rollout-monitor

# CRDs are NOT removed by helm uninstall (by design).
# To remove them manually:
kubectl delete crd clusterrolloutstates.deploy-monitor.io rolloutrecords.deploy-monitor.io
```

## Values Reference

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/koolhandluke/k8s-deploy-monitor-operator` | Container image |
| `image.tag` | `latest` | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy (`Never` for local builds) |
| `dispatch.mode` | `log` | `log`, `holmes`, `slack`, or `both` |
| `dispatch.holmesApiUrl` | `""` | Holmes API endpoint (required for `holmes`/`both`) |
| `dispatch.slackWebhookUrl` | `""` | Slack webhook (required for `slack`/`both`) |
| `namespaceFilter.allowlist` | `[]` | If set, only watch these namespaces |
| `namespaceFilter.denylist` | `[kube-system, kube-public, kube-node-lease]` | Ignored when allowlist is set |
| `investigation.mode` | `none` | `none`, `runbook`, or `holmes` — post-rollout investigation mode |
| `investigation.maxConcurrent` | `10` | Max concurrent investigations |
| `logging.debug` | `false` | Enable debug-level logging |
| `logging.trace` | `false` | Enable trace-level logging and status API |
| `logging.statusAPIPort` | `8081` | Status API port (only active when trace=true) |
| `persistence.enabled` | `true` | CRD-based hash persistence and audit records |
| `persistence.namespace` | `""` | Namespace for CRs (defaults to release namespace) |
| `tuning.workerCount` | `3` | Dispatcher worker pool size |
| `tuning.debounceSeconds` | `30` | Per-deployment coalescing window |
| `tuning.queueMaxSize` | `100` | Event queue depth before dropping |
| `resources.requests.cpu` | `50m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `resources.limits.cpu` | `200m` | CPU limit |
| `resources.limits.memory` | `128Mi` | Memory limit |
| `serviceAccount.create` | `true` | Create a ServiceAccount |
| `serviceAccount.name` | `""` | Override ServiceAccount name |
| `namespace.create` | `false` | Create the namespace |
