# Coding Conventions

**Analysis Date:** 2026-07-23

## Naming Patterns

**Files:**
- Lowercase with underscores: `hash_store.go`, `namespace_filter.go`, `slack_bot.go`
- Test files: `*_test.go` co-located with source (e.g., `internal/watcher/informer_test.go`)
- Generated files: `zz_generated_deepcopy.go` (kubebuilder convention)
- Testdata: embedded YAML fixtures in `internal/diagnostic/testdata/data.go` using `//go:embed`

**Packages:**
- Short, single-word lowercase: `watcher`, `dispatch`, `config`, `models`, `persistence`, `diagnostic`, `investigation`, `trace`
- Package comments on the primary file: `// Package watcher detects Kubernetes Deployment rollouts...`

**Types:**
- PascalCase structs: `ClusterWatcher`, `HashStore`, `RolloutEvent`, `NamespaceFilter`
- Interfaces are named by behavior, not "I" prefix: `Target`, `HashObserver`, `Investigator`
- Type aliases for enum-like constants: `type DispatchMode string`, `type RolloutPhase string`

**Functions/Methods:**
- camelCase unexported: `templateHash()`, `stripUnneededFields()`, `applyDefaults()`
- PascalCase exported: `NewClusterWatcher()`, `LoadClusters()`, `BufferHash()`
- Constructor pattern: `New<Type>(deps...) *Type` (e.g., `NewHashStore(c, namespace)`)
- Boolean methods: `Allowed()`, `NamespaceAllowed()`

**Variables:**
- camelCase locals: `eventCh`, `nsFilter`, `deployKey`
- Composite keys use `/` separator: `clusterID + "/" + namespace + "/" + name`

**Constants:**
- PascalCase exported: `DispatchLog`, `PhaseDetected`, `DefaultConfigPath`
- Grouped in `const()` blocks with doc comments per constant

## Code Style

**Formatting:**
- Standard `gofmt` — no custom formatter configured
- No `.editorconfig`, `.prettierrc`, or similar

**Linting:**
- No linter configured (noted in CLAUDE.md: "No linter or CI config exists in the repo yet")
- Code follows standard Go conventions without enforcement

**Line Length:**
- No enforced limit; lines generally stay under ~120 chars

## Import Organization

**Order (standard Go convention):**
1. Standard library (`context`, `fmt`, `log/slog`, `sync`)
2. External dependencies (`k8s.io/...`, `sigs.k8s.io/...`)
3. Internal packages (`github.com/koolhandluke/k8s-deploy-monitor-operator/internal/...`)

**Blank line separation** between each group. Example from `internal/watcher/informer.go`:
```go
import (
    "context"
    "crypto/sha256"
    "fmt"

    appsv1 "k8s.io/api/apps/v1"
    "k8s.io/client-go/informers"
    "k8s.io/client-go/kubernetes"

    "github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)
```

**Path Aliases:**
- Kubernetes type packages use short aliases: `appsv1`, `corev1`, `metav1`
- Conflict resolution aliases: `apierrors "k8s.io/apimachinery/pkg/api/errors"`, `utilruntime "k8s.io/apimachinery/pkg/util/runtime"`
- CRD package alias: `v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"`

## Error Handling

**Patterns:**
- Wrap errors with `fmt.Errorf("context: %w", err)` for all returned errors
- Use lowercase error messages starting with a gerund: `"marshalling slack message: %w"`, `"creating controller-runtime client: %w"`
- Fatal errors in `main()` use `slog.Error()` + `os.Exit(1)` — never `log.Fatal()`
- Non-fatal errors log and continue: `slog.Warn("failed to load persisted hashes", "cluster", id, "error", err)`
- Kubernetes API not-found errors checked with `errors.IsNotFound(err)` (from `k8s.io/apimachinery/pkg/api/errors`)
- Auth errors checked with `apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err)` for permanent failure classification

**Error propagation style:**
```go
if err := s.client.Create(ctx, state); err != nil {
    return fmt.Errorf("creating cluster state: %w", err)
}
```

**Silently defaulting malformed config:**
- `envInt()` returns `defaultVal` on parse error — no logging, no error
- This is intentional per CLAUDE.md: "Malformed ints silently fall back to the default"

## Logging

**Framework:** `log/slog` with JSON handler (stdlib, no third-party logging library)

**Setup** in `cmd/monitor/main.go`:
```go
slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))
```

