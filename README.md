<!-- generated-by: gsd-doc-writer -->
# k8s-deploy-monitor-operator

A Go service that watches Kubernetes Deployment rollouts across one or more clusters, detects template changes via SHA256 hashing, and dispatches notifications to Slack or Holmes AI.

## Installation

### Prerequisites

- Go >= 1.25.6 (for building from source)
- Docker (for container image)
- Helm 3 (for cluster deployment)
- A directory of kubeconfig files, one per cluster

### Build from source

```bash
git clone https://github.com/koolhandluke/k8s-deploy-monitor-operator.git
cd k8s-deploy-monitor-operator
go build ./...
```

### Build container image

```bash
docker build -t rollout-monitor:latest .
```

## Quick Start

**Run locally** (watches clusters from kubeconfigs in `./kubeconfigs/`):

```bash
KUBECONFIG_DIR=./kubeconfigs go run ./cmd/monitor
```

**Deploy to a cluster** via Helm:

```bash
# Log-only mode (no external notifications)
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never
```

After the 30-second debounce window, rollout events appear in the monitor logs:

```bash
kubectl logs -n rollout-monitor -l app.kubernetes.io/name=deploy-monitor -f
```

## Usage Examples

### Trigger a rollout and observe detection

```bash
# Create a test deployment
kubectl create deployment test-rollout --image=nginx:1.25 -n default

# Update the image — this triggers rollout detection
kubectl set image deployment/test-rollout nginx=nginx:1.26 -n default

# Events appear in monitor logs after the 30s debounce window
kubectl logs -n rollout-monitor -l app.kubernetes.io/name=deploy-monitor -f
```

### Deploy with Slack notifications

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never \
  --set dispatch.mode=slack \
  --set dispatch.slackWebhookUrl=https://hooks.slack.com/services/YOUR/WEBHOOK/URL
```

### Deploy with runbook investigation

Runbook mode polls the Deployment every 10 seconds after a rollout is detected, diagnoses failures (CrashLoopBackOff, ImagePullBackOff, ProgressDeadlineExceeded), and posts a report to Slack — no external AI service required.

```bash
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never \
  --set investigation.mode=runbook \
  --set dispatch.slackWebhookUrl=https://hooks.slack.com/services/YOUR/WEBHOOK/URL
```

## Configuration

The monitor reads from a YAML config file (`/etc/rollout-monitor/config.yaml` by default, overridden by `CONFIG_FILE`) with environment variable overrides for secrets. If no config file exists, it falls back to pure environment variable loading.

| Variable | Default | Notes |
|---|---|---|
| `KUBECONFIG_DIR` | — | **Required.** Directory of `.yaml`/`.yml`/`.conf` kubeconfig files; filename stem becomes cluster ID |
| `DISPATCH_MODE` | `log` | `log` \| `holmes` \| `slack` \| `both`; invalid values fail startup |
| `HOLMES_API_URL` | — | Required for `holmes`/`both`; posts to `/api/chat` |
| `SLACK_WEBHOOK_URL` | — | Required for `slack`/`both` |
| `SLACK_BOT_TOKEN` | — | Optional; enables per-app channel routing via Slack bot |
| `NAMESPACE_ALLOWLIST` | — | Comma-separated; if set, denylist is ignored |
| `NAMESPACE_DENYLIST` | `kube-system,kube-public,kube-node-lease` | Applies only when allowlist is empty |
| `WORKER_COUNT` | `3` | Dispatcher worker pool size |
| `DEBOUNCE_SECONDS` | `30` | Per-deployment coalescing window |
| `QUEUE_MAX_SIZE` | `100` | Event queue depth before dropping |
| `RESCAN_INTERVAL_SECONDS` | `600` | How often to re-read `KUBECONFIG_DIR` for added/changed/removed clusters |
| `WATCHER_START_TIMEOUT_SECONDS` | `30` | Timeout for initial cache sync per cluster |
| `PERSISTENCE_ENABLED` | `false` | Enable CRD-based hash persistence and audit recording |
| `PERSISTENCE_NAMESPACE` | `rollout-monitor` | Namespace for `ClusterRolloutState` and `RolloutRecord` CRDs |
| `INVESTIGATION_MODE` | `none` | `none` \| `runbook` \| `holmes` — post-rollout investigation mode |
| `DEBUG` | `false` | Enable debug-level logging |
| `TRACE` | `false` | Enable trace-level logging and the status API on port 8081 |

Namespace filtering can also be configured at runtime via a `MonitorConfig` CRD named `default` (requires `PERSISTENCE_ENABLED=true`). CRD values override env vars when present.

## How It Works

The monitor detects rollouts by hashing `spec.template` — only actual pod template changes (image, env, volumes) trigger events. Status updates, scale changes, and label/annotation edits are silently dropped.

```
kubeconfigs → Manager → ClusterWatcher (1 per cluster) → Debouncer → eventCh → Dispatcher → Targets
                ↕                ↕
          reconcileLoop    HashStore (CRD)
```

- One `ClusterWatcher` per cluster runs a `SharedInformerFactory` on Deployments. The initial LIST seeds the template hash cache (baseline); subsequent WATCH events compare against it.
- The `Debouncer` coalesces rapid changes per deployment — only the latest event within the 30-second window is dispatched.
- The `Dispatcher` fans events to all configured targets via a worker pool.
- Persistence (opt-in) stores template hashes in `ClusterRolloutState` CRDs and writes an audit `RolloutRecord` per dispatched event.
- The `Manager` periodically rescans `KUBECONFIG_DIR` to detect new, changed, or removed clusters without a restart.

See [docs/architecture.md](docs/architecture.md) for the full operational flow.

## Deployment

### Helm (recommended)

See [TESTING.md](TESTING.md) for the full Helm values reference and deployment examples including Holmes AI integration.

### Raw manifests

```bash
# Apply CRDs (required if persistence is enabled)
kubectl apply -f deploy/crds.yaml

# Apply RBAC and Deployment
kubectl apply -f deploy/deployment.yaml
```

The Deployment manifest creates a `rollout-monitor` namespace, ServiceAccount, ClusterRole (read-only on Deployments), and a single-replica Deployment with resource limits of `200m` CPU and `128Mi` memory.

## Testing

```bash
# Unit tests (no cluster required — uses fake clientsets)
go test ./...

# With race detector
go test ./... -race

# E2E tests (requires minikube)
./test/e2e.sh
```

See [TESTING.md](TESTING.md) for local development setup, E2E test scenarios, and the investigation status API.

## License

<!-- VERIFY: license type and LICENSE file presence -->
See the LICENSE file for details.
