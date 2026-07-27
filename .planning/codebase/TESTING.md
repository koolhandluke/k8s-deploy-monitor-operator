# Testing Patterns

**Analysis Date:** 2026-07-26

## Test Framework

**Runner:**
- Go standard `testing` package (no third-party test framework)
- No test runner configuration file — uses `go test` directly

**Assertion Library:**
- Standard library only — manual `if` checks with `t.Error`/`t.Fatal`
- No testify, gomega, or other assertion libraries

**Run Commands:**
```bash
go test ./...                                                    # Run all tests
go test ./internal/watcher/ -run TestClusterWatcher_DetectsRollout -v   # Single test
go test ./... -race                                              # Race detector (recommended for this codebase)
go test ./... -count=1                                           # No cache
```

## Test File Organization

**Location:**
- Co-located with source files in the same package (same directory)
- Tests use the same package name (not `_test` suffix package), enabling access to unexported symbols

**Naming:**
- Test files: `<source_file>_test.go` (e.g., `informer_test.go`, `slack_test.go`, `manager_test.go`)
- Exception: `fixtures_test.go` and `scenarios_test.go` in `internal/diagnostic/` for shared test helpers

**Structure:**
```
internal/
├── config/
│   ├── config.go
│   ├── config_test.go
│   ├── kubeconfig.go
│   ├── kubeconfig_test.go
│   ├── env_config.go
│   └── env_config_test.go
├── watcher/
│   ├── informer.go
│   ├── informer_test.go
│   ├── debouncer.go
│   ├── debouncer_test.go
│   ├── manager.go
│   └── manager_test.go
├── dispatch/
│   ├── slack.go
│   ├── slack_test.go
│   ├── slack_bot.go
│   ├── slack_bot_test.go
│   ├── record_watcher.go
│   ├── record_watcher_test.go
│   ├── ttl_cleaner.go
│   └── ttl_cleaner_test.go
├── watchlist/
│   ├── store.go
│   ├── store_test.go
│   ├── handler.go
│   └── handler_test.go
├── diagnostic/
│   ├── analyzer.go
│   ├── analyzer_test.go
│   ├── diagnostics_test.go
│   ├── fixtures_test.go
│   ├── monitor_test.go
│   ├── scenarios_test.go
│   ├── soak_test.go
│   └── testdata/
│       ├── data.go              # Embedded YAML fixtures via //go:embed
│       ├── deployment-stable.yaml
│       ├── pod-crashloop.yaml
│       └── ... (14 YAML fixtures)
└── investigation/
    ├── orchestrator.go
    ├── orchestrator_test.go
    ├── slack_reporter.go
    └── slack_reporter_test.go
```

## Test Structure

**Suite Organization:**
- One `Test<Function>_<Scenario>` per test function — no subtests by default
- Table-driven tests used for parameterized cases with `t.Run()`
- No `TestMain` setup/teardown

**Standard test pattern:**
```go
func TestClusterWatcher_DetectsRollout(t *testing.T) {
    // 1. Setup: create context, fake clients, channels
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    deploy := makeDeployment("myapp", "default", "myapp:v1")
    clientset := fake.NewSimpleClientset(deploy)

    eventCh := make(chan models.RolloutEvent, 10)
    debouncer := NewDebouncer(100*time.Millisecond, eventCh)
    defer debouncer.Stop()

    // 2. Create and start the component under test
    w := NewClusterWatcher("test-cluster", clientset, debouncer,
        func(string) bool { return true }, nil, 30*time.Second)
    if err := w.Start(ctx); err != nil {
        t.Fatalf("failed to start watcher: %v", err)
    }
    defer w.Stop()

    // 3. Wait for initialization
    time.Sleep(200 * time.Millisecond)

    // 4. Perform the action
    deploy.Spec.Template.Spec.Containers[0].Image = "myapp:v2"
    _, err := clientset.AppsV1().Deployments("default").Update(ctx, deploy, metav1.UpdateOptions{})
    if err != nil {
        t.Fatalf("failed to update deployment: %v", err)
    }

    // 5. Assert results via channel/state
    select {
    case event := <-eventCh:
        if event.DeploymentName != "myapp" {
            t.Errorf("expected myapp, got %s", event.DeploymentName)
        }
    case <-time.After(5 * time.Second):
        t.Fatal("timed out waiting for rollout event")
    }
}
```

**Table-driven test pattern:**
```go
func TestRetryBackoff(t *testing.T) {
    tests := []struct {
        attempt  int
        expected time.Duration
    }{
        {0, 10 * time.Second},
        {1, 20 * time.Second},
        {5, 5 * time.Minute}, // capped
    }

    for _, tt := range tests {
        got := retryBackoff(tt.attempt)
        if got != tt.expected {
            t.Errorf("retryBackoff(%d) = %v, want %v", tt.attempt, got, tt.expected)
        }
    }
}
```

