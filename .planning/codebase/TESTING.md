# Testing Patterns

**Analysis Date:** 2026-07-23

## Test Framework

**Runner:**
- Go stdlib `testing` package — no third-party test framework
- No test config file (no `jest.config`, `vitest.config`, etc.)

**Assertion Library:**
- Stdlib only — manual `if` checks with `t.Error()`, `t.Errorf()`, `t.Fatal()`, `t.Fatalf()`
- No testify, gomega, or other assertion libraries

**Run Commands:**
```bash
go test ./...                              # Run all tests
go test ./internal/watcher/ -run TestClusterWatcher_DetectsRollout -v   # Single test
go test ./... -race                        # Race detector (recommended — concurrent maps + timers)
```

## Test File Organization

**Location:**
- Co-located with source files (Go convention)
- Test files in the same package (white-box testing — access to unexported symbols)

**Naming:**
- `<source>_test.go` pattern: `informer_test.go`, `config_test.go`, `slack_test.go`
- Shared test helpers in separate files: `fixtures_test.go` for YAML unmarshal helpers

**Structure:**
```
internal/watcher/
├── informer.go
├── informer_test.go        # Tests for ClusterWatcher
├── manager.go
├── manager_test.go         # Tests for Manager + reconcile loop
├── debouncer.go
├── debouncer_test.go       # Tests for Debouncer

internal/diagnostic/
├── analyzer.go
├── analyzer_test.go        # Unit tests for analyzer functions
├── scenarios_test.go       # Table-driven scenario tests
├── fixtures_test.go        # Shared YAML parse helpers (yamlToDeploy, yamlToPod, etc.)
├── soak_test.go            # Long-running soak/integration test
├── monitor_test.go         # Monitor integration test
├── testdata/
│   ├── data.go             # //go:embed declarations for YAML fixtures
│   ├── deployment-stable.yaml
│   ├── pod-crashloop.yaml
│   └── ... (15+ fixture files)
```

## Test Structure

**Suite Organization:**
```go
func TestComponentName_Behavior(t *testing.T) {
    // Setup
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    clientset := fake.NewSimpleClientset(initialObjects...)
    component := NewComponent(deps...)
    defer component.Stop()

    // Act
    result, err := component.DoSomething(ctx)

    // Assert
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if result != expected {
        t.Errorf("expected %v, got %v", expected, result)
    }
}
```

**Test naming convention:** `Test<Type>_<Behavior>` or `Test<Function>_<Scenario>`:
- `TestClusterWatcher_DetectsRollout`
- `TestClusterWatcher_IgnoresStatusUpdates`
- `TestDebouncer_ReplacesEvent`
- `TestRetryBackoff`
- `TestLoad_HolmesRequiresURL`

**Table-driven tests** for functions with multiple scenarios:
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

**Table-driven with subtests** (using `t.Run`) for complex scenarios:
```go
func TestCheckFailureConditions(t *testing.T) {
    tests := []struct {
        name             string
        deployFixture    string
        pods             []runtime.Object
        expectedResult   Result
        expectedContains string
    }{
        {
            name:          "ProgressDeadlineExceeded",
            deployFixture: testdata.DeploymentDeadlineExceeded,
            ...
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // test body
        })
    }
}
```

## Mocking

**Framework:** No mocking framework. Hand-written test doubles.

**Kubernetes fake clientsets** (`k8s.io/client-go/kubernetes/fake`):
```go
deploy := makeDeployment("myapp", "default", "myapp:v1")
clientset := fake.NewSimpleClientset(deploy)
```

**HTTP test servers** (`net/http/httptest`) for external API targets:
```go
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        t.Errorf("expected POST, got %s", r.Method)
    }
    json.NewDecoder(r.Body).Decode(&receivedMsg)
    w.WriteHeader(http.StatusOK)
}))
defer server.Close()

target := NewSlackTarget(server.URL, server.Client())
```

**Interface-based test doubles** for internal dependencies:
```go
// fakeInvestigator is a test double for Investigator.
type fakeInvestigator struct {
    delay  time.Duration
    result diagnostic.Result
    calls  atomic.Int32
}

func (f *fakeInvestigator) Investigate(ctx context.Context, event models.RolloutEvent) (*diagnostic.DiagnosticReport, error) {
    f.calls.Add(1)
    // simulated delay
    return &diagnostic.DiagnosticReport{Result: f.result}, nil
}
```

**Factory functions** for replaceable production dependencies:
```go
// In production code:
type ClientsetFactory func(*rest.Config) (kubernetes.Interface, error)

// In tests:
func fakeClientsetFactory(_ *rest.Config) (kubernetes.Interface, error) {
    return fake.NewSimpleClientset(), nil
}
m.clientsetFactory = fakeClientsetFactory
```

**What to Mock:**
- Kubernetes API interactions → use `fake.NewSimpleClientset()`
- HTTP external services (Slack, Holmes) → use `httptest.NewServer()`
- Interfaces like `Investigator`, `Reporter` → hand-written structs implementing the interface
- Time-dependent operations → use short durations (50ms-100ms debounce in tests vs 30s production)

**What NOT to Mock:**
- Internal data structures (maps, slices, channels)
- Pure functions (`templateHash`, `extractImages`, `retryBackoff`)
- Configuration loading (tests set env vars directly via `os.Setenv`)

