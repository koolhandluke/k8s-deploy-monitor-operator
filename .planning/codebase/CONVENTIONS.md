# Coding Conventions

**Analysis Date:** 2026-07-26

## Naming Patterns

**Files:**
- Use `snake_case.go` for all Go source files (e.g., `hash_store.go`, `namespace_filter.go`, `slack_bot.go`)
- Test files use `_test.go` suffix, co-located with source (e.g., `informer_test.go` alongside `informer.go`)
- CRD type files use descriptive names: `types.go`, `monitor_config.go`, `app_watch_config.go`
- Generated files use `zz_generated_` prefix: `zz_generated_deepcopy.go`

**Functions:**
- Use `camelCase` for unexported functions: `templateHash()`, `stripUnneededFields()`, `envInt()`
- Use `PascalCase` for exported functions: `NewClusterWatcher()`, `LoadClusters()`, `NamespaceAllowed()`
- Constructors follow `New<Type>` pattern: `NewDebouncer()`, `NewDispatcher()`, `NewHashStore()`
- Factory-style constructors return pointer: `func NewSlackTarget(...) *SlackTarget`

**Variables:**
- Use `camelCase` for local variables and struct fields
- Short names for loop variables and receivers: `w` for watcher, `d` for debouncer, `s` for store, `c` for client
- Descriptive names for function parameters: `clusterID`, `webhookURL`, `workerCount`

**Types:**
- Use `PascalCase` for exported types: `ClusterWatcher`, `RolloutEvent`, `DispatchMode`
- String-typed enums use `PascalCase` constants: `DispatchLog`, `DispatchSlack`, `InvestigationRunbook`
- Interfaces are named by behavior, not `I`-prefix: `Target`, `HashObserver`, `Investigator`

**Constants:**
- Enum constants use type prefix: `DispatchLog`, `DispatchHolmes` (not `Log`, `Holmes`)
- Phase constants use `Phase` prefix: `PhaseDetected`, `PhaseDispatched`, `PhaseFailed`

## Code Style

**Formatting:**
- Standard `gofmt` formatting (no custom formatter configured)
- No `.editorconfig`, `.prettierrc`, or equivalent exists
- No linter or CI configuration in the repo

**Linting:**
- No linter configured. No `.golangci.yml` or equivalent exists.
- Code follows standard Go idioms manually

## Package Organization

**Package comments:**
- Every package has a doc comment on the `package` line in one primary file
- Format: `// Package <name> <does what>.` (e.g., `// Package dispatch fans out rollout events to notification targets via a worker pool.`)
- See `internal/dispatch/dispatcher.go`, `internal/persistence/hash_store.go`, `internal/models/event.go`

**Package naming:**
- Short, lowercase, single-word names: `config`, `watcher`, `dispatch`, `models`, `persistence`, `watchlist`
- Exception: `v1alpha1` for CRD API package (follows Kubernetes convention)

## Import Organization

**Order:**
1. Standard library imports (`context`, `fmt`, `log/slog`, `sync`, `time`)
2. External/third-party imports (`k8s.io/...`, `sigs.k8s.io/...`)
3. Internal project imports (`github.com/koolhandluke/k8s-deploy-monitor-operator/...`)

**Grouping:**
- Groups separated by blank lines
- Within groups, imports are alphabetically sorted
- Example from `internal/watcher/informer.go`:
```go
import (
    "context"
    "crypto/sha256"
    "encoding/json"
    "fmt"
    "log/slog"
    "sync"
    "sync/atomic"
    "time"

    appsv1 "k8s.io/api/apps/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/informers"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/tools/cache"

    "github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)
```

**Aliases:**
- Kubernetes API groups use short aliases: `appsv1`, `corev1`, `metav1`
- CRD API uses version alias: `v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"`
- Utility renames for clarity: `utilruntime "k8s.io/apimachinery/pkg/util/runtime"`
- Fake client aliasing: `ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"`

## Error Handling

**Patterns:**
- Return `error` as last return value; check immediately: `if err != nil { return ..., fmt.Errorf(...) }`
- Use `fmt.Errorf` with `%w` for error wrapping: `fmt.Errorf("creating controller-runtime client: %w", err)`
- Error messages are lowercase, describing the failed action: `"slack webhook failed: %w"`, `"loading cluster state for %s: %w"`
- Fatal errors in `main()` use `slog.Error(...)` followed by `os.Exit(1)` -- never `log.Fatal`
- Custom error types for domain-specific cases: `*ConflictError` in `internal/watchlist/store.go`

**Error wrapping convention:**
```go
// Always wrap with context about what was being attempted
return nil, fmt.Errorf("reading config file %s: %w", path, err)
return fmt.Errorf("creating cluster state: %w", err)
```

**Sentinel error checks:**
- Use `k8s.io/apimachinery/pkg/api/errors` for Kubernetes API errors: `errors.IsNotFound(err)`, `apierrors.IsUnauthorized(err)`
- String matching used sparingly for simple cases: `strings.Contains(err.Error(), "not found")` in `internal/watchlist/handler.go`

## Logging

**Framework:** `log/slog` with JSON handler — no external logging library

**Setup pattern** (from `cmd/monitor/main.go`):
```go
slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))
```