**Table-driven with subtests:**
```go
func TestWaitingReason(t *testing.T) {
    tests := []struct {
        name   string
        cs     corev1.ContainerStatus
        expect string
    }{
        {name: "waiting reason", cs: ..., expect: "CrashLoopBackOff"},
        {name: "no reason", cs: corev1.ContainerStatus{}, expect: ""},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := waitingReason(tt.cs)
            if got != tt.expect {
                t.Errorf("expected %q, got %q", tt.expect, got)
            }
        })
    }
}
```

## Assertion Patterns

**Use `t.Fatalf` for setup failures and preconditions that must pass:**
```go
if err := w.Start(ctx); err != nil {
    t.Fatalf("failed to start watcher: %v", err)
}
```

**Use `t.Errorf` for test assertions that should continue:**
```go
if event.DeploymentName != "myapp" {
    t.Errorf("expected myapp, got %s", event.DeploymentName)
}
```

**Use `t.Fatal` for terminal failures:**
```go
case <-time.After(5 * time.Second):
    t.Fatal("timed out waiting for rollout event")
```

**Negative assertions (expect no event):**
```go
select {
case event := <-eventCh:
    t.Fatalf("unexpected rollout event for status-only update: %+v", event)
case <-time.After(500 * time.Millisecond):
    // expected -- no event
}
```

## Mocking

**Framework:** No mocking framework. All test doubles are hand-written.

**Kubernetes fake clients:**
- `k8s.io/client-go/kubernetes/fake` for core Kubernetes API: `fake.NewSimpleClientset(objects...)`
- `sigs.k8s.io/controller-runtime/pkg/client/fake` for CRD operations: `fake.NewClientBuilder().WithScheme(s).WithObjects(obj).Build()`

**HTTP mocking:**
- `net/http/httptest` for HTTP targets: `httptest.NewServer(http.HandlerFunc(...))`
- Test servers capture requests for assertion

**Pattern — fake Kubernetes client:**
```go
deploy := makeDeployment("myapp", "default", "myapp:v1")
clientset := fake.NewSimpleClientset(deploy)
```

**Pattern — httptest for HTTP targets:**
```go
var receivedMsg slackMessage
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    json.NewDecoder(r.Body).Decode(&receivedMsg)
    w.WriteHeader(http.StatusOK)
}))
defer server.Close()

target := NewSlackTarget(server.URL, server.Client())
```

**Pattern — hand-written test doubles:**
```go
type fakeInvestigator struct {
    delay  time.Duration
    result diagnostic.Result
    calls  atomic.Int32
}

func (f *fakeInvestigator) Investigate(ctx context.Context, event models.RolloutEvent) (*diagnostic.DiagnosticReport, error) {
    f.calls.Add(1)
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-time.After(f.delay):
    }
    return &diagnostic.DiagnosticReport{Result: f.result}, nil
}
```

**Pattern — factory function injection:**
```go
// Production code accepts a factory function
m.clientsetFactory = func(cfg *rest.Config) (kubernetes.Interface, error) { ... }

// Test overrides with fake
m.clientsetFactory = func(_ *rest.Config) (kubernetes.Interface, error) {
    return fake.NewSimpleClientset(), nil
}
```

**What to mock:**
- Kubernetes API server (use `fake.NewSimpleClientset`)
- External HTTP services (use `httptest.NewServer`)
- Time-dependent components (use short durations: `50ms`, `100ms` debounce windows)
- Factory functions for dependency injection

**What NOT to mock:**
- Internal components (Debouncer, NamespaceFilter) — test with real instances
- The component under test itself
- Channels — use real buffered channels

## Fixtures and Factories

**Test data — embedded YAML fixtures:**
```go
// internal/diagnostic/testdata/data.go
//go:embed deployment-stable.yaml
var DeploymentStable string

//go:embed pod-crashloop.yaml
var PodCrashloop string
```

**Fixture location:** `internal/diagnostic/testdata/` directory with 14 YAML files covering:
- Deployment states: stable, progressing, deadline-exceeded, paused, generation-mismatch
- Pod states: ready, crashloop, image-pull-backoff, OOM-killed, config-error, init-crash, pending
- ReplicaSet and Event fixtures

**YAML parsing helpers** (from `internal/diagnostic/fixtures_test.go`):
```go
func yamlToDeploy(t *testing.T, y string) *appsv1.Deployment {
    t.Helper()
    var d appsv1.Deployment
    if err := yaml.Unmarshal([]byte(y), &d); err != nil {
        t.Fatalf("unmarshal deployment: %v", err)
    }
    return &d
}
```

**Factory functions for Kubernetes objects:**
```go
func makeDeployment(name, namespace, image string) *appsv1.Deployment {
    return &appsv1.Deployment{
        ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
        Spec: appsv1.DeploymentSpec{
            Selector: &metav1.LabelSelector{
                MatchLabels: map[string]string{"app": name},
            },
            Template: corev1.PodTemplateSpec{
                ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
                Spec: corev1.PodSpec{
                    Containers: []corev1.Container{{Name: name, Image: image}},
                },
            },
        },
    }
}
```

