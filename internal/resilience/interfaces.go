// Package resilience provides rate limiting and circuit breaker implementations
// for protecting destination endpoints from overload.
package resilience

import (
	"context"
	"time"
)

// RateLimiter defines the interface for rate limiting implementations.
// This allows swapping between in-memory and Redis-backed implementations.
type RateLimiter interface {
	// Allow checks if a request is allowed for the given subscription.
	// Returns true if allowed, false if rate limited.
	Allow(ctx context.Context, subscriptionID string, limit int) (bool, error)
}

// CircuitBreaker defines the interface for circuit breaker implementations.
// This allows swapping between in-memory and Redis-backed implementations.
type CircuitBreaker interface {
	// Allow checks if a request should be allowed through the circuit breaker.
	Allow(ctx context.Context, subscriptionID string) (bool, error)
	// RecordSuccess records a successful request.
	RecordSuccess(ctx context.Context, subscriptionID string) error
	// RecordFailure records a failed request.
	RecordFailure(ctx context.Context, subscriptionID string) error
	// State returns the current state of the circuit breaker.
	State(ctx context.Context, subscriptionID string) (CircuitState, error)
}

// InMemoryRateLimiterAdapter adapts RateLimiterManager to the RateLimiter interface.
type InMemoryRateLimiterAdapter struct {
	manager *RateLimiterManager
}

// NewInMemoryRateLimiterAdapter creates a new adapter for in-memory rate limiting.
func NewInMemoryRateLimiterAdapter(config RateLimiterConfig) *InMemoryRateLimiterAdapter {
	return &InMemoryRateLimiterAdapter{
		manager: NewRateLimiterManager(config),
	}
}

// Allow implements RateLimiter interface.
func (a *InMemoryRateLimiterAdapter) Allow(ctx context.Context, subscriptionID string, limit int) (bool, error) {
	a.manager.SetRateIfNotExists(subscriptionID, float64(limit), limit/10+1)
	return a.manager.Allow(subscriptionID), nil
}

// InMemoryCircuitBreakerAdapter adapts CircuitBreakerManager to the CircuitBreaker interface.
type InMemoryCircuitBreakerAdapter struct {
	manager *CircuitBreakerManager
}

// NewInMemoryCircuitBreakerAdapter creates a new adapter for in-memory circuit breaking.
func NewInMemoryCircuitBreakerAdapter(config CircuitBreakerConfig) *InMemoryCircuitBreakerAdapter {
	return &InMemoryCircuitBreakerAdapter{
		manager: NewCircuitBreakerManager(config),
	}
}

// Allow implements CircuitBreaker interface.
func (a *InMemoryCircuitBreakerAdapter) Allow(ctx context.Context, subscriptionID string) (bool, error) {
	state := a.manager.State(subscriptionID)
	return state != CircuitBreakerStateOpen, nil
}

// RecordSuccess implements CircuitBreaker interface.
// Note: gobreaker handles this internally via Execute, so this is a no-op for compatibility.
func (a *InMemoryCircuitBreakerAdapter) RecordSuccess(ctx context.Context, subscriptionID string) error {
	// gobreaker tracks success/failure internally via Execute
	return nil
}

// RecordFailure implements CircuitBreaker interface.
// Note: gobreaker handles this internally via Execute, so this is a no-op for compatibility.
func (a *InMemoryCircuitBreakerAdapter) RecordFailure(ctx context.Context, subscriptionID string) error {
	// gobreaker tracks success/failure internally via Execute
	return nil
}

// State implements CircuitBreaker interface.
func (a *InMemoryCircuitBreakerAdapter) State(ctx context.Context, subscriptionID string) (CircuitState, error) {
	state := a.manager.State(subscriptionID)
	switch state {
	case CircuitBreakerStateClosed:
		return CircuitStateClosed, nil
	case CircuitBreakerStateOpen:
		return CircuitStateOpen, nil
	case CircuitBreakerStateHalfOpen:
		return CircuitStateHalfOpen, nil
	default:
		return CircuitStateClosed, nil
	}
}

// Execute runs a function through the in-memory circuit breaker.
// This properly tracks success/failure for the gobreaker implementation.
func (a *InMemoryCircuitBreakerAdapter) Execute(subscriptionID string, fn func() (interface{}, error)) (interface{}, error) {
	return a.manager.Execute(subscriptionID, fn)
}

// OnStateChange sets a callback for circuit breaker state changes.
func (a *InMemoryCircuitBreakerAdapter) OnStateChange(fn func(subscriptionID string, from, to CircuitBreakerState)) {
	a.manager.OnStateChange(fn)
}

// RedisConfig holds configuration for Redis connection.
type RedisConfig struct {
	URL          string
	PoolSize     int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// DefaultRedisConfig returns sensible defaults for Redis connection.
func DefaultRedisConfig() RedisConfig {
	return RedisConfig{
		URL:          "redis://localhost:6379/0",
		PoolSize:     10,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	}
}
