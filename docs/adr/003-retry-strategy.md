# ADR 003: Retry Strategy

## Status
Accepted

## Context
Webhook deliveries fail for various reasons:
- Temporary network issues
- Destination server overload
- Transient errors (503, 429)

We need a retry strategy that:
- Gives destinations time to recover
- Doesn't overwhelm failing endpoints
- Eventually gives up on permanent failures
- Distributes retry load over time

## Decision
Use **exponential backoff with jitter**, maximum 5 attempts.

Formula:
```
delay = min(InitialInterval * (Multiplier ^ (attempt - 1)) + jitter, MaxInterval)
```

Default configuration:
- InitialInterval: 1 second
- Multiplier: 2.0
- Jitter: ±10%
- MaxInterval: 1 hour
- MaxAttempts: 5

## Alternatives Considered

### Fixed Interval
```
delay = constant (e.g., 60 seconds)
```
**Cons:**
- Doesn't adapt to failure patterns
- Can overwhelm recovering servers
- Thundering herd problem

### Linear Backoff
```
delay = InitialInterval * attempt
```
**Cons:**
- Grows too slowly for long outages
- Still causes load spikes

### Exponential Without Jitter
```
delay = InitialInterval * (Multiplier ^ attempt)
```
**Cons:**
- Thundering herd: all retries at same time
- Synchronized load spikes

### Fibonacci Backoff
```
delay = fib(attempt) * InitialInterval
```
**Cons:**
- More complex, no clear benefit
- Less predictable

## Rationale

### 1. Exponential Growth
Delays grow quickly, giving servers time to recover:
```
Attempt 1: ~1s
Attempt 2: ~2s
Attempt 3: ~4s
Attempt 4: ~8s
Attempt 5: ~16s
```

### 2. Jitter Prevents Thundering Herd
Random variation spreads retry load:
```go
jitterRange := delay * p.Jitter
jitterOffset := (rand.Float64()*2 - 1) * jitterRange
delay += jitterOffset
```

Without jitter, if 1000 events fail at t=0:
- All retry at t=1s, t=3s, t=7s... (synchronized spikes)

With 10% jitter:
- Retries spread across t=0.9s-1.1s, t=2.7s-3.3s... (distributed load)

### 3. Maximum 5 Attempts
After 5 attempts (~31 seconds total), event is marked failed:
- Prevents infinite retry loops
- Allows human intervention for persistent failures
- Configurable per use case

### 4. MaxInterval Cap
Prevents delays from growing unbounded:
- Default 1 hour max
- Important for very high attempt counts (if configured)

## Implementation

```go
func (p Policy) CalculateDelay(attempt int) time.Duration {
    delay := float64(p.InitialInterval) * math.Pow(p.Multiplier, float64(attempt-1))
    
    if delay > float64(p.MaxInterval) {
        delay = float64(p.MaxInterval)
    }
    
    if p.Jitter > 0 {
        jitterRange := delay * p.Jitter
        jitterOffset := (rand.Float64()*2 - 1) * jitterRange
        delay += jitterOffset
    }
    
    return time.Duration(delay)
}
```

## Intelligent Retry Classification

Not all failures should be retried. The system classifies HTTP status codes:

### Permanent Failures (No Retry)

These errors indicate issues that won't resolve with retries:

| Status | Description | Reason |
|--------|-------------|--------|
| 400 | Bad Request | Payload is invalid |
| 401 | Unauthorized | Credentials invalid |
| 403 | Forbidden | Access denied |
| 404 | Not Found | Endpoint doesn't exist |
| 405 | Method Not Allowed | POST not accepted |
| 406 | Not Acceptable | Content type not accepted |
| 410 | Gone | Resource permanently removed |
| 411 | Length Required | Server config issue |
| 413 | Payload Too Large | Event too big |
| 414 | URI Too Long | URL invalid |
| 415 | Unsupported Media Type | Content type not supported |
| 422 | Unprocessable Entity | Semantically invalid |
| 426 | Upgrade Required | Needs HTTPS |
| 431 | Request Header Fields Too Large | Headers too big |

### Retryable Failures

These errors are temporary and may succeed on retry:

| Status | Description |
|--------|-------------|
| 408 | Request Timeout |
| 429 | Too Many Requests |
| 500 | Internal Server Error |
| 502 | Bad Gateway |
| 503 | Service Unavailable |
| 504 | Gateway Timeout |

### Network Errors

Errors without HTTP status code (DNS failures, connection refused, timeouts) are retried as they may be temporary.

## Consequences

### Positive
- Well-understood, industry-standard approach
- Adapts to failure duration
- Prevents thundering herd
- Simple to implement and reason about
- Intelligent classification avoids wasting retries on permanent failures

### Negative
- May be too aggressive for some endpoints
- Fixed max attempts may not suit all use cases

### Future Considerations
- Per-subscription retry configuration
- Dead letter queue for failed events
