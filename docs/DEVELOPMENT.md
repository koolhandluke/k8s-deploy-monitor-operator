<!-- generated-by: gsd-doc-writer -->
# Development Guide

## Local Setup

### Prerequisites

- Go >= 1.25.6
- Docker (for building container images)
- A directory of kubeconfig files, one per cluster (required for local runs)

### Clone and build

```bash
git clone https://github.com/koolhandluke/k8s-deploy-monitor-operator.git
cd k8s-deploy-monitor-operator

# Build all packages
go build ./...
```

### Run locally

```bash
# Requires a kubeconfigs/ directory with at least one kubeconfig file
KUBECONFIG_DIR=./kubeconfigs go run ./cmd/monitor
```

The monitor watches all `.yaml`, `.yml`, and `.conf` files under `KUBECONFIG_DIR`. The filename stem (without extension) becomes the cluster ID used in logs and events.

### Configuration for local development

The monitor reads config from `/etc/rollout-monitor/config.yaml` by default, falling back to environment variables if no file is found. For local development, environment variables are the simplest approach:

```bash
# Minimal — log-only mode
KUBECONFIG_DIR=./kubeconfigs go run ./cmd/monitor

# With Slack notifications
KUBECONFIG_DIR=./kubeconfigs \
DISPATCH_MODE=slack \
SLACK_WEBHOOK_URL=https://hooks.slack.com/services/YOUR/WEBHOOK/URL \
go run ./cmd/monitor

# Debug logging
KUBECONFIG_DIR=./kubeconfigs DEBUG=true go run ./cmd/monitor
```

See `docs/CONFIGURATION.md` for the full list of configuration options.

## Build Commands

| Command | Description |
|---|---|
| `go build ./...` | Build all packages |
| `go run ./cmd/monitor` | Run the monitor binary |
| `go run ./cmd/dispatcher` | Run the standalone dispatcher binary |
| `go test ./...` | Run all unit tests |
| `go test ./... -race` | Run tests with race detector (recommended — concurrent maps and timers) |
| `go test ./internal/watcher/ -run TestClusterWatcher_DetectsRollout -v` | Run a single named test |
| `docker build -t rollout-monitor:latest .` | Build the container image |

The `Dockerfile` produces a multi-stage build. The builder stage compiles both `rollout-monitor` and `rollout-dispatcher` binaries from `cmd/monitor` and `cmd/dispatcher` respectively. The runtime stage is `alpine:3.20`.

## Code Style

No linter or formatter configuration exists in the repository yet. Follow standard Go conventions:

- Format code with `gofmt` before committing (built into most editors)
- Use `log/slog` with a JSON handler for structured logging — no third-party logging libraries
- Comment complex logic; skip obvious code
- Use `TODO:` comments for follow-up items rather than leaving them implicit

## Project Structure

```
cmd/
  monitor/      # main binary — wires all components and handles shutdown
  dispatcher/   # standalone dispatcher binary (split-mode deployment)
internal/
  config/       # YAML config file loading + env var overrides
  watcher/      # Manager, ClusterWatcher, Debouncer, NamespaceFilter, ConfigWatcher
  dispatch/     # Dispatcher, Target interface, LogTarget, SlackTarget, SlackBotTarget, AuditTarget
  persistence/  # HashStore (ClusterRolloutState CRDs), AuditRecorder (RolloutRecord CRDs)
  diagnostic/   # RolloutAnalyzer — polls Deployments and gathers failure diagnostics
  investigation/ # Orchestrator, RunbookInvestigator, HolmesInvestigator, SlackReporter
  models/       # RolloutEvent — shared between watcher and dispatch
  trace/        # Custom slog level for trace-level logging
api/
  v1alpha1/     # CRD types: ClusterRolloutState, RolloutRecord, MonitorConfig
chart/          # Helm chart for cluster deployment
deploy/         # Raw Kubernetes manifests (CRDs, RBAC, Deployment)
test/           # E2E test script (e2e.sh)
```

## Adding a New Dispatch Target

New notification destinations implement the `Target` interface in `internal/dispatch/dispatcher.go`:

```go
// Target is something that can handle a rollout event.
type Target interface {
    Dispatch(ctx context.Context, event models.RolloutEvent) error
    Name() string
}
```

Register the new target in `cmd/monitor/main.go` by appending it to the `targets` slice before `dispatch.NewDispatcher` is called.

## Branch Conventions

No branch naming convention is formally documented. Main branch is `master`.

## PR Process

No CI pipeline or pull request template exists in the repository yet. Before submitting a PR:

- Run `go build ./...` to confirm the build passes
- Run `go test ./... -race` to confirm all tests pass with the race detector
- Confirm no regressions in the packages you modified

## Known Issues

- `go.mod` requires Go 1.25.6, which is consistent with the `Dockerfile` builder stage (`golang:1.25-alpine`). Local builds and container builds should both work as long as Go 1.25.6 or later is installed locally.
