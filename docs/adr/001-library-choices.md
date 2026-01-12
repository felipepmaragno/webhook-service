# ADR 001: Library Choices

## Status
Accepted

## Context
The dispatch webhook service requires several external libraries for HTTP routing, database access, metrics, and resilience patterns. We need to choose libraries that are:
- Well-maintained and battle-tested
- Idiomatic Go
- Minimal in dependencies
- Easy to test

## Decisions

### HTTP Router: go-chi/chi v5

**Choice:** `github.com/go-chi/chi/v5`

**Alternatives considered:**
- `net/http` (stdlib): Too verbose for routing, no URL parameters
- `gin-gonic/gin`: Heavy, many dependencies, non-standard handler signatures
- `labstack/echo`: Similar to Gin, more opinionated

**Rationale:**
- 100% compatible with `net/http` (handlers, middleware)
- Lightweight with minimal dependencies
- Clean URL parameter extraction via `chi.URLParam()`
- Composable middleware chain
- Active maintenance, widely adopted

### Database: jackc/pgx v5

**Choice:** `github.com/jackc/pgx/v5`

**Alternatives considered:**
- `database/sql` + `lib/pq`: Older driver, less performant
- `jmoiron/sqlx`: Adds reflection overhead
- ORMs (GORM, ent): Too much abstraction for our needs

**Rationale:**
- Native PostgreSQL driver (not database/sql wrapper)
- Connection pooling built-in (`pgxpool`)
- Better performance than lib/pq
- Full PostgreSQL feature support (LISTEN/NOTIFY, COPY, etc.)
- Active development by the community

### Metrics: prometheus/client_golang

**Choice:** `github.com/prometheus/client_golang`

**Alternatives considered:**
- `go-kit/kit/metrics`: More abstraction than needed
- OpenTelemetry: More complex setup for our use case
- Custom metrics: Reinventing the wheel

**Rationale:**
- Official Prometheus client library
- Industry standard for Go services
- Seamless integration with Prometheus/Grafana ecosystem
- `promauto` for automatic registration
- Well-documented metric types (Counter, Gauge, Histogram)

### Rate Limiting: golang.org/x/time/rate

**Choice:** `golang.org/x/time/rate`

**Alternatives considered:**
- `uber-go/ratelimit`: Leaky bucket, less flexible
- `juju/ratelimit`: Less maintained
- Custom implementation: Unnecessary complexity

**Rationale:**
- Official Go extended library (golang.org/x)
- Token bucket algorithm (allows bursts)
- Thread-safe by design
- Simple API: `Allow()`, `Wait()`, `Reserve()`
- Zero external dependencies

### Circuit Breaker: sony/gobreaker

**Choice:** `github.com/sony/gobreaker`

**Alternatives considered:**
- `afex/hystrix-go`: Netflix Hystrix port, more complex
- `rubyist/circuitbreaker`: Less maintained
- Custom implementation: Error-prone

**Rationale:**
- Battle-tested at Sony
- Clean, simple API
- Configurable thresholds and timeouts
- State change callbacks for metrics
- Well-documented state machine

### Logging: log/slog (stdlib)

**Choice:** `log/slog` (Go 1.21+)

**Alternatives considered:**
- `uber-go/zap`: Fast but external dependency
- `rs/zerolog`: Similar to zap
- `sirupsen/logrus`: Older, less performant

**Rationale:**
- Standard library (no external dependency)
- Structured logging with levels
- Context propagation support
- JSON and text handlers built-in
- Future-proof (official Go solution)

## Consequences

### Positive
- Minimal dependency tree
- Easy to understand for Go developers
- Good testability (interfaces, stdlib compatibility)
- Strong community support for all choices

### Negative
- pgx requires learning its specific API (vs database/sql)
- slog is relatively new (Go 1.21+)

### Risks
- Library deprecation (mitigated by choosing well-maintained projects)
- Breaking changes in major versions (mitigated by using go.mod)

## References
- [chi documentation](https://go-chi.io/)
- [pgx documentation](https://github.com/jackc/pgx)
- [Prometheus Go client](https://prometheus.io/docs/guides/go-application/)
- [gobreaker](https://github.com/sony/gobreaker)
- [slog proposal](https://go.dev/blog/slog)
