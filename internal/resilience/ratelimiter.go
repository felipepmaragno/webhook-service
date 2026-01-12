package resilience

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

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

func (m *RateLimiterManager) Allow(subscriptionID string) bool {
	return m.GetLimiter(subscriptionID).Allow()
}

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

func (m *RateLimiterManager) SetRate(subscriptionID string, requestsPerSecond float64, burstSize int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	limiter := rate.NewLimiter(rate.Limit(requestsPerSecond), burstSize)
	m.limiters[subscriptionID] = limiter
}

func (m *RateLimiterManager) Remove(subscriptionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.limiters, subscriptionID)
}
