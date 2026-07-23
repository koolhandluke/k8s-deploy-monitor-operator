# External Integrations

**Analysis Date:** 2026-07-23

## APIs & External Services

**Slack (Notifications):**
- **Incoming Webhook** - Posts rollout detection and investigation reports
  - Client: `net/http` stdlib, no SDK
  - Auth: `SLACK_WEBHOOK_URL` env var (or `slackWebhookURL` in YAML config)
  - Implementation: `internal/dispatch/slack.go` - `SlackTarget`
  - Payload: Simple JSON `{"text": "..."}` for notifications
  - Investigation reports use Slack Block Kit formatting: `internal/investigation/slack_reporter.go`
  - Timeout: 10s for notifications, 30s for investigation reports

- **Web API (Bot)** - Per-channel routing via `chat.postMessage`
  - Client: `net/http` stdlib
  - Auth: `SLACK_BOT_TOKEN` env var, sent as `Bearer` token in `Authorization` header
  - Implementation: `internal/dispatch/slack_bot.go` - `SlackBotTarget`
  - Endpoint: `https://slack.com/api/chat.postMessage`
  - Channel resolved per-event from namespace/app routing config
  - Events with empty `SlackChannel` are silently skipped (no-op)

**Holmes AI (Investigation):**
- AI-powered rollout analysis service
  - Client: `net/http` stdlib
  - Auth: None (assumes network-level access)
  - Endpoint: `{HOLMES_API_URL}/api/chat` (POST)
  - Implementation: `internal/investigation/holmes.go` - `HolmesInvestigator`
  - Request: `{"ask": "<rollout context query>"}`
  - Response: `{"analysis": "<text>"}`
  - Timeout: 5 minutes (AI analysis can be slow)
  - Retry: Single retry after 10s on connection failure
  - Required when: `investigationMode: holmes` or legacy `DISPATCH_MODE=holmes|both`

## Data Storage

**Kubernetes CRDs (Custom Resource Definitions):**
- Storage mechanism: Kubernetes API server (etcd-backed)
- Client: `sigs.k8s.io/controller-runtime` typed client + `k8s.io/client-go/dynamic` for watches
- Namespace: `PERSISTENCE_NAMESPACE` (default: `rollout-monitor`)
- CRD definitions: `deploy/crds.yaml`
- API types: `api/v1alpha1/types.go`

**CRD Types:**

| CRD | Purpose | Implementation |
|-----|---------|----------------|
| `ClusterRolloutState` | Persists template hashes per cluster (survives restarts) | `internal/persistence/hash_store.go` |
| `RolloutRecord` | Audit trail of detected rollouts with dispatch status | `internal/persistence/audit_recorder.go` |
| `MonitorConfig` | Runtime-reloadable namespace filtering | `api/v1alpha1/monitor_config.go` |

**File Storage:**
- Kubeconfig files read from `KUBECONFIG_DIR` directory (one file per cluster)
- Environment config YAML files from `ENV_CONFIG_DIR` directory
- Slack routing config from `SLACK_ROUTING_FILE` path

**Caching:**
- In-memory only: `templateCache` (map of deployment key to SHA256 hash) per `ClusterWatcher`
- In-memory `StatusCache` for investigation status API (when `TRACE=true`)
- `HashStore` uses in-memory buffer with periodic flush (every 5s) to CRDs

## Kubernetes API (Watched Clusters)

**Read-only access to remote clusters:**
- Resource: `apps/v1` Deployments (get, list, watch)
- Mechanism: `SharedInformerFactory` per cluster via `k8s.io/client-go`
- Auth: Kubeconfig files in `KUBECONFIG_DIR` (one per cluster, filename = cluster ID)
- Implementation: `internal/watcher/informer.go`
- Auto-discovery: Rescan `KUBECONFIG_DIR` every `RESCAN_INTERVAL_SECONDS` (default 600s)

**Write access to management cluster (optional):**
- CRD operations when `PERSISTENCE_ENABLED=true`
- Uses REST config from first configured cluster
- RBAC: Requires additional permissions for CRD resources in persistence namespace

## Authentication & Identity

**Auth Provider:**
- Kubernetes ServiceAccount (in-cluster) or kubeconfig files (multi-cluster)
  - In-cluster: `rest.InClusterConfig()` (dispatcher binary)
  - Multi-cluster: Kubeconfig files loaded by `internal/config/kubeconfig.go`
  - RBAC: ClusterRole defined in `deploy/deployment.yaml`

**Slack Auth:**
- Webhook URL (no OAuth) for `SlackTarget` and `SlackReporter`
- Bot token (Bearer auth) for `SlackBotTarget`

## Monitoring & Observability

**Error Tracking:**
- None (no Sentry, Datadog, etc.)

**Logs:**
- Structured JSON via `log/slog` with `slog.NewJSONHandler`
- Levels: Info (default), Debug (`DEBUG=true`), Trace (`TRACE=true`, custom level at -8)
- Trace implementation: `internal/trace/level.go`

**Status API:**
- HTTP JSON API on port 8081 (configurable via `STATUS_API_PORT`)
- Only active when `TRACE=true` and investigation mode is enabled
- Endpoints: `GET /api/v1/investigations`, `GET /api/v1/investigations/{namespace}/{name}`
- Implementation: `internal/investigation/status_api.go`

## CI/CD & Deployment

**Hosting:**
- Kubernetes (single Deployment, 1 replica)
- Manifest: `deploy/deployment.yaml`
- Namespace: `rollout-monitor`

**CI Pipeline:**
- None configured

**Container:**
- Docker multi-stage build (`Dockerfile`)
- Base: `alpine:3.20` with `ca-certificates`
- Both binaries (`rollout-monitor`, `rollout-dispatcher`) built into same image

## Environment Configuration

**Required env vars:**
- `KUBECONFIG_DIR` - Directory containing kubeconfig files (monitor binary only)

**Conditional env vars:**
- `SLACK_WEBHOOK_URL` - Required when `dispatchMode` is `slack` or `both`, or `investigationMode` is `runbook` or `holmes`
- `HOLMES_API_URL` - Required when `dispatchMode` is `holmes` or `both`, or `investigationMode` is `holmes`
- `SLACK_BOT_TOKEN` - Optional, enables per-channel Slack routing

**Secrets location:**
- Env vars (expected to be mounted from Kubernetes Secrets)
- YAML config file can hold non-secret values; env vars always override for secrets

## Webhooks & Callbacks

**Incoming:**
- Status API (when enabled): `GET /api/v1/investigations` on configurable port

**Outgoing:**
- Slack Incoming Webhook: `POST {SLACK_WEBHOOK_URL}` (rollout notifications)
- Slack Web API: `POST https://slack.com/api/chat.postMessage` (per-channel bot messages)
- Holmes API: `POST {HOLMES_API_URL}/api/chat` (investigation queries)

## Split Architecture Mode

When `DISPATCHER_SPLIT=true` (requires `PERSISTENCE_ENABLED=true`):
- Monitor writes `RolloutRecord` CRDs only, does not dispatch
- Separate `rollout-dispatcher` binary (`cmd/dispatcher/main.go`) watches `RolloutRecord` CRDs and dispatches
- `RecordWatcher` (`internal/dispatch/record_watcher.go`) uses dynamic client to watch CRDs
- `TTLCleaner` (`internal/dispatch/ttl_cleaner.go`) garbage-collects old records based on `ROLLOUT_RECORD_TTL_DAYS` (default 7)

---

*Integration audit: 2026-07-23*
