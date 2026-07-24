<!-- generated-by: gsd-doc-writer -->
# Deployment

This document covers deploying the rollout monitor to a Kubernetes cluster, including both the
recommended Helm path and the raw-manifest alternative.

## Deployment Targets

Two deployment methods are supported:

| Method | Config | Notes |
|---|---|---|
| Helm chart | `chart/deploy-monitor/` | Recommended; handles RBAC, CRDs, and dispatcher as a unit |
| Raw manifests | `deploy/deployment.yaml`, `deploy/crds.yaml` | Minimal; suitable for one-off or air-gapped installs |

The container image is published to `ghcr.io/koolhandluke/k8s-deploy-monitor-operator`. The
`Dockerfile` uses a two-stage build: `golang:1.25-alpine` for compilation and `alpine:3.20` as
the runtime base. Both binaries (`rollout-monitor` and `rollout-dispatcher`) are built into the
same image; the default `ENTRYPOINT` runs `rollout-monitor`.

## Build Pipeline

No CI/CD pipeline is configured in this repository. The build steps are manual:

1. Build the container image:

```bash
docker build -t rollout-monitor:latest .
```

2. Push to your registry (substitute your registry and tag):

```bash
# <!-- VERIFY: registry URL and push command for your environment -->
docker tag rollout-monitor:latest ghcr.io/koolhandluke/k8s-deploy-monitor-operator:latest
docker push ghcr.io/koolhandluke/k8s-deploy-monitor-operator:latest
```

3. Deploy via Helm (see below).

## Helm Deployment (Recommended)

The Helm chart at `chart/deploy-monitor/` creates the namespace, ServiceAccount, ClusterRole,
ClusterRoleBinding, CRDs, and the monitor Deployment in a single release.

### Minimal install (log-only, local image)

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --create-namespace \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never
```

### With Slack notifications

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --create-namespace \
  --set dispatch.mode=slack \
  --set dispatch.slackWebhookUrl=https://hooks.slack.com/services/YOUR/WEBHOOK/URL
```

### With runbook-based investigation

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --create-namespace \
  --set investigation.mode=runbook \
  --set dispatch.mode=slack \
  --set dispatch.slackWebhookUrl=https://hooks.slack.com/services/YOUR/WEBHOOK/URL
```

### With Holmes AI investigation

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --create-namespace \
  --set dispatch.mode=holmes \
  --set dispatch.holmesApiUrl=http://holmes.your-cluster/
```

### With the split dispatcher

The optional dispatcher component reads `RolloutRecord` CRDs written by the monitor and handles
dispatch separately. Enable it when you want to decouple detection from notification delivery:

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --create-namespace \
  --set dispatcher.enabled=true \
  --set dispatch.mode=slack \
  --set dispatch.slackWebhookUrl=https://hooks.slack.com/services/YOUR/WEBHOOK/URL \
  --set persistence.enabled=true
```

### Key Helm values

| Value | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/koolhandluke/k8s-deploy-monitor-operator` | Image registry and name |
| `image.tag` | `latest` | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Kubernetes image pull policy |
| `kubeconfig.configMapName` | `""` | ConfigMap name containing kubeconfig files (one key per cluster); mounted at `/etc/kubeconfig` |
| `dispatch.mode` | `log` | `log` \| `holmes` \| `slack` \| `both` |
| `dispatch.holmesApiUrl` | `""` | Holmes API base URL (required for `holmes`/`both`) |
| `dispatch.slackWebhookUrl` | `""` | Slack webhook URL (required for `slack`/`both`) |
| `namespaceFilter.allowlist` | `[]` | Namespace allowlist; overrides denylist when non-empty |
| `namespaceFilter.denylist` | `[kube-system, kube-public, kube-node-lease]` | Namespace denylist |
| `persistence.enabled` | `true` | Enable CRD-based hash persistence and audit recording |
| `persistence.namespace` | `""` | Namespace for CRDs; defaults to release namespace |
| `tuning.workerCount` | `3` | Dispatcher worker pool size |
| `tuning.debounceSeconds` | `30` | Per-deployment event coalescing window |
| `tuning.queueMaxSize` | `100` | Event queue depth before dropping |
| `logging.debug` | `false` | Enable debug-level logging |
| `logging.trace` | `false` | Enable trace-level logging and status API on port `8081` |
| `investigation.mode` | `none` | `none` \| `runbook` \| `holmes` |
| `investigation.maxConcurrent` | `10` | Max concurrent in-flight investigations |
| `dispatcher.enabled` | `false` | Enable the split dispatcher component |
| `dispatcher.replicas` | `1` | Dispatcher replica count |
| `dispatcher.ttlDays` | `7` | Days before `RolloutRecord` CRDs are eligible for cleanup |

