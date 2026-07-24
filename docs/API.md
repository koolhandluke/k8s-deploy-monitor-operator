<!-- generated-by: gsd-doc-writer -->
# API Reference

This document covers the two external-facing APIs in the deploy monitor:

1. **Status HTTP API** — query current investigation status per deployment (requires `TRACE=true` and an active investigation mode)
2. **Holmes API integration** — the outbound request format when `investigationMode: holmes` is configured

The deploy monitor is primarily event-driven and does not expose a general-purpose REST API. Rollout detection feeds into dispatch targets (Slack, Holmes, audit CRDs) rather than being queryable by default.

---

## Authentication

The status API has no authentication. It binds to a configurable port on the pod and is intended for internal observability only. Restrict access at the network level (e.g., pod network policy) rather than at the application layer.

The Holmes integration uses no authentication headers by default. If the Holmes instance requires authentication, configure it at the proxy or network layer in front of the Holmes endpoint.

The Slack bot target (`SLACK_BOT_TOKEN`) authenticates using a Bearer token sent in the `Authorization` header of each request to the Slack Web API.

---

## Status HTTP API

The status API is enabled only when **both** conditions are met:

- `TRACE=true` (or `trace: true` in the config file)
- `investigationMode` is set to `runbook` or `holmes` (not `none`)

