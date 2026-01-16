// Package resilience provides rate limiting and circuit breaker implementations
// for protecting destination endpoints from overload.
package resilience

import (
	"context"
	"sync"
	"time"
)

// DefaultRateLimit is the fixed rate limit for all subscriptions (100 req/s).
const DefaultRateLimit = 100

// RateLimiter defines the interface for rate limiting implementations.
// This allows swapping between in-memory and Redis-backed implementations.
// Rate limit is fixed at 100 req/s for all subscriptions.
type RateLimiter interface {
	// Allow checks if a request is allowed for the given subscription.
	// Returns true if allowed, false if rate limited.
	Allow(ctx context.Context, subscriptionID string) (bool, error)
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
func (a *InMemoryRateLimiterAdapter) Allow(ctx context.Context, subscriptionID string) (bool, error) {
	return a.manager.Allow(subscriptionID), nil
}

// SimpleCircuitBreaker implements CircuitBreaker with manual success/failure tracking.
// Unlike gobreaker which requires Execute(), this works with RecordSuccess/RecordFailure calls.
type SimpleCircuitBreaker struct {
	mu       sync.RWMutex
	breakers map[string]*simpleBreaker
	config   CircuitBreakerConfig
}

type simpleBreaker struct {
	state       CircuitState
	failures    int
	successes   int
	lastFailure time.Time
	openedAt    time.Time
}

// NewInMemoryCircuitBreakerAdapter creates a simple in-memory circuit breaker.
func NewInMemoryCircuitBreakerAdapter(config CircuitBreakerConfig) *SimpleCircuitBreaker {
	return &SimpleCircuitBreaker{
		breakers: make(map[string]*simpleBreaker),
		config:   config,
	}
}

func (s *SimpleCircuitBreaker) getBreaker(subscriptionID string) *simpleBreaker {
	s.mu.Lock()
	defer s.mu.Unlock()

	if b, ok := s.breakers[subscriptionID]; ok {
		return b
	}

	b := &simpleBreaker{state: CircuitStateClosed}
	s.breakers[subscriptionID] = b
	return b
}

// Allow checks if a request should be allowed through the circuit breaker.
func (s *SimpleCircuitBreaker) Allow(ctx context.Context, subscriptionID string) (bool, error) {
	b := s.getBreaker(subscriptionID)

	s.mu.Lock()
	defer s.mu.Unlock()

	switch b.state {
	case CircuitStateClosed:
		return true, nil
	case CircuitStateOpen:
		// Check if timeout has passed
		if time.Since(b.openedAt) >= s.config.Timeout {
			b.state = CircuitStateHalfOpen
			b.successes = 0
			return true, nil
		}
		return false, nil
	case CircuitStateHalfOpen:
		return true, nil
	}
	return true, nil
}

// RecordSuccess records a successful request.
func (s *SimpleCircuitBreaker) RecordSuccess(ctx context.Context, subscriptionID string) error {
	b := s.getBreaker(subscriptionID)

	s.mu.Lock()
	defer s.mu.Unlock()

	b.failures = 0 // Reset failures on success

	if b.state == CircuitStateHalfOpen {
		b.successes++
		if b.successes >= int(s.config.MaxRequests) {
			b.state = CircuitStateClosed
		}
	}
	return nil
}

// RecordFailure records a failed request.
func (s *SimpleCircuitBreaker) RecordFailure(ctx context.Context, subscriptionID string) error {
	b := s.getBreaker(subscriptionID)

	s.mu.Lock()
	defer s.mu.Unlock()

	b.failures++
	b.lastFailure = time.Now()

	if b.state == CircuitStateHalfOpen {
		// Any failure in half-open reopens the circuit
		b.state = CircuitStateOpen
		b.openedAt = time.Now()
	} else if b.state == CircuitStateClosed {
		// Check if we should open
		if b.failures >= int(s.config.MinRequests) {
			b.state = CircuitStateOpen
			b.openedAt = time.Now()
		}
	}
	return nil
}

// State returns the current state of the circuit breaker.
func (s *SimpleCircuitBreaker) State(ctx context.Context, subscriptionID string) (CircuitState, error) {
	b := s.getBreaker(subscriptionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return b.state, nil
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
