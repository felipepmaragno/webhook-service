# ADR 005: Circuit Breaker

## Status
Accepted

## Context
When a destination is down or failing:
- Continued requests waste resources
- Can worsen the destination's problems
- Delays delivery to healthy destinations

We need to detect failing destinations and stop sending requests temporarily.

## Decision
Use **circuit breaker pattern** via `github.com/sony/gobreaker`, per subscription.

Key behavior: **Open circuit does NOT consume retry attempts.**

Default configuration:
- MaxRequests (half-open): 5
- Interval (closed state reset): 60 seconds
- Timeout (open → half-open): 30 seconds
- FailureRatio: 50%
- MinRequests: 3

## Alternatives Considered

### No Circuit Breaker
**Cons:**
- Wasted resources on failing destinations
- Slower recovery (queue backs up)
- Can contribute to cascading failures

### hystrix-go (Netflix Hystrix port)
**Pros:**
- Feature-rich
- Well-known pattern

**Cons:**
- More complex configuration
- Heavier dependency
- Netflix deprecated original Hystrix

### Custom Implementation
**Cons:**
- Error-prone
- Reinventing the wheel
- Testing burden

## Rationale

### 1. Circuit Breaker States

```
[Closed] ──(failures exceed threshold)──► [Open]
    ▲                                        │
    │                                        │
    │                                   (timeout)
    │                                        │
    │                                        ▼
    └────────(success)──────────────── [Half-Open]
                                            │
                                       (failure)
                                            │
                                            ▼
                                         [Open]
```

- **Closed**: Normal operation, requests pass through
- **Open**: Destination failing, requests rejected immediately
- **Half-Open**: Testing recovery, limited requests allowed

### 2. sony/gobreaker
Battle-tested library from Sony:
- Clean, simple API
- Configurable thresholds
- State change callbacks (for metrics)
- Well-maintained

```go
cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
    Name:        subscriptionID,
    MaxRequests: 5,
    Interval:    60 * time.Second,
    Timeout:     30 * time.Second,
    ReadyToTrip: func(counts gobreaker.Counts) bool {
        ratio := float64(counts.TotalFailures) / float64(counts.Requests)
        return counts.Requests >= 3 && ratio >= 0.5
    },
})
```

### 3. Open Circuit ≠ Failed Attempt
Critical decision: when circuit is open, we reschedule WITHOUT incrementing attempts.

Why:
- The event didn't actually fail—we didn't try
- Preserves retry budget for when destination recovers
- Fairer to events that hit a temporarily bad destination

```go
if errors.Is(err, gobreaker.ErrOpenState) {
    event.RescheduleWithoutAttemptIncrement(nextAttempt)
    return
}
```

### 4. Per-Subscription Isolation
Each subscription has independent circuit breaker:
- One failing destination doesn't affect others
- Clean failure isolation
- Independent recovery

### 5. 5xx Responses as Failures
HTTP 5xx responses are counted as circuit breaker failures:

```go
result, err := cb.Execute(func() (interface{}, error) {
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    if resp.StatusCode >= 500 {
        return resp, fmt.Errorf("server error: %d", resp.StatusCode)
    }
    return resp, nil
})
```

## Metrics Integration

```go
cb.OnStateChange(func(name string, from, to gobreaker.State) {
    metrics.CircuitBreakerState.WithLabelValues(name).Set(stateToFloat(to))
    if to == gobreaker.StateOpen {
        metrics.CircuitBreakerTrips.WithLabelValues(name).Inc()
    }
})
```

## Consequences

### Positive
- Fast failure for known-bad destinations
- Gives destinations time to recover
- Preserves retry budget
- Clear observability via metrics

### Negative
- In-memory state (lost on restart)
- No coordination between instances
- May delay delivery during recovery testing

### Future Considerations
- Distributed circuit breaker state
- Per-subscription configuration
- Adaptive thresholds based on destination behavior
