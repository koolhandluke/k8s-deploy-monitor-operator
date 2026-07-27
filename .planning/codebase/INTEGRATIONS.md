# External Integrations

**Analysis Date:** 2026-07-26

## APIs & External Services

**Slack (Notifications):**
- **Incoming Webhook** - Posts rollout notifications to a single channel
  - Client: `net/http` (standard library, no SDK)
  - Auth: `SLACK_WEBHOOK_URL` env var
  - Implementation: `internal/dispatch/slack.go` - `SlackTarget`
  - Payload: JSON `{"text": "..."}` to webhook URL

- **Web API (Bot)** - Posts per-channel rollout notifications via `chat.postMessage`
  - Client: `net/http` (standard library, no SDK)
  - Auth: `SLACK_BOT_TOKEN` env var (Bearer token)
  - API endpoint: `https://slack.com/api/chat.postMessage`
  - Implementation: `internal/dispatch/slack_bot.go` - `SlackBotTarget`
  - Payload: JSON `{"channel": "...", "text": "..."}`
  - Skips dispatch silently when `SlackChannel` is empty on the event

- **Slack Reporter (Investigation Results)** - Posts diagnostic/investigation results using Block Kit
  - Client: `net/http`
  - Auth: `SLACK_WEBHOOK_URL` env var (same webhook as above)
  - Implementation: `internal/investigation/slack_reporter.go` - `SlackReporter`

**Holmes AI (Investigation):**
- AI-powered rollout analysis service
  - Client: `net/http` (standard library)
  - Auth: None (no auth header sent)
  - Endpoint: `{HOLMES_API_URL}/api/chat` (POST)
  - Request: `{"ask": "..."}` with deployment context
  - Response: `{"analysis": "..."}` - free-text analysis
  - Retry: 1 retry after 10s on connection failure
  - Timeout: 5 minutes per request
  - Implementation: `internal/investigation/holmes.go` - `HolmesInvestigator`
  - Required when: `DISPATCH_MODE=holmes|both` or `investigationMode=holmes`

## Kubernetes API (Primary Integration)

**Multi-Cluster Watching (Read-Only):**
- Watches Deployments via `SharedInformerFactory` across multiple clusters
- One kubeconfig file per cluster in `KUBECONFIG_DIR`
- Client: `k8s.io/client-go` informers (watch + list)
- RBAC: `get`, `list`, `watch` on `apps/deployments` (ClusterRole in `deploy/deployment.yaml`)
- Never mutates watched clusters
- Implementation: `internal/watcher/` - `Manager` + `ClusterWatcher`

**CRD Persistence (Read-Write, optional):**
- Writes to CRDs in a single "home" cluster (first cluster in config)
- Client: `sigs.k8s.io/controller-runtime` typed client
- CRD types defined in `api/v1alpha1/`:
  - `ClusterRolloutState` (`api/v1alpha1/types.go`) - Persisted template hashes per cluster
  - `RolloutRecord` (`api/v1alpha1/types.go`) - Audit trail of dispatched events
  - `MonitorConfig` (`api/v1alpha1/monitor_config.go`) - Runtime namespace filter config
  - `AppWatchConfig` (`api/v1alpha1/app_watch_config.go`) - Watchlist app registrations
- CRD manifests: `deploy/crds.yaml`
- Enabled when: `PERSISTENCE_ENABLED=true`

**Dynamic Client:**
- `k8s.io/client-go/dynamic` - Used for ConfigWatcher (watching MonitorConfig CRD) and RecordWatcher (watching RolloutRecord CRDs in dispatcher)
- Implementation: `internal/watcher/config_watcher.go`, `internal/dispatch/record_watcher.go`

## Data Storage

**Databases:**
- None - All state stored as Kubernetes CRDs or in-memory

