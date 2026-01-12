package resilience

import (
	"sync"
	"time"

	"github.com/sony/gobreaker"
)

// Circuit Breaker Pattern Implementation
//
// The circuit breaker prevents cascading failures by stopping requests to
// failing destinations. It has three states:
//
//   - Closed: Normal operation, requests pass through.
//   - Open: Destination is failing, requests are rejected immediately.
//   - Half-Open: Testing if destination recovered, limited requests allowed.
//
// State transitions:
//
//	[Closed] ---(failure threshold reached)---> [Open]
//	[Open] ---(timeout expires)---> [Half-Open]
//	[Half-Open] ---(success)---> [Closed]
//	[Half-Open] ---(failure)---> [Open]

// CircuitBreakerConfig defines the circuit breaker behavior.
//
// MaxRequests is the maximum number of requests allowed in half-open state.
// Interval is the cyclic period for clearing internal counts while closed.
// Timeout is how long to wait in open state before transitioning to half-open.
// FailureRatio is the failure percentage threshold to trip the breaker (0.0-1.0).
// MinRequests is the minimum requests needed before failure ratio is evaluated.
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

// CircuitBreakerState represents the current state of a circuit breaker.
type CircuitBreakerState string

const (
	CircuitBreakerStateClosed   CircuitBreakerState = "closed"
	CircuitBreakerStateOpen     CircuitBreakerState = "open"
	CircuitBreakerStateHalfOpen CircuitBreakerState = "half-open"
)

// CircuitBreakerManager maintains per-subscription circuit breakers.
// Each subscription gets an independent breaker to isolate failures.
// This prevents a single failing destination from affecting deliveries
// to healthy destinations.
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

// OnStateChange registers a callback for circuit breaker state transitions.
// Used to emit metrics and logs when breakers open or close.
func (m *CircuitBreakerManager) OnStateChange(fn func(subscriptionID string, from, to CircuitBreakerState)) {
	m.onStateChange = fn
}

// GetBreaker returns the circuit breaker for a subscription, creating one if needed.
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

// Execute runs a function through the circuit breaker.
// If the breaker is open, returns ErrOpenState immediately without calling fn.
// Failures from fn count toward the failure threshold.
func (m *CircuitBreakerManager) Execute(subscriptionID string, fn func() (interface{}, error)) (interface{}, error) {
	return m.GetBreaker(subscriptionID).Execute(fn)
}

// State returns the current state of the circuit breaker for a subscription.
func (m *CircuitBreakerManager) State(subscriptionID string) CircuitBreakerState {
	return toState(m.GetBreaker(subscriptionID).State())
}

// Remove deletes the circuit breaker for a subscription, freeing memory.
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
