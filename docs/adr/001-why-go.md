# ADR 001: Why Go

## Status
Accepted

## Context
We need to choose a programming language for the webhook dispatcher service. The service must:
- Handle high concurrency (thousands of concurrent webhook deliveries)
- Be reliable and performant
- Be easy to deploy and operate
- Have good ecosystem for HTTP, databases, and observability

## Decision
Use **Go** as the implementation language.

## Alternatives Considered

### Rust
**Pros:**
- Maximum performance, zero-cost abstractions
- Memory safety without GC

**Cons:**
- Steeper learning curve
- Slower development velocity
- Smaller ecosystem for web services
- More complex deployment (though still single binary)

### Node.js/TypeScript
**Pros:**
- Large ecosystem
- Fast prototyping
- Familiar to many developers

**Cons:**
- Single-threaded event loop (worker threads add complexity)
- Higher memory footprint
- Runtime dependency (Node.js installation)
- Less predictable latency due to GC

### Java/Kotlin
**Pros:**
- Mature ecosystem
- Strong typing
- Good concurrency with virtual threads (Java 21+)

**Cons:**
- JVM startup time and memory overhead
- More complex deployment (JVM required)
- Verbose compared to Go

## Rationale

### 1. Native Concurrency Model
Go's goroutines and channels are ideal for webhook delivery:
```go
// Each worker is a lightweight goroutine (~2KB stack)
for i := 0; i < config.Workers; i++ {
    go p.worker(ctx, i)
}
```
- Goroutines are cheap (thousands concurrent with minimal memory)
- No callback hell or async/await complexity
- Built-in race detector for development

### 2. Performance
- Compiled to native code
- Low latency, predictable GC pauses
- Efficient HTTP client/server in stdlib

### 3. Simple Deployment
- Single static binary, no runtime dependencies
- Small Docker images (~20MB with Alpine)
- Cross-compilation built-in

### 4. Ecosystem
- Excellent database drivers (pgx)
- Prometheus client library
- Mature HTTP routing (chi, stdlib)
- Strong testing support

### 5. Operational Simplicity
- Easy profiling (pprof)
- Built-in race detector
- Simple dependency management (go modules)

## Consequences

### Positive
- Fast development with good performance
- Easy to hire Go developers
- Simple CI/CD pipeline
- Low operational overhead

### Negative
- Less expressive than Rust for some patterns
- No generics until Go 1.18 (now available)
- Error handling verbosity

### Risks
- Team must learn Go idioms
- Mitigated by: Go's simplicity, good documentation
