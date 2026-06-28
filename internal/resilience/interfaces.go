// Package resilience provides rate limiting and circuit breaker implementations
// for protecting destination endpoints from overload.
package resilience

import (
	"context"
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