## Fixtures and Factories

**Kubernetes object factories:**
```go
func makeDeployment(name, namespace, image string) *appsv1.Deployment {
    return &appsv1.Deployment{
        ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
        Spec: appsv1.DeploymentSpec{
            Template: corev1.PodTemplateSpec{
                Spec: corev1.PodSpec{
                    Containers: []corev1.Container{{Name: name, Image: image}},
                },
            },
        },
    }
}
```

**YAML embedded fixtures** for complex Kubernetes objects (`internal/diagnostic/testdata/`):
```go
//go:embed deployment-stable.yaml
var DeploymentStable string

// Used in tests via helper:
deploy := yamlToDeploy(t, testdata.DeploymentStable)
```

**YAML parse helpers** in `internal/diagnostic/fixtures_test.go`:
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

**Test helper functions** use `t.Helper()` for correct line reporting:
```go
func newTestManager(t *testing.T, dir string) (*Manager, chan models.RolloutEvent) {
    t.Helper()
    eventCh := make(chan models.RolloutEvent, 100)
    m := NewManager(...)
    m.clientsetFactory = fakeClientsetFactory
    return m, eventCh
}
```

**Location of fixtures:**
- `internal/diagnostic/testdata/*.yaml` — 15+ embedded YAML fixtures for K8s objects
- `internal/diagnostic/testdata/data.go` — `//go:embed` declarations
- `internal/diagnostic/fixtures_test.go` — YAML parse helpers
- Inline factory functions in test files for simple objects (`makeDeployment`, `testEvent`, `minimalKubeconfig`)

## Coverage

**Requirements:** None enforced — no coverage threshold configured

**View Coverage:**
```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## Test Types

**Unit Tests:**
- Pure function tests: `TestTemplateHash_DifferentImages`, `TestRetryBackoff`, `TestFilterErrorLines`
- Struct method tests: `TestNamespaceAllowed_Allowlist`, `TestHealthStatus_InitiallyHealthy`
- These use no external dependencies or goroutines

**Integration-style Tests (in-process):**
- Watcher tests with fake clientsets: `TestClusterWatcher_DetectsRollout` — starts a real informer against a fake API
- Manager reconcile tests: `TestReconcile_AddsNewCluster` — uses temp directories and fake clientset factories
- Orchestrator lifecycle tests: `TestOrchestrator_StopDrains` — verifies goroutine lifecycle

**E2E Tests:**
- Not present. No envtest, no live cluster tests, no container-based test harness
- Tests rely entirely on `fake.NewSimpleClientset()` and `httptest.NewServer()`

## Common Patterns

**Async Testing (channel-based with timeout):**
```go
select {
case event := <-eventCh:
    if event.DeploymentName != "myapp" {
        t.Errorf("expected myapp, got %s", event.DeploymentName)
    }
case <-time.After(5 * time.Second):
    t.Fatal("timed out waiting for rollout event")
}
```

**Negative async testing (expect no event):**
```go
select {
case event := <-eventCh:
    t.Fatalf("unexpected rollout event: %+v", event)
case <-time.After(500 * time.Millisecond):
    // expected — no event
}
```

**Error Testing:**
```go
_, err := Load()
if err == nil {
    t.Fatal("expected error when HOLMES_API_URL is missing")
}
```

**Goroutine leak detection:**
```go
done := make(chan struct{})
go func() {
    target.Stop()
    close(done)
}()

select {
case <-done:
    // good — Stop returned
case <-time.After(5 * time.Second):
    t.Fatal("Stop() did not return within timeout — goroutine leak")
}
```

**Testing with environment variables:**
```go
func TestLoad_HolmesRequiresURL(t *testing.T) {
    os.Setenv("DISPATCH_MODE", "holmes")
    os.Unsetenv("HOLMES_API_URL")
    defer os.Unsetenv("DISPATCH_MODE")

    _, err := Load()
    if err == nil {
        t.Fatal("expected error when HOLMES_API_URL is missing")
    }
}
```

**Testing with temp directories:**
```go
func TestReconcile_AddsNewCluster(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "cluster-a.yaml"),
        minimalKubeconfig("https://a:6443"), 0644)

    m, _ := newTestManager(t, dir)
    // ...
}
```

**Testing internal state via mutex:**
```go
m.mu.Lock()
if len(m.watchers) != 1 {
    t.Fatalf("expected 1 watcher, got %d", len(m.watchers))
}
m.mu.Unlock()
```

**Sleep-based synchronization** (used for informer cache sync):
```go
time.Sleep(200 * time.Millisecond) // Wait for cache sync to complete
```

Note: Tests use short durations (50ms-200ms) for debounce/timeout to keep test execution fast while still exercising timing-dependent code paths.

## Test Caveats

**Fake clientset limitations:**
- `fake.NewSimpleClientset` does not support field selectors — tests note this:
  ```go
  // The fake client doesn't support field selectors, so both Warning events come back.
  // But name prefix filtering should exclude the unrelated one.
  ```

**Environment variable tests are not parallel:**
- Config tests manipulate `os.Setenv`/`os.Unsetenv` — they must run serially
- No `t.Parallel()` usage observed anywhere in the codebase

**No test for `main()`:**
- The `cmd/monitor/main.go` function is not tested; integration is covered by component tests

---

*Testing analysis: 2026-07-23*