**Log Levels:**
- `slog.Info` — operational events: startup, watcher started/stopped, rollout detected
- `slog.Warn` — degraded state: watch errors, failed hash loads, unhealthy watchers
- `slog.Error` — failures requiring attention: dispatch failures, persistence failures
- `slog.Debug` — cache operations, skipped events, template hash details
- Custom `trace.LevelTrace` — deep investigation pipeline detail (enabled via `TRACE=true`)

**Structured key-value pairs** (always use named keys, not positional):
```go
slog.Info("rollout detected",
    "cluster", w.clusterID,
    "app", event.App,
    "deployment", newDeploy.Namespace+"/"+newDeploy.Name,
)
```

**Log message conventions:**
- Lowercase, snake_case for event identifiers: `"watcher_started"`, `"watcher_queued_retry"`, `"zero_active_watchers"`
- Present tense for state: `"starting rollout monitor"`, `"cluster watcher started"`
- Past tense for completed actions: `"dispatched to slack"`, `"loaded persisted hashes"`

## Comments

**When to Comment:**
- Package-level doc comment on the primary file of each package
- All exported types, functions, and methods get doc comments
- Unexported helpers get doc comments when the purpose is non-obvious
- Step-by-step numbered comments for multi-phase operations (see `Start()` in `internal/watcher/informer.go`)

**Doc comment style:**
```go
// NewClusterWatcher creates a watcher for a single cluster that detects
// deployment rollouts by tracking spec.template hash changes.
func NewClusterWatcher(...) *ClusterWatcher {
```

**Inline comments:**
- Used sparingly for non-obvious logic: `// Empty string signals deletion`
- Phase markers: `// Phase 1: Rescan directory for file changes`
- Test clarifications: `// expected — no event`, `// expected — filtered out`

## Function Design

**Size:** Functions are generally 20-60 lines. Longest methods (e.g., `reconcile`, `main`) break work into labeled phases.

**Parameters:**
- Use concrete types for dependencies, interfaces only where needed for testing (`kubernetes.Interface`, `HashObserver`)
- Functional options pattern for callbacks: `SetEventEnricher(func(*models.RolloutEvent))`, `SetOnDispatched(func(...))`
- `context.Context` is always the first parameter

**Return Values:**
- Single error return for fallible operations: `func (w *ClusterWatcher) Start(ctx context.Context) error`
- Multiple returns for status checks: `func HealthStatus() (healthy bool, permanentErr bool, lastErr error)`
- Named returns only in `DispatchEvent` for clarity: `(targets []string, dispatchErr string)`

## Module Design

**Exports:**
- Only export what other packages need; internal helpers are unexported
- Constructor + method pattern: `New<Type>()` creates, methods operate
- Each package has a clear entry point type (e.g., `Manager` in watcher, `Dispatcher` in dispatch)

**Barrel Files:**
- Not used — Go does not use barrel/index files

**Interface definition:**
- Interfaces defined in the consumer package, not the provider (e.g., `Target` in `dispatch`, `HashObserver` in `watcher`)
- Small interfaces (1-2 methods): `Target{Dispatch(), Name()}`, `HashObserver{OnHashUpdate(), OnHashDelete()}`

## Concurrency Patterns

**Goroutine lifecycle:**
- Always use `context.Context` for cancellation
- `sync.WaitGroup` for goroutine draining on shutdown
- `sync.Mutex` for shared state (maps), `sync.RWMutex` for read-heavy state (`NamespaceFilter`)
- `sync/atomic` for counters and flags (`consecutiveErrors`, `permanent`)
- `atomic.Value` for storing typed values (`lastSuccessTime`, `lastWatchError`)

**Channel patterns:**
- Buffered channels for event pipelines: `make(chan models.RolloutEvent, cfg.QueueMaxSize)`
- Non-blocking sends with drop semantics (debouncer)
- Channel close for shutdown signaling

**Graceful shutdown sequence** (from `main.go`):
1. Signal received → `manager.Stop()` → `close(eventCh)` → `dispatcher.Wait()` → `cancel()`

## CRD Type Conventions

**Location:** `api/v1alpha1/`

**Markers:** kubebuilder markers for validation, print columns, and scope:
```go
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=crs
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterID`
```

**Structure:** Standard Kubernetes Spec/Status split:
- `Spec` holds desired/declarative state
- `Status` holds observed state with timestamps

---

*Convention analysis: 2026-07-23*
