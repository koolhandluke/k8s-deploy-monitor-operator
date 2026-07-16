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
  --set dispatch.mode=holmes \
  --set dispatch.holmesApiUrl=http://holmes-holmes.rollout-monitor:80
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
