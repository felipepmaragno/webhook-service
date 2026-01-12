# ADR 009: Testing Strategy

## Status
Accepted

## Context
Testing is critical for:
- Confidence in correctness
- Safe refactoring
- Documentation of behavior
- Regression prevention

We need a testing strategy that:
- Is fast for development feedback
- Covers critical paths
- Is maintainable long-term

## Decision
Use **Test-Driven Development (TDD)** with:
- Unit tests with interfaces and mocks
- Integration tests with testcontainers (planned)
- Table-driven tests for comprehensive coverage

## Testing Pyramid

```
        /\
       /  \      E2E (manual/smoke)
      /----\
     /      \    Integration (testcontainers)
    /--------\
   /          \  Unit (mocks, fast)
  /------------\
```

Focus on unit tests (fast, many), fewer integration tests (slower, critical paths).

## Alternatives Considered

### No Mocks (Real Dependencies)
**Cons:**
- Slow tests
- Flaky (network, database state)
- Hard to test edge cases

### Heavy Mocking Frameworks
**Cons:**
- Complex setup
- Brittle tests
- Hide interface design issues

### Only Integration Tests
**Cons:**
- Slow feedback loop
- Hard to test edge cases
- Expensive to run

## Rationale

### 1. Interface-Based Design

All external dependencies are interfaces:
```go
type EventRepository interface {
    Create(ctx context.Context, event *Event) error
    GetByID(ctx context.Context, id string) (*Event, error)
    GetPendingBatch(ctx context.Context, limit int) ([]*Event, error)
    Update(ctx context.Context, event *Event) error
}

type HTTPClient interface {
    Do(req *http.Request) (*http.Response, error)
}
```

Benefits:
- Easy to mock in tests
- Clear contracts
- Enables dependency injection

### 2. Simple Mocks

Hand-written mocks, no framework:
```go
type mockEventRepo struct {
    events      map[string]*domain.Event
    createErr   error
    lastUpdated *domain.Event
}

func (m *mockEventRepo) Create(ctx context.Context, e *domain.Event) error {
    if m.createErr != nil {
        return m.createErr
    }
    m.events[e.ID] = e
    return nil
}
```

Benefits:
- No external dependency
- Full control over behavior
- Easy to understand

### 3. Table-Driven Tests

```go
func TestEvent_CanRetry(t *testing.T) {
    tests := []struct {
        name        string
        attempts    int
        maxAttempts int
        want        bool
    }{
        {"zero attempts", 0, 5, true},
        {"some attempts", 3, 5, true},
        {"at max", 5, 5, false},
        {"over max", 6, 5, false},
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            e := &Event{Attempts: tt.attempts, MaxAttempts: tt.maxAttempts}
            if got := e.CanRetry(); got != tt.want {
                t.Errorf("CanRetry() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

Benefits:
- Comprehensive coverage
- Easy to add cases
- Clear documentation of behavior

### 4. Race Detector

Always run tests with race detector:
```bash
go test -race ./...
```

Catches:
- Data races in concurrent code
- Unsafe shared state
- Missing synchronization

### 5. Integration Tests (Planned)

Using testcontainers for real PostgreSQL:
```go
func TestIntegration_EventLifecycle(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }
    
    ctx := context.Background()
    pgContainer, err := postgres.RunContainer(ctx)
    // ... test with real database
}
```

Benefits:
- Tests real SQL queries
- Catches PostgreSQL-specific issues
- Runs in CI

## Test Organization

```
internal/
├── domain/
│   ├── event.go
│   └── event_test.go      # Unit tests
├── worker/
│   ├── worker.go
│   └── worker_test.go     # Unit tests with mocks
├── repository/
│   └── postgres/
│       ├── event.go
│       └── event_integration_test.go  # testcontainers
```

## Running Tests

```bash
# Unit tests (fast)
go test -short ./...

# All tests including integration
go test ./...

# With race detector
go test -race ./...

# With coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## Consequences

### Positive
- Fast feedback loop
- High confidence in changes
- Documentation via tests
- Safe refactoring

### Negative
- Mocks can drift from real implementation
- Integration tests are slower
- Test maintenance overhead

### Coverage Goals
- Unit tests: >80% coverage
- Critical paths: 100% coverage
- Integration tests: happy path + key error cases

### Future Considerations
- Mutation testing
- Fuzz testing for parsers
- Load testing (see separate doc)
- Contract testing for API