**In-Memory State:**
- `templateCache` in `ClusterWatcher` - `map[string]string` of deployment key to SHA256 hash
- `WatchlistStore` in-memory index - O(1) lookup by (clusterID, namespace)
- `Debouncer` pending events map - Per-deployment-key timer + latest event

**File Storage:**
- Kubeconfig directory (`KUBECONFIG_DIR`) - Read-only, one file per cluster
- Optional env config directory (`ENV_CONFIG_DIR`) - Per-app environment configs
- Optional slack routing file (`SLACK_ROUTING_FILE`) - App-to-channel mapping

**Caching:**
- `HashStore` batch write cache - Pending hashes flushed to CRDs every 5s with jitter (`internal/persistence/hash_store.go`)

## Authentication & Identity

**Kubernetes Auth:**
- In-cluster: ServiceAccount token (production)
- Out-of-cluster: Kubeconfig files with embedded credentials
- No custom auth layer

**Slack Auth:**
- Webhook URL (no OAuth flow, just a URL secret)
- Bot token (Bearer token for Web API)

**Holmes Auth:**
- No authentication (plain HTTP POST)

## Monitoring & Observability

**Error Tracking:**
- None (no Sentry, Datadog, etc.)

**Logs:**
- Structured JSON logging via `log/slog` with `slog.NewJSONHandler` (`cmd/monitor/main.go` line 50)
- Log levels: Info (default), Debug (`DEBUG=true`), Trace (`TRACE=true`, custom level in `internal/trace/`)
- No external log aggregation configured

**Metrics:**
- None (no Prometheus metrics endpoint)

## HTTP APIs (Self-Hosted)

**Watchlist API (optional):**
- Port: `WATCHLIST_PORT` (default 8080)
- Endpoints: `GET/PUT/DELETE /api/v1/watchlist`
- Purpose: Dynamic app watch registration
- Implementation: `internal/watchlist/handler.go`
- Enabled when: `WATCHLIST_ENABLED=true` and `PERSISTENCE_ENABLED=true`

**Status API (optional):**
- Port: `STATUS_API_PORT` (default 8081)
- Purpose: Investigation status cache for trace-level debugging
- Implementation: `internal/investigation/status_api.go`
- Enabled when: `TRACE=true` and investigation mode is active

## CI/CD & Deployment

**Hosting:**
- Kubernetes (runs as a Deployment in `rollout-monitor` namespace)
- Helm chart available at `chart/deploy-monitor/`

**CI Pipeline:**
- None configured (no `.github/`, `.gitlab-ci.yml`, or Makefile)

**Container:**
- Multi-stage Docker build (`Dockerfile`)
- Builder: `golang:1.25-alpine`
- Runtime: `alpine:3.20` with `ca-certificates`
- Two binaries: `rollout-monitor`, `rollout-dispatcher`

## Environment Configuration

**Required env vars:**
- `KUBECONFIG_DIR` - Directory containing kubeconfig files (one per cluster)

**Conditional env vars:**
- `SLACK_WEBHOOK_URL` - Required when `DISPATCH_MODE=slack|both` or investigation mode is enabled
- `SLACK_BOT_TOKEN` - Required for per-channel Slack routing
- `HOLMES_API_URL` - Required when `DISPATCH_MODE=holmes|both` or `investigationMode=holmes`

**Secrets location:**
- Env vars (expected to be mounted from Kubernetes Secrets)
- No `.env` files in repo

## Webhooks & Callbacks

**Incoming:**
- `PUT /api/v1/watchlist` - Register app watch (triggers immediate reconcile)
- `DELETE /api/v1/watchlist` - Unregister app watch
- `GET /api/v1/watchlist` - List watched apps

**Outgoing:**
- Slack incoming webhook (`SLACK_WEBHOOK_URL`)
- Slack Web API `chat.postMessage` (`SLACK_BOT_TOKEN`)
- Holmes `/api/chat` (`HOLMES_API_URL`)

---

*Integration audit: 2026-07-26*
