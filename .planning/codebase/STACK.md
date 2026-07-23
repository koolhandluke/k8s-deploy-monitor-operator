# Technology Stack

**Analysis Date:** 2026-07-23

## Languages

**Primary:**
- Go 1.25.6 - All application code (`cmd/`, `internal/`, `api/`)

**Secondary:**
- YAML - Configuration files, Kubernetes manifests, CRD definitions (`deploy/`, config files)

## Runtime

**Environment:**
- Go 1.25.6 (specified in `go.mod`)
- Alpine Linux 3.20 (production container base)

**Package Manager:**
- Go modules
- Lockfile: `go.sum` present

## Frameworks

**Core:**
- `k8s.io/client-go` v0.31.0 - Kubernetes API client, informers, fake clientsets
- `k8s.io/apimachinery` v0.31.0 - Kubernetes object types, runtime scheme
- `k8s.io/api` v0.31.0 - Kubernetes API types (apps/v1 Deployments)
- `sigs.k8s.io/controller-runtime` v0.19.0 - CRD client, scheme registration, status subresource updates

**Testing:**
- Go standard `testing` package
- `k8s.io/client-go/kubernetes/fake` - Fake Kubernetes clientsets for unit tests
- `net/http/httptest` - HTTP test servers for dispatch target tests

**Build/Dev:**
- `go build` - Direct compilation, no build framework
- Docker multi-stage build (`Dockerfile`) - `golang:1.25-alpine` builder, `alpine:3.20` runtime

## Key Dependencies

**Critical:**
- `k8s.io/client-go` v0.31.0 - Core Kubernetes interaction: informers, REST config, kubeconfig loading
- `sigs.k8s.io/controller-runtime` v0.19.0 - CRD CRUD operations for persistence layer (HashStore, AuditRecorder)
- `k8s.io/client-go/dynamic` - Dynamic client for CRD watches (ConfigWatcher, RecordWatcher)

**Infrastructure:**
- `gopkg.in/yaml.v3` - YAML config file parsing (`internal/config/config.go`)
- `log/slog` (stdlib) - Structured JSON logging throughout
- `crypto/sha256` (stdlib) - Template hash computation for rollout detection
- `net/http` (stdlib) - Slack webhooks, Holmes API calls, status API server

## Configuration

**Environment:**
- Primary: YAML config file at `/etc/rollout-monitor/config.yaml` (override with `CONFIG_FILE` env var)
- Fallback: Pure environment variable loading for backward compatibility
- Env var overrides always applied on top of YAML config (for secrets via K8s Secrets)
- Config struct: `internal/config/config.go` - `Config` type with YAML tags

**Key env vars (secrets, always override YAML):**
- `SLACK_WEBHOOK_URL` - Slack incoming webhook
- `SLACK_BOT_TOKEN` - Slack bot token for per-channel routing
- `HOLMES_API_URL` - Holmes AI investigation API
- `KUBECONFIG_DIR` - Directory of kubeconfig files (one per cluster)

**Build:**
- `Dockerfile` - Multi-stage Docker build, CGO_ENABLED=0, static Linux binary
- No Makefile, no CI config

## Two Binaries

The project produces two separate binaries from two entry points:

| Binary | Entry Point | Purpose |
|--------|-------------|---------|
| `rollout-monitor` | `cmd/monitor/main.go` | Watches clusters, detects rollouts, dispatches events |
| `rollout-dispatcher` | `cmd/dispatcher/main.go` | Standalone CRD-driven dispatcher (split mode) |

Both binaries share the same `Config` struct and are built into the same container image.

## Platform Requirements

**Development:**
- Go 1.25.6+
- A `kubeconfigs/` directory with at least one kubeconfig file
- No linter or CI config exists

**Production:**
- Kubernetes cluster with RBAC (ClusterRole: get/list/watch on deployments)
- Container runtime (Docker/containerd)
- Resource limits: 50m-200m CPU, 64Mi-128Mi memory (`deploy/deployment.yaml`)
- Optional: CRD installation (`deploy/crds.yaml`) when `PERSISTENCE_ENABLED=true`

---

*Stack analysis: 2026-07-23*