**Event factory:**
```go
func testEvent() models.RolloutEvent {
    return models.RolloutEvent{
        ClusterID:      "test-cluster",
        Namespace:      "default",
        DeploymentName: "myapp",
        Timestamp:      time.Now(),
    }
}
```

**Test helper for manager setup:**
```go
func newTestManager(t *testing.T, dir string) (*Manager, chan models.RolloutEvent) {
    t.Helper()
    eventCh := make(chan models.RolloutEvent, 100)
    m := NewManager(
        func(string) bool { return true },
        100*time.Millisecond,
        eventCh,
        nil,
        dir,
        10*time.Minute,
        30*time.Second,
    )
    m.clientsetFactory = fakeClientsetFactory
    return m, eventCh
}
```

**Kubeconfig fixture:**
```go
func minimalKubeconfig(server string) []byte {
    return []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
  name: test
...`, server))
}
```

## Coverage

**Requirements:** No coverage targets enforced. No coverage configuration exists.

**View Coverage:**
```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out    # HTML report
go tool cover -func=coverage.out    # Summary
```

## Test Types

**Unit Tests:**
- Primary test type — all tests are unit tests
- Test individual functions and methods in isolation
- Use fake Kubernetes clients, never real clusters
- Co-located with source in same package (white-box testing)

**Integration Tests:**
- No integration tests exist
- No `envtest` (controller-runtime test environment) setup
- No live cluster tests

**E2E Tests:**
- Not used
- No e2e test framework configured

## Common Patterns

**Async Testing with Channels:**
```go
select {
case event := <-eventCh:
    // Assert on event
    if event.DeploymentName != "myapp" {
        t.Errorf("expected myapp, got %s", event.DeploymentName)
    }
case <-time.After(5 * time.Second):
    t.Fatal("timed out waiting for rollout event")
}
```

**Polling for async state:**
```go
deadline := time.After(3 * time.Second)
for {
    m.mu.Lock()
    count := len(m.watchers)
    m.mu.Unlock()
    if count > 0 {
        break
    }
    select {
    case <-deadline:
        t.Fatal("timed out waiting for watcher")
    case <-time.After(50 * time.Millisecond):
    }
}
```

**Error Testing:**
```go
_, err := Load()
if err == nil {
    t.Fatal("expected error when HOLMES_API_URL is missing")
}
```

**Testing HTTP handlers:**
```go
req := httptest.NewRequest(http.MethodGet, "/api/v1/watchlist", nil)
w := httptest.NewRecorder()
handler.ServeHTTP(w, req)

if w.Code != http.StatusOK {
    t.Fatalf("expected 200, got %d", w.Code)
}

var apps []AppSummary
json.NewDecoder(w.Body).Decode(&apps)
```

**Testing goroutine lifecycle (Stop drains in-flight work):**
```go
done := make(chan struct{})
go func() {
    target.Stop()
    close(done)
}()

select {
case <-done:
    // good -- Stop returned
case <-time.After(5 * time.Second):
    t.Fatal("Stop() did not return within timeout -- goroutine leak")
}
```

**Environment variable setup/teardown:**
```go
os.Setenv("DISPATCH_MODE", "holmes")
os.Unsetenv("HOLMES_API_URL")
defer os.Unsetenv("DISPATCH_MODE")
```

**Using `t.TempDir()` for filesystem tests:**
```go
dir := t.TempDir()
os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
    minimalKubeconfig("https://a:6443"), 0644)
```

**Using `t.Helper()` in test helpers:**
- All factory functions and test setup helpers call `t.Helper()` so failure messages point to the calling test, not the helper

**Controller-runtime fake client with scheme:**
```go
func newTestScheme() *runtime.Scheme {
    s := runtime.NewScheme()
    v1alpha1.AddToScheme(s)
    return s
}

c := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(existing).Build()
```

## Time-Dependent Tests

- Use short durations for debounce/timeout in tests: `50ms`, `100ms`, `200ms`
- Use `time.Sleep` for settling after async operations (e.g., cache sync, goroutine startup)
- Use `time.After` with generous timeouts (1-5s) for channel receives to avoid flaky failures
- Tests with timing dependencies accept some margin (e.g., `500ms` wait for negative assertions)

## Test Naming Convention

**Format:** `Test<Component>_<Behavior>` or `Test<Function>_<Scenario>`

Examples:
- `TestClusterWatcher_DetectsRollout`
- `TestClusterWatcher_IgnoresStatusUpdates`
- `TestDebouncer_ReplacesEvent`
- `TestHandler_PUT_Created`
- `TestHandler_PUT_BadJSON`
- `TestOrchestrator_SupersedeCancellation`
- `TestLoad_Defaults`
- `TestLoad_HolmesRequiresURL`

---

*Testing analysis: 2026-07-26*
