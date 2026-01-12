// Package resilience provides rate limiting and circuit breaker patterns for
// protecting webhook destinations from overload and cascading failures.
//
// This package uses:
//   - golang.org/x/time/rate: Token bucket rate limiter from the Go team.
//     Chosen for its simplicity, efficiency, and official support.
//   - github.com/sony/gobreaker: Circuit breaker implementation by Sony.
//     Chosen for its battle-tested reliability and clean API.
package resilience

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiterConfig defines the rate limiting parameters.
//
// RequestsPerSecond controls the steady-state rate of allowed requests.
// BurstSize allows temporary spikes above the rate limit.
type RateLimiterConfig struct {
	RequestsPerSecond float64
	BurstSize         int
}

func DefaultRateLimiterConfig() RateLimiterConfig {
	return RateLimiterConfig{
		RequestsPerSecond: 100,
		BurstSize:         10,
	}
}

// RateLimiterManager maintains per-subscription rate limiters.
// It uses lazy initialization with double-checked locking for thread safety.
// Each subscription gets its own independent rate limiter to prevent
// one destination from affecting others.
type RateLimiterManager struct {
	config   RateLimiterConfig
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
}

func NewRateLimiterManager(config RateLimiterConfig) *RateLimiterManager {
	return &RateLimiterManager{
		config:   config,
		limiters: make(map[string]*rate.Limiter),
	}
}

// GetLimiter returns the rate limiter for a subscription, creating one if needed.
// Uses double-checked locking pattern for optimal concurrent performance.
func (m *RateLimiterManager) GetLimiter(subscriptionID string) *rate.Limiter {
	m.mu.RLock()
	limiter, exists := m.limiters[subscriptionID]
	m.mu.RUnlock()

	if exists {
		return limiter
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if limiter, exists = m.limiters[subscriptionID]; exists {
		return limiter
	}

	limiter = rate.NewLimiter(rate.Limit(m.config.RequestsPerSecond), m.config.BurstSize)
	m.limiters[subscriptionID] = limiter
	return limiter
}

// Allow reports whether a request for the subscription is allowed right now.
// Returns false if the rate limit has been exceeded.
func (m *RateLimiterManager) Allow(subscriptionID string) bool {
	return m.GetLimiter(subscriptionID).Allow()
}

// Wait returns how long the caller would need to wait before the next request
// would be allowed. Useful for implementing backoff strategies.
func (m *RateLimiterManager) Wait(subscriptionID string) time.Duration {
	limiter := m.GetLimiter(subscriptionID)
	reservation := limiter.Reserve()
	if !reservation.OK() {
		return 0
	}
	delay := reservation.Delay()
	reservation.Cancel()
	return delay
}

// SetRate configures a custom rate limit for a specific subscription.
// This allows per-destination rate limiting based on subscription settings.
func (m *RateLimiterManager) SetRate(subscriptionID string, requestsPerSecond float64, burstSize int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	limiter := rate.NewLimiter(rate.Limit(requestsPerSecond), burstSize)
	m.limiters[subscriptionID] = limiter
}

// SetRateIfNotExists configures a rate limit only if one doesn't already exist.
// This is used to lazily initialize rate limiters with subscription-specific settings.
func (m *RateLimiterManager) SetRateIfNotExists(subscriptionID string, requestsPerSecond float64, burstSize int) {
	m.mu.RLock()
	_, exists := m.limiters[subscriptionID]
	m.mu.RUnlock()

	if exists {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if _, exists = m.limiters[subscriptionID]; exists {
		return
	}

	limiter := rate.NewLimiter(rate.Limit(requestsPerSecond), burstSize)
	m.limiters[subscriptionID] = limiter
}

// Remove deletes the rate limiter for a subscription, freeing memory.
// Should be called when a subscription is deleted.
func (m *RateLimiterManager) Remove(subscriptionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.limiters, subscriptionID)
}
