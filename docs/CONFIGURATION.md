<!-- generated-by: gsd-doc-writer -->
# Configuration

The rollout monitor supports two configuration methods: a YAML config file (preferred) and
environment variables (legacy, still fully supported). When both are present, the YAML file
is loaded first and environment variables override specific fields — primarily secrets that
should not be stored in config files.

## Config File

The binary looks for a YAML config file at `/etc/rollout-monitor/config.yaml` by default.
Override the path with the `CONFIG_FILE` environment variable. If no file is found, the
binary falls back to pure environment variable loading for backward compatibility.

**Minimal working example (log-only mode):**

```yaml
kubeconfigDir: /etc/kubeconfig
dispatchMode: log
debounceSeconds: 30
workerCount: 3
```

**Full example with Slack and investigation:**

```yaml
# Cluster access
kubeconfigDir: /etc/kubeconfig

# Dispatch
dispatchMode: slack
# slackWebhookURL is better set via SLACK_WEBHOOK_URL env var (secret)

# Investigation
investigationMode: runbook
investigationMaxConcurrent: 10

# Namespace filtering
namespaceAllowlist: []
namespaceDenylist:
  - kube-system
  - kube-public
  - kube-node-lease

# Tuning
workerCount: 3
debounceSeconds: 30
queueMaxSize: 100
rescanIntervalSeconds: 600
watcherStartTimeoutSeconds: 30

# Persistence
persistenceEnabled: true
persistenceNamespace: rollout-monitor

# Logging
debug: false
trace: false
statusAPIPort: 8081
```

## Environment Variables

All settings can be configured via environment variables. When a YAML config file is present,
the following env vars still take precedence (intended for secrets injected via Kubernetes
Secret mounts): `SLACK_WEBHOOK_URL`, `SLACK_BOT_TOKEN`, `HOLMES_API_URL`, `KUBECONFIG_DIR`,
`ENV_CONFIG_DIR`, `SLACK_ROUTING_FILE`.

When no YAML config file is present, all env vars are loaded as the primary configuration source.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `CONFIG_FILE` | No | `/etc/rollout-monitor/config.yaml` | Path to the YAML config file |
| `KUBECONFIG_DIR` | **Yes** | — | Directory of kubeconfig files (`.yaml`/`.yml`/`.conf`); filename stem becomes the cluster ID |
| `DISPATCH_MODE` | No | `log` | Notification targets: `log` \| `holmes` \| `slack` \| `both` |
| `HOLMES_API_URL` | Conditional | — | Required when `DISPATCH_MODE=holmes` or `both`; posts to `/api/chat` |
| `SLACK_WEBHOOK_URL` | Conditional | — | Required when `DISPATCH_MODE=slack` or `both`; also required for `investigationMode=runbook` or `holmes` |
| `SLACK_BOT_TOKEN` | No | — | Enables per-app Slack channel routing when set alongside `ENV_CONFIG_DIR` and `SLACK_ROUTING_FILE` |
| `ENV_CONFIG_DIR` | No | — | Directory of per-environment YAML files mapping clusters/namespaces to app names |
| `SLACK_ROUTING_FILE` | No | — | Path to a YAML file mapping app names to Slack channel IDs |
| `INVESTIGATION_MODE` | No | `none` | Post-rollout investigation: `none` \| `runbook` \| `holmes` |
| `INVESTIGATION_MAX_CONCURRENT` | No | `10` | Maximum concurrent investigations |
| `NAMESPACE_ALLOWLIST` | No | — | Comma-separated; if set, only these namespaces are watched and `NAMESPACE_DENYLIST` is ignored |
| `NAMESPACE_DENYLIST` | No | `kube-system,kube-public,kube-node-lease` | Comma-separated namespaces to exclude; applies only when allowlist is empty |
| `WORKER_COUNT` | No | `3` | Dispatcher worker goroutines |
| `DEBOUNCE_SECONDS` | No | `30` | Coalescing window per deployment — only the latest event within this window is dispatched |
| `QUEUE_MAX_SIZE` | No | `100` | Event channel buffer depth; a full queue drops events with a warning |
| `RESCAN_INTERVAL_SECONDS` | No | `600` | How often to re-read `KUBECONFIG_DIR` for added, changed, or removed cluster kubeconfigs |
| `WATCHER_START_TIMEOUT_SECONDS` | No | `30` | Timeout for initial cache sync per cluster; prevents hanging on unreachable clusters |
| `PERSISTENCE_ENABLED` | No | `false` | Enables CRD-based hash persistence (`ClusterRolloutState`) and audit recording (`RolloutRecord`) |
| `PERSISTENCE_NAMESPACE` | No | `rollout-monitor` | Namespace for `ClusterRolloutState` and `RolloutRecord` CRDs; only active when `PERSISTENCE_ENABLED=true` |
| `DISPATCHER_SPLIT` | No | `false` | Split mode: monitor writes `RolloutRecord` CRDs only; a separate dispatcher service handles notifications. Requires `PERSISTENCE_ENABLED=true` |
| `ROLLOUT_RECORD_TTL_DAYS` | No | `7` | Age in days after which `RolloutRecord` CRDs are deleted by the dispatcher's TTL cleaner |
| `STATUS_API_PORT` | No | `8081` | Port for the investigation status HTTP API; only starts when `TRACE=true` |
| `DEBUG` | No | `false` | Enables debug-level structured log output |
| `TRACE` | No | `false` | Enables trace-level logging (investigation pipeline detail) and the status API |

Malformed integer values silently fall back to their defaults rather than causing a startup error.