**Patterns:**
- Use structured key-value pairs, never string interpolation in log messages
- Log messages are lowercase, action-oriented: `"rollout detected"`, `"dispatch queue full, dropping event"`
- Always include identifying context: `"cluster"`, `"deployment"`, `"error"`
- Use appropriate levels:
  - `slog.Error` for failures requiring attention
  - `slog.Warn` for degraded but recoverable situations (e.g., dropped events, watch reconnects)
  - `slog.Info` for operational state changes (startup, shutdown, rollout detection)
  - `slog.Debug` for internal state (cache seeding, hash comparisons)

**Example:**
```go
slog.Info("rollout detected",
    "cluster", w.clusterID,
    "app", event.App,
    "deployment", newDeploy.Namespace+"/"+newDeploy.Name,
    "images", fmt.Sprintf("%v -> %v", event.OldImages, event.NewImages),
)
```

## Comments

**When to Comment:**
- Package-level doc comment on every package (mandatory)
- Exported types and functions always have doc comments
- Unexported functions have doc comments explaining intent, not mechanics
- Comments use `//` style (no block `/* */` comments)

**Doc comment style:**
- Start with the name of the thing being documented: `// Debouncer coalesces rapid rollout events...`
- Constructor docs describe what is returned: `// NewDebouncer creates a Debouncer that waits for...`
- Method docs describe what happens: `// Submit adds or replaces a pending event...`

**Inline comments:**
- Used for step-by-step processes: `// Step 1: Create factory with transform...`
- Used for non-obvious behavior: `// Empty string signals deletion`
- Used for test intent: `// Should NOT return error because cluster is queued for retry`

## Function Design

**Size:** Functions are typically 10-40 lines. Largest functions are in `cmd/monitor/main.go` (wiring) and reconcile loops.

**Parameters:**
- Accept interfaces where possible: `kubernetes.Interface` not `*kubernetes.Clientset`
- Use `func(string) bool` for simple predicates (namespace filter)
- Accept `*http.Client` as dependency injection for HTTP targets
- Use `context.Context` as first parameter for anything that does I/O

**Return Values:**
- Single error: `func (s *HashStore) LoadHashes(...) (map[string]string, error)`
- Multiple named returns for complex cases: `func (d *Dispatcher) DispatchEvent(...) (targets []string, dispatchErr string)`
- Health status uses multiple returns: `func (w *ClusterWatcher) HealthStatus() (healthy bool, permanentErr bool, lastErr error)`

## Module Design

**Exports:**
- Export types, constructors, and methods that form the public API
- Keep internal helpers unexported: `templateHash()`, `stripUnneededFields()`, `envInt()`
- Use the `internal/` directory to prevent external imports

**Barrel Files:**
- Not used. Each package exposes types directly from their defining files.

## Interface Design

**Pattern:** Define interfaces where they are consumed, not where they are implemented.

- `Target` interface in `internal/dispatch/dispatcher.go` — implemented by `LogTarget`, `SlackTarget`, `AuditTarget`, etc.
- `HashObserver` interface in `internal/watcher/informer.go` — implemented by `HashStore`
- `Investigator` interface in `internal/investigation/orchestrator.go` — implemented by `RunbookInvestigator`, `HolmesInvestigator`

**Minimal interfaces:** Interfaces have 1-2 methods:
```go
type Target interface {
    Dispatch(ctx context.Context, event models.RolloutEvent) error
    Name() string
}
```

## Concurrency Patterns

**Mutex usage:**
- Use `sync.Mutex` for state protection with `Lock()`/`Unlock()` and `defer` for unlock
- Use `sync.RWMutex` for read-heavy data: `NamespaceFilter` uses `RWMutex`
- Use `atomic` operations for counters: `consecutiveErrors`, `permanent` flag

**Channel patterns:**
- Non-blocking sends for backpressure: `select { case ch <- event: default: slog.Warn(...) }`
- Buffered channels for event queues: `make(chan models.RolloutEvent, cfg.QueueMaxSize)`
- Signal channels use `chan struct{}` with buffer 1: `reconcileTriggerCh = make(chan struct{}, 1)`

**Goroutine lifecycle:**
- Use `sync.WaitGroup` for tracking worker goroutines
- Use `context.Context` for cancellation propagation
- Always provide a `Stop()` method that drains in-flight work

## Configuration Pattern

**Layered loading** (from `internal/config/config.go`):
1. Read YAML config file (path from `CONFIG_FILE` env var, default `/etc/rollout-monitor/config.yaml`)
2. Apply env var overrides for secrets (`SLACK_WEBHOOK_URL`, `SLACK_BOT_TOKEN`, `HOLMES_API_URL`)
3. Apply defaults for zero values
4. Validate required dependencies

**Defaults:** Applied in `applyDefaults()` — check for zero value, set default. Malformed int env vars silently fall back to default.

## HTTP Handler Pattern

**Use `http.NewServeMux`** and `HandleFunc` for routing (from `internal/watchlist/handler.go`):
```go
func NewHandler(store *Store, reconcileTrigger chan<- struct{}) http.Handler {
    mux := http.NewServeMux()
    handler := func(w http.ResponseWriter, r *http.Request) {
        // Route based on URL path and method
    }
    mux.HandleFunc("/api/v1/watchlist", handler)
    mux.HandleFunc("/api/v1/watchlist/", handler)
    return mux
}
```

**Response pattern:**
- Set `Content-Type: application/json` before writing
- Use `json.NewEncoder(w).Encode(...)` for JSON responses
- Use `http.Error(w, message, statusCode)` for error responses

---

*Convention analysis: 2026-07-26*
