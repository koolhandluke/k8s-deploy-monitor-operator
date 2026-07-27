# Technology Stack

**Analysis Date:** 2026-07-26

## Languages

**Primary:**
- Go 1.25.6 - All application code (`go.mod` line 3)

**Secondary:**
- YAML - Kubernetes manifests, Helm chart, configuration files

## Runtime

**Environment:**
- Go 1.25.6 (specified in `go.mod`)
- Alpine Linux 3.20 (production container base, `Dockerfile` line 10)

**Package Manager:**
- Go modules
- Lockfile: `go.sum` present

## Frameworks

**Core:**
- `k8s.io/client-go` v0.31.0 - Kubernetes API client, SharedInformerFactory for watching Deployments
- `k8s.io/api` v0.31.0 - Kubernetes API types
- `k8s.io/apimachinery` v0.31.0 - Kubernetes API machinery (meta types, runtime scheme, API errors)
- `sigs.k8s.io/controller-runtime` v0.19.0 - CRD client for persistence (HashStore, AuditRecorder, WatchlistStore)

**Testing:**
- Go standard `testing` package
- `k8s.io/client-go/kubernetes/fake` - Fake Kubernetes clientsets for unit tests
- `sigs.k8s.io/controller-runtime/pkg/client/fake` - Fake controller-runtime client for CRD tests
- `net/http/httptest` - HTTP test server for dispatch target tests

**Build/Dev:**
- `go build` - Direct Go build (no Makefile or build tool)
- Docker multi-stage build (`Dockerfile`)
- Helm chart (`chart/deploy-monitor/`)

## Key Dependencies

**Critical (direct):**
- `k8s.io/client-go` v0.31.0 - Core Kubernetes API client; informers, REST config, fake clientsets
- `k8s.io/api` v0.31.0 - Kubernetes resource types (apps/v1 Deployment)
- `k8s.io/apimachinery` v0.31.0 - API machinery: ObjectMeta, runtime.Scheme, API errors, types.NamespacedName
- `sigs.k8s.io/controller-runtime` v0.19.0 - CRD CRUD via typed client (persistence layer)

**Infrastructure (indirect, notable):**
- `gopkg.in/yaml.v3` - YAML config file parsing (`internal/config/config.go`)
- `golang.org/x/oauth2` v0.21.0 - OAuth2 transport (pulled in by client-go)
- `golang.org/x/time` v0.3.0 - Rate limiting (pulled in by client-go)
- `k8s.io/klog/v2` v2.130.1 - Kubernetes logging (indirect, used by client-go internals)

## Configuration

**Environment:**
- YAML config file at `/etc/rollout-monitor/config.yaml` (default) or path from `CONFIG_FILE` env var (`internal/config/config.go`)
- Env var overrides for secrets: `SLACK_WEBHOOK_URL`, `SLACK_BOT_TOKEN`, `HOLMES_API_URL`, `KUBECONFIG_DIR`
- Full env-var-only mode as fallback when no config file exists
- All config loaded in `internal/config/config.go` via `config.Load()`

**Key env vars (see CLAUDE.md for full table):**
- `KUBECONFIG_DIR` (required) - Directory of kubeconfig files, one per cluster
- `CONFIG_FILE` - Path to YAML config (default: `/etc/rollout-monitor/config.yaml`)
- `DISPATCH_MODE` - `log|holmes|slack|both` (default: `log`)
- `PERSISTENCE_ENABLED` - Enable CRD-based persistence (default: `false`)
- `WATCHLIST_ENABLED` - Enable HTTP watchlist API (default: `false`)

**Build:**
- `Dockerfile` - Multi-stage build: `golang:1.25-alpine` builder, `alpine:3.20` runtime
- `deploy/deployment.yaml` - Kubernetes Deployment manifest with RBAC
- `deploy/crds.yaml` - Custom Resource Definitions
- `chart/deploy-monitor/` - Helm chart with `Chart.yaml`, `values.yaml`, `templates/`, `crds/`

## Binaries

**Two binaries built from `cmd/`:**
- `cmd/monitor/main.go` - Main monitor process: watches clusters, detects rollouts, dispatches notifications
- `cmd/dispatcher/main.go` - Standalone dispatcher: watches RolloutRecord CRDs and dispatches notifications (split-mode architecture)

Both binaries are built in the Dockerfile and copied to the runtime image.

## Platform Requirements

**Development:**
- Go 1.25.6+
- Kubeconfig files in a directory for multi-cluster access
- No linter, CI, or Makefile configured

**Production:**
- Kubernetes cluster (runs as a Deployment with ServiceAccount)
- Read-only ClusterRole for Deployments (`get`, `list`, `watch`)
- CRD installation for persistence features (`deploy/crds.yaml`)
- Resource limits: 200m CPU, 128Mi memory (per `deploy/deployment.yaml`)

## Known Issue

`go.mod` requires Go 1.25.6. The `Dockerfile` now references `golang:1.25-alpine` (updated from earlier `golang:1.22-alpine`). Verify the Docker image tag resolves correctly for the Go toolchain version.

---

*Stack analysis: 2026-07-26*