## Required vs Optional Settings

The binary exits on startup if these conditions are not met:

- `KUBECONFIG_DIR` (or `kubeconfigDir` in YAML) must be set and the directory must contain at
  least one valid kubeconfig file.
- `HOLMES_API_URL` is required when `dispatchMode` is `holmes` or `both`.
- `SLACK_WEBHOOK_URL` is required when `dispatchMode` is `slack` or `both`.
- `SLACK_WEBHOOK_URL` is required when `investigationMode` is `runbook` or `holmes`.
- `HOLMES_API_URL` is additionally required when `investigationMode` is `holmes`.
- `PERSISTENCE_ENABLED=true` is required when `DISPATCHER_SPLIT=true`.
- `dispatchMode` must be one of `log`, `holmes`, `slack`, or `both` — invalid values fail startup.
- `investigationMode` must be one of `none`, `runbook`, or `holmes`.

## Per-Environment App Config

When `ENV_CONFIG_DIR` is set, the monitor loads all YAML files in that directory. Each file
defines which clusters and namespaces belong to each app. This enables per-app Slack channel
routing when combined with `SLACK_ROUTING_FILE` and `SLACK_BOT_TOKEN`.

**Example env config file** (`/etc/env-configs/prod.yaml`):

```yaml
clusters:
  - name: prod-us-east
    deployments:
      - app: payments-service
        namespaces: [payments, payments-jobs]
      - app: checkout-api
        namespaces: [checkout]
  - name: prod-eu-west
    deployments:
      - app: payments-service
        namespaces: [payments]
```

**Example Slack routing file** (`/etc/slack-routing.yaml`):

```yaml
payments-service: "#payments-deploys"
checkout-api: "#checkout-deploys"
```

The `name` field in cluster entries must match the stem of the kubeconfig filename in
`KUBECONFIG_DIR` (e.g., `prod-us-east` matches `prod-us-east.yaml`).

## Runtime Namespace Filtering via CRD

When `PERSISTENCE_ENABLED=true`, namespace filtering can be updated at runtime without
restarting the monitor by applying a `MonitorConfig` CRD named `default`. CRD values
override env var settings when present.

```yaml
apiVersion: monitor.example.com/v1alpha1
kind: MonitorConfig
metadata:
  name: default
spec:
  namespaceAllowlist:
    - production
    - staging
  namespaceDenylist: []
```

The `MonitorConfig` CRD is cluster-scoped. The monitor's `ConfigWatcher` picks up changes
automatically. `namespaceAllowlist` takes precedence over `namespaceDenylist` when both are set.

## Dispatch Modes

| Mode | Behavior |
| --- | --- |
| `log` | Writes structured JSON log lines only (default; no external dependencies) |
| `slack` | Posts rollout notifications to the configured Slack webhook URL |
| `holmes` | Sends events to the Holmes AI API for analysis (`/api/chat`) |
| `both` | Sends to both Holmes and Slack |

## Investigation Modes

Investigation runs after a rollout event is dispatched and reports findings to Slack.

| Mode | Behavior |
| --- | --- |
| `none` | No post-rollout investigation (default) |
| `runbook` | Runs the built-in diagnostic runbook and reports results to Slack |
| `holmes` | Delegates investigation to the Holmes AI API and reports results to Slack |

## Split Mode (Dispatcher Service)

Setting `DISPATCHER_SPLIT=true` decouples notification delivery from the monitor process.
In this mode the monitor writes `RolloutRecord` CRDs for each event and exits the dispatch
path. A separate `dispatcher` binary watches those CRDs and handles Slack/Holmes delivery.

Both binaries use the same config file and env var schema. The dispatcher binary additionally
uses `ROLLOUT_RECORD_TTL_DAYS` to age out old `RolloutRecord` CRDs.

Split mode requires:

```bash
PERSISTENCE_ENABLED=true
DISPATCHER_SPLIT=true
PERSISTENCE_NAMESPACE=rollout-monitor  # or your chosen namespace
```

## Helm Chart Values

The Helm chart at `chart/deploy-monitor/` maps its values to the env var configuration:

| Helm value | Env var |
| --- | --- |
| `dispatch.mode` | `DISPATCH_MODE` |
| `dispatch.holmesApiUrl` | `HOLMES_API_URL` |
| `dispatch.slackWebhookUrl` | `SLACK_WEBHOOK_URL` |
| `namespaceFilter.allowlist` | `NAMESPACE_ALLOWLIST` |
| `namespaceFilter.denylist` | `NAMESPACE_DENYLIST` |
| `tuning.workerCount` | `WORKER_COUNT` |
| `tuning.debounceSeconds` | `DEBOUNCE_SECONDS` |
| `tuning.queueMaxSize` | `QUEUE_MAX_SIZE` |
| `persistence.enabled` | `PERSISTENCE_ENABLED` |
| `persistence.namespace` | `PERSISTENCE_NAMESPACE` |
| `investigation.mode` | `INVESTIGATION_MODE` |
| `investigation.maxConcurrent` | `INVESTIGATION_MAX_CONCURRENT` |
| `logging.debug` | `DEBUG` |
| `logging.trace` | `TRACE` |
| `logging.statusAPIPort` | `STATUS_API_PORT` |
| `dispatcher.enabled` | `DISPATCHER_SPLIT` |
| `dispatcher.ttlDays` | `ROLLOUT_RECORD_TTL_DAYS` |
| `kubeconfig.configMapName` | Mounts ConfigMap to `/etc/kubeconfig`, sets `KUBECONFIG_DIR` |
