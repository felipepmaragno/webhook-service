package resilience

import (
	"sync"
	"time"

	"github.com/sony/gobreaker"
)

type CircuitBreakerConfig struct {
	MaxRequests  uint32
	Interval     time.Duration
	Timeout      time.Duration
	FailureRatio float64
	MinRequests  uint32
}

func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		MaxRequests:  5,
		Interval:     60 * time.Second,
		Timeout:      30 * time.Second,
		FailureRatio: 0.5,
		MinRequests:  3,
	}
}

type CircuitBreakerState string

const (
	CircuitBreakerStateClosed   CircuitBreakerState = "closed"
	CircuitBreakerStateOpen     CircuitBreakerState = "open"
	CircuitBreakerStateHalfOpen CircuitBreakerState = "half-open"
)

type CircuitBreakerManager struct {
	config   CircuitBreakerConfig
	breakers map[string]*gobreaker.CircuitBreaker
	mu       sync.RWMutex

	onStateChange func(subscriptionID string, from, to CircuitBreakerState)
}

func NewCircuitBreakerManager(config CircuitBreakerConfig) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		config:   config,
		breakers: make(map[string]*gobreaker.CircuitBreaker),
	}
}

func (m *CircuitBreakerManager) OnStateChange(fn func(subscriptionID string, from, to CircuitBreakerState)) {
	m.onStateChange = fn
}

func (m *CircuitBreakerManager) GetBreaker(subscriptionID string) *gobreaker.CircuitBreaker {
	m.mu.RLock()
	cb, exists := m.breakers[subscriptionID]
	m.mu.RUnlock()

	if exists {
		return cb
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if cb, exists = m.breakers[subscriptionID]; exists {
		return cb
	}

	settings := gobreaker.Settings{
		Name:        subscriptionID,
		MaxRequests: m.config.MaxRequests,
		Interval:    m.config.Interval,
		Timeout:     m.config.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < m.config.MinRequests {
				return false
			}
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= m.config.FailureRatio
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			if m.onStateChange != nil {
				m.onStateChange(name, toState(from), toState(to))
			}
		},
	}

	cb = gobreaker.NewCircuitBreaker(settings)
	m.breakers[subscriptionID] = cb
	return cb
}

func (m *CircuitBreakerManager) Execute(subscriptionID string, fn func() (interface{}, error)) (interface{}, error) {
	return m.GetBreaker(subscriptionID).Execute(fn)
}

func (m *CircuitBreakerManager) State(subscriptionID string) CircuitBreakerState {
	return toState(m.GetBreaker(subscriptionID).State())
}

func (m *CircuitBreakerManager) Remove(subscriptionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.breakers, subscriptionID)
}

func toState(s gobreaker.State) CircuitBreakerState {
	switch s {
	case gobreaker.StateClosed:
		return CircuitBreakerStateClosed
	case gobreaker.StateOpen:
		return CircuitBreakerStateOpen
	case gobreaker.StateHalfOpen:
		return CircuitBreakerStateHalfOpen
	default:
		return CircuitBreakerStateClosed
	}
}