When enabled, the server starts on the configured port (default `8081`, configurable via `STATUS_API_PORT` env var or `statusAPIPort` in the config file).

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/investigations` | List all cached investigation results |
| `GET` | `/api/v1/investigations/{namespace}/{name}` | Get the latest investigation result for a specific deployment |

### GET /api/v1/investigations

Returns a JSON array of all investigation results currently held in the in-memory status cache. The cache holds the most recent result per deployment key; it does not persist across restarts.

**Request**

```bash
curl http://localhost:8081/api/v1/investigations
```

**Response** — `200 OK`, `Content-Type: application/json`

```json
[
  {
    "deployment_key": "prod-cluster/payments/payments-api",
    "result": "SUCCESS",
    "duration": "1m23s",
    "timestamp": "2026-07-23T14:05:32+00:00"
  },
  {
    "deployment_key": "prod-cluster/auth/auth-service",
    "result": "FAILED",
    "failure_reason": "Pod auth-service-6b8c9-xk2lm: CrashLoopBackOff (restarts: 5)",
    "duration": "4m11s",
    "timestamp": "2026-07-23T13:58:01+00:00"
  }
]
```

Returns an empty array `[]` when no investigations have completed yet.

### GET /api/v1/investigations/{namespace}/{name}

Returns the most recent investigation result for the deployment identified by `{namespace}/{name}`. The lookup matches by suffix against the full deployment key (`{clusterID}/{namespace}/{name}`), so it finds the result regardless of which cluster the deployment lives on. If the same namespace/name pair exists on multiple clusters, the first match is returned.

**Request**

```bash
curl http://localhost:8081/api/v1/investigations/payments/payments-api
```

**Response** — `200 OK` when found

```json
{
  "deployment_key": "prod-cluster/payments/payments-api",
  "result": "SUCCESS",
  "duration": "1m23s",
  "timestamp": "2026-07-23T14:05:32+00:00"
}
```

**Response** — `405 Method Not Allowed` for non-GET methods

```
method not allowed
```

**Response** — `404 Not Found` when no matching entry exists

```
not found
```

### Response Fields

| Field | Type | Description |
|-------|------|-------------|
| `deployment_key` | string | Full key: `{clusterID}/{namespace}/{deploymentName}` |
| `result` | string | Investigation outcome. See [Result Values](#result-values) below. |
| `failure_reason` | string | Human-readable failure description. Omitted when result is `SUCCESS`. |
| `duration` | string | Time elapsed from investigation start to completion (e.g. `"1m23s"`). |
| `timestamp` | string | RFC3339 timestamp of when the investigation completed. |

### Result Values

| Value | Meaning |
|-------|---------|
| `SUCCESS` | Rollout completed and passed the soak period without errors. |
| `FAILED` | Rollout hit a definitive failure condition (crash loop, image pull error, etc.). |
| `STALLED` | Rollout stopped making forward progress before completing. |
| `INCONCLUSIVE` | Analysis could not determine a clear outcome. |
| `UNSTABLE` | Rollout converged but then regressed during the soak period. |
| `PAUSED` | Deployment was paused when the analysis timed out. |
| `DELETED` | Deployment was deleted while analysis was in progress. |

---

## Holmes API Integration

When `investigationMode: holmes` is configured, the monitor posts outbound requests to the Holmes AI API after each rollout event is detected. This is not an inbound API — the monitor is the client.

### Endpoint

```
POST {HOLMES_API_URL}/api/chat
```

`HOLMES_API_URL` must not include a trailing slash. The monitor appends `/api/chat` exactly.

### Request

```
POST /api/chat HTTP/1.1
Content-Type: application/json
```

```json
{
  "ask": "Deployment payments-api in namespace payments on cluster prod-cluster rolled out: gcr.io/myorg/payments:v1.2.3 -> gcr.io/myorg/payments:v1.2.4. Analyse the rollout health and report any issues."
}
```

The `ask` field is a natural-language query built from the rollout event. It includes the deployment name, namespace, cluster ID, old image list, and new image list.

### Response

The monitor expects a JSON response with an `analysis` field:

```json
{
  "analysis": "The deployment rolled out successfully. All pods are running and ready. No errors detected in pod events or logs."
}
```

If the response body is not valid JSON, the raw response body is used as the analysis text.

The monitor classifies the Holmes response as `FAILED` if the analysis text contains the words `fail`, `error`, or `crash` (case-insensitive). All other responses are classified as `SUCCESS`.

### Retry behavior

On network error, the monitor retries once after a 10-second delay. If the retry also fails, the investigation is marked as failed with the error. A `4xx` or `5xx` HTTP status from Holmes is treated as an immediate failure with no retry.

The HTTP client timeout for Holmes requests is 5 minutes, to allow Holmes sufficient time to query the Kubernetes API and generate its analysis.

---

## Slack Webhook Integration

When `dispatchMode: slack` or `dispatchMode: both` is configured, the monitor posts outbound webhook messages to Slack. This is an outbound integration, not an inbound API.

### Rollout notification (webhook)

```
POST {SLACK_WEBHOOK_URL}
Content-Type: application/json
```

```json
{
  "text": "*Rollout detected:* `payments-api` (`payments`) on *prod-cluster*\ngcr.io/myorg/payments:v1.2.3 → gcr.io/myorg/payments:v1.2.4"
}
```

### Investigation report (webhook)

When an investigation completes with a non-success result, the `SlackReporter` posts a Block Kit payload to the same webhook:

```json
{
  "text": ":x: Rollout FAILED: prod-cluster/payments/payments-api",
  "blocks": [
    {
      "type": "header",
      "text": { "type": "plain_text", "text": ":x: Rollout *FAILED*: `payments-api`" }
    },
    {
      "type": "section",
      "text": {
        "type": "mrkdwn",
        "text": "*Namespace:* `payments` | *Cluster:* `prod-cluster`\n*Duration:* 4m11s\n*Reason:* CrashLoopBackOff"
      }
    }
  ]
}
```

For successful rollouts, a plain text message is sent:

```json
{
  "text": ":white_check_mark: Rollout *SUCCESS*: `payments-api` (`payments`) on *prod-cluster* — completed in 1m23s"
}
```

Log snippets included in the Slack message are capped at 3,000 characters.

---

## Slack Bot API Integration

When `SLACK_BOT_TOKEN` is set, the monitor sends per-channel notifications using the Slack Web API `chat.postMessage` endpoint. This is used for per-app channel routing configured via the Slack routing file.

```
POST https://slack.com/api/chat.postMessage
Content-Type: application/json; charset=utf-8
Authorization: Bearer {SLACK_BOT_TOKEN}
```

```json
{
  "channel": "C012AB3CD",
  "text": "*Rollout detected:* `payments-api` (`payments`) on *prod-cluster*\ngcr.io/myorg/payments:v1.2.3 → gcr.io/myorg/payments:v1.2.4"
}
```

The `channel` value is resolved from the event's `SlackChannel` field, which is populated via the namespace → app → channel lookup at event enrichment time. Events with no resolved channel are silently skipped by the bot target.

---

## CRD API (Kubernetes)

When `persistenceEnabled: true`, the monitor reads and writes three custom resource types. These are Kubernetes resources, not HTTP endpoints — they are accessed via `kubectl` or the Kubernetes API server.

### ClusterRolloutState (`crs`)

Persists template hashes per cluster. One resource per cluster, named after the cluster ID.

```
apiVersion: deploy-monitor.example.com/v1alpha1
kind: ClusterRolloutState
```

| Field | Description |
|-------|-------------|
| `spec.clusterID` | Cluster identifier (kubeconfig filename stem) |
| `spec.templateHashes` | Map of `namespace/deploymentName` → SHA256 of `spec.template` |
| `status.trackedDeployments` | Number of deployments currently tracked |
| `status.lastSyncTime` | When hashes were last flushed (every 5 seconds) |

### RolloutRecord (`rr`)

Audit trail — one resource per detected rollout event.

```
apiVersion: deploy-monitor.example.com/v1alpha1
kind: RolloutRecord
```

| Field | Description |
|-------|-------------|
| `spec.clusterID` | Cluster where the rollout was detected |
| `spec.namespace` | Deployment namespace |
| `spec.deployment` | Deployment name |
| `spec.oldImages` | Container images before rollout |
| `spec.newImages` | Container images after rollout |
| `spec.oldTemplateHash` | SHA256 of previous `spec.template` |
| `spec.newTemplateHash` | SHA256 of new `spec.template` |
| `spec.detectedAt` | Timestamp of rollout detection |
| `status.phase` | One of `Detected`, `Processing`, `Dispatched`, `Failed` |
| `status.dispatchTargets` | List of targets that successfully received the event |
| `status.error` | Error message if dispatch failed |

### MonitorConfig (`mc`)

Runtime-reloadable namespace filtering. A single cluster-scoped instance named `default` is expected.

```
apiVersion: deploy-monitor.example.com/v1alpha1
kind: MonitorConfig
```

| Field | Description |
|-------|-------------|
| `spec.namespaceAllowlist` | If non-empty, restricts monitoring to only these namespaces |
| `spec.namespaceDenylist` | Excludes these namespaces; ignored if allowlist is non-empty |
| `status.active` | Whether the monitor has picked up this config |
| `status.lastApplied` | Timestamp of last application |

CRD values override env vars and YAML config file values when present.