## Raw Manifest Deployment

For environments without Helm:

```bash
# 1. Apply CRDs (required if PERSISTENCE_ENABLED=true)
kubectl apply -f deploy/crds.yaml

# 2. Apply RBAC and Deployment
kubectl apply -f deploy/deployment.yaml
```

The raw manifest (`deploy/deployment.yaml`) creates:
- Namespace: `rollout-monitor`
- ServiceAccount: `rollout-monitor` in namespace `rollout-monitor`
- ClusterRole with `get`, `list`, `watch` on `apps/deployments`
- ClusterRoleBinding binding the ClusterRole to the ServiceAccount
- Deployment with 1 replica, `50m`/`64Mi` requests, `200m`/`128Mi` limits

The raw manifest defaults to `DISPATCH_MODE=log` and does not mount kubeconfigs from a ConfigMap.
Edit the `env` section and add a volume/volumeMount if you need kubeconfig injection.

## Environment Setup

The monitor requires `KUBECONFIG_DIR` to point to a directory of kubeconfig files. In-cluster
deployments use a ConfigMap mount for this:

```bash
# Create a ConfigMap with one kubeconfig key per cluster
kubectl create configmap kubeconfig \
  --from-file=cluster-a.yaml=./kubeconfigs/cluster-a.yaml \
  --from-file=cluster-b.yaml=./kubeconfigs/cluster-b.yaml \
  -n rollout-monitor
```

Then set `kubeconfig.configMapName=kubeconfig` in your Helm values. The chart mounts the ConfigMap
at `/etc/kubeconfig` and sets `KUBECONFIG_DIR=/etc/kubeconfig` automatically.

For dispatch targets, supply secrets directly via `--set` or a values override file:

```bash
# Using a separate secrets values file (recommended over --set for credentials)
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  -f values-production.yaml \
  -f values-secrets.yaml
```

See [docs/CONFIGURATION.md](CONFIGURATION.md) for the full environment variable reference.

## RBAC Requirements

The monitor's ClusterRole grants read-only access by default. When `persistence.enabled=true`, the
role also gains write access to the `deploy-monitor.io` CRDs:

| Resource | Verbs (base) | Verbs (persistence enabled) |
|---|---|---|
| `apps/deployments` | `get`, `list`, `watch` | `get`, `list`, `watch` |
| `deploy-monitor.io/monitorconfigs` | `get`, `list`, `watch` | `get`, `list`, `watch` |
| `deploy-monitor.io/clusterrolloutstates` | — | `get`, `list`, `create`, `update`, `patch` |
| `deploy-monitor.io/rolloutrecords` | — | `get`, `list`, `create`, `update`, `patch` |

The dispatcher component (when enabled) uses its own ServiceAccount with a narrower ClusterRole
that only creates `rolloutrecords`.

## Rollback Procedure

Because the monitor is stateless by default (no persistence) or uses CRDs for state, rollback is
straightforward:

1. Identify the previous working image tag from your registry or build history.
2. Roll back the Helm release:

```bash
# View release history
helm history deploy-monitor -n rollout-monitor

# Roll back to a specific revision
helm rollback deploy-monitor <REVISION> -n rollout-monitor
```

3. Verify the pod is running:

```bash
kubectl rollout status deployment/deploy-monitor -n rollout-monitor
```

If using raw manifests, redeploy the previous `deployment.yaml` version:

```bash
kubectl apply -f deploy/deployment.yaml
```

CRD state (`ClusterRolloutState`, `RolloutRecord`) is not affected by rollback — the monitor
re-seeds its template hash cache from the cluster on startup if CRD state is absent or stale.

## Monitoring

The monitor uses structured JSON logging via `log/slog`. No external monitoring library is
configured.

To stream logs in production:

```bash
kubectl logs -n rollout-monitor -l app.kubernetes.io/name=deploy-monitor -f
```

When `logging.trace=true` is set (Helm value) or `TRACE=true` (env var), a status API is exposed
on port `8081` that shows in-flight investigations:

```bash
# Port-forward to the status API
kubectl port-forward -n rollout-monitor deployment/deploy-monitor 8081:8081

# List active investigations
curl http://localhost:8081/api/v1/investigations

# Query a specific deployment
curl http://localhost:8081/api/v1/investigations/<namespace>/<deployment-name>
```

<!-- VERIFY: external monitoring dashboard or alerting integration, if configured -->
