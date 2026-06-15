// Package resilience provides rate limiting and circuit breaker implementations
// for protecting destination endpoints from overload.
package resilience

import (
	"context"
	"sync"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
)

type RateLimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
	Degraded   bool
}

// RateLimiter defines the interface for rate limiting implementations.
// This allows swapping between in-memory and Redis-backed implementations.
type RateLimiter interface {
	// Allow checks if a request is allowed for the given subscription policy.
	Allow(ctx context.Context, subscriptionID string, policy domain.RatePolicy) (RateLimitDecision, error)
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

// StateChangeNotifier is an optional interface implemented by circuit breakers
// that support state-change callbacks. Used to wire Prometheus metrics without
// coupling the core CircuitBreaker interface to observability concerns.
type StateChangeNotifier interface {
	// OnStateChange registers a callback invoked whenever a circuit transitions
	// between states. from and to are CircuitState values.
	OnStateChange(fn func(subscriptionID string, from, to CircuitState))
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
func (a *InMemoryRateLimiterAdapter) Allow(ctx context.Context, subscriptionID string, policy domain.RatePolicy) (RateLimitDecision, error) {
	allowed, retryAfter := a.manager.AllowWithPolicy(subscriptionID, policy)
	return RateLimitDecision{Allowed: allowed, RetryAfter: retryAfter}, nil
}

// SimpleCircuitBreaker implements CircuitBreaker with manual success/failure tracking.
// Unlike gobreaker which requires Execute(), this works with RecordSuccess/RecordFailure calls.
// Also implements StateChangeNotifier.
type SimpleCircuitBreaker struct {
	mu            sync.RWMutex
	breakers      map[string]*simpleBreaker
	config        CircuitBreakerConfig
	onStateChange func(subscriptionID string, from, to CircuitState)
}

// OnStateChange registers a callback invoked on every circuit state transition.
// Implements StateChangeNotifier.
func (s *SimpleCircuitBreaker) OnStateChange(fn func(subscriptionID string, from, to CircuitState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onStateChange = fn
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
	prev := b.state
	b.failures = 0 // Reset failures on success

	if b.state == CircuitStateHalfOpen {
		b.successes++
		if b.successes >= int(s.config.MaxRequests) {
			b.state = CircuitStateClosed
		}
	}
	next := b.state
	cb := s.onStateChange
	s.mu.Unlock()

	if cb != nil && next != prev {
		cb(subscriptionID, prev, next)
	}
	return nil
}

// RecordFailure records a failed request.
func (s *SimpleCircuitBreaker) RecordFailure(ctx context.Context, subscriptionID string) error {
	b := s.getBreaker(subscriptionID)

	s.mu.Lock()
	prev := b.state
	b.failures++
	b.lastFailure = time.Now()

	switch b.state {
	case CircuitStateHalfOpen:
		// Any failure in half-open reopens the circuit
		b.state = CircuitStateOpen
		b.openedAt = time.Now()
	case CircuitStateClosed:
		// Check if we should open
		if b.failures >= int(s.config.MinRequests) {
			b.state = CircuitStateOpen
			b.openedAt = time.Now()
		}
	}
	next := b.state
	cb := s.onStateChange
	s.mu.Unlock()

	if cb != nil && next != prev {
		cb(subscriptionID, prev, next)
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
