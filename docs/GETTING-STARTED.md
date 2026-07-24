<!-- generated-by: gsd-doc-writer -->
# Getting Started

## Prerequisites

Before running the monitor, ensure the following are installed and available:

| Tool | Version | Notes |
|------|---------|-------|
| Go | >= 1.25.6 | Required for building from source |
| Docker | any recent | Required for building the container image |
| Helm | 3.x | Required for cluster deployment |
| kubectl | any recent | Required for interacting with the cluster |
| minikube (or equivalent) | any recent | Required for local testing |

You also need a directory of kubeconfig files — one file per cluster. The filename stem (without extension) becomes the cluster ID used in events and CRD names.

## Installation Steps

### 1. Clone the repository

```bash
git clone https://github.com/koolhandluke/k8s-deploy-monitor-operator.git
cd k8s-deploy-monitor-operator
```

### 2. Install dependencies

```bash
go mod download
```

### 3. Build the binaries

```bash
go build ./...
```

This produces two binaries: `rollout-monitor` (the watcher) and `rollout-dispatcher` (the optional split-mode dispatcher).

## First Run

### Option A: Run locally against a kubeconfig directory

The fastest path — no cluster deployment needed. Point `KUBECONFIG_DIR` at a directory containing at least one kubeconfig file:

```bash
mkdir -p ./kubeconfigs
# Copy your kubeconfig(s) into ./kubeconfigs/
KUBECONFIG_DIR=./kubeconfigs go run ./cmd/monitor
```

You should see structured JSON log output including a `starting rollout monitor` line listing the number of clusters detected. After the initial LIST sync, the monitor is ready and will emit events on deployment template changes.

### Option B: Deploy to a local minikube cluster via Helm

```bash
# Start minikube if not already running
minikube start

# Build the image and load it into minikube's registry
docker build -t rollout-monitor:latest .
minikube image load rollout-monitor:latest

# Create the namespace first
kubectl create namespace rollout-monitor

# Deploy in log-only mode (no external notifications)
helm upgrade --install deploy-monitor ./chart/deploy-monitor/ \
  --namespace rollout-monitor \
  --set image.repository=rollout-monitor \
  --set image.pullPolicy=Never

# Stream logs
kubectl logs -n rollout-monitor -l app.kubernetes.io/name=deploy-monitor -f
```

## Verifying Detection Works

Trigger a rollout in any watched namespace:

```bash
# Create a test deployment
kubectl create deployment test-rollout --image=nginx:1.25 -n default

# Update the image — this changes spec.template and triggers detection
kubectl set image deployment/test-rollout nginx=nginx:1.26 -n default

# After the 30-second debounce window, the event appears in monitor logs
kubectl logs -n rollout-monitor -l app.kubernetes.io/name=deploy-monitor -f
```

Look for a log line with `"msg":"dispatched event"` containing the deployment name, namespace, and cluster ID.

Clean up:

```bash
kubectl delete deployment test-rollout -n default
```

## Common Setup Issues

**`KUBECONFIG_DIR is required` startup failure**

The monitor exits immediately if `KUBECONFIG_DIR` is not set or resolves to an empty directory. Ensure the directory exists and contains at least one `.yaml`, `.yml`, or `.conf` kubeconfig file.

**`no clusters configured` startup failure**

The directory exists but no kubeconfig files were found. Kubeconfig filenames must have the extension `.yaml`, `.yml`, or `.conf`. Other extensions are ignored.

**Image pull errors when using minikube**

If the pod shows `ImagePullBackOff`, the image was not loaded into minikube's registry. Run `minikube image load rollout-monitor:latest` after every `docker build`. Use `--set image.pullPolicy=Never` in the Helm command to prevent Kubernetes from trying to pull from a remote registry.

**Watcher startup timeout**

If a cluster is unreachable, the watcher will time out after 30 seconds (controlled by `WATCHER_START_TIMEOUT_SECONDS`) and queue the cluster for retry with exponential backoff. The monitor continues watching other clusters. Check the kubeconfig file for that cluster and ensure the API server is reachable.

**Events do not appear for 30 seconds**

This is expected. The `Debouncer` coalesces rapid changes per deployment — only the latest change within the 30-second window is dispatched. Set `DEBOUNCE_SECONDS=5` for local testing if you want faster feedback.

**Helm CRDs not removed on uninstall**

```bash
# CRDs must be deleted manually after helm uninstall
kubectl delete crd clusterrolloutstates.deploy-monitor.io rolloutrecords.deploy-monitor.io monitorconfigs.deploy-monitor.io
```

## Next Steps

- **Configuration** — See [CONFIGURATION.md](CONFIGURATION.md) for the full environment variable reference, YAML config file format, and per-environment overrides.
- **Development** — See [DEVELOPMENT.md](DEVELOPMENT.md) for local development setup, build commands, and code style.
- **Testing** — See [TESTING.md](TESTING.md) for unit test commands, E2E test scenarios, investigation modes (runbook and Holmes AI), and the status API.
- **Architecture** — See [architecture.md](architecture.md) for the full component diagram and operational flow.
