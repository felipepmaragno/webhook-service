package resilience

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCircuitBreakerManager_Execute_Success(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	manager := NewCircuitBreakerManager(config)

	subID := "sub_success"

	result, err := manager.Execute(subID, func() (interface{}, error) {
		return "ok", nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %v", result)
	}
	if manager.State(subID) != CircuitBreakerStateClosed {
		t.Errorf("expected closed state, got %v", manager.State(subID))
	}
}

func TestCircuitBreakerManager_Execute_Failure_OpensCircuit(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxRequests:  1,
		Interval:     60 * time.Second,
		Timeout:      1 * time.Second,
		FailureRatio: 0.5,
		MinRequests:  2,
	}
	manager := NewCircuitBreakerManager(config)

	subID := "sub_failure"
	testErr := errors.New("test error")

	for i := 0; i < 3; i++ {
		_, _ = manager.Execute(subID, func() (interface{}, error) {
			return nil, testErr
		})
	}

	if manager.State(subID) != CircuitBreakerStateOpen {
		t.Errorf("expected open state after failures, got %v", manager.State(subID))
	}
}

func TestCircuitBreakerManager_OnStateChange(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxRequests:  1,
		Interval:     60 * time.Second,
		Timeout:      100 * time.Millisecond,
		FailureRatio: 0.5,
		MinRequests:  2,
	}
	manager := NewCircuitBreakerManager(config)

	var stateChanges []struct {
		from, to CircuitBreakerState
	}
	var mu sync.Mutex

	manager.OnStateChange(func(subID string, from, to CircuitBreakerState) {
		mu.Lock()
		stateChanges = append(stateChanges, struct{ from, to CircuitBreakerState }{from, to})
		mu.Unlock()
	})

	subID := "sub_state_change"
	testErr := errors.New("test error")

	for i := 0; i < 3; i++ {
		_, _ = manager.Execute(subID, func() (interface{}, error) {
			return nil, testErr
		})
	}

	mu.Lock()
	if len(stateChanges) == 0 {
		t.Error("expected state change callback to be called")
	}
	if len(stateChanges) > 0 && stateChanges[0].to != CircuitBreakerStateOpen {
		t.Errorf("expected transition to open, got %v", stateChanges[0].to)
	}
	mu.Unlock()
}

func TestCircuitBreakerManager_ConcurrentAccess(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	manager := NewCircuitBreakerManager(config)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = manager.Execute("sub_concurrent", func() (interface{}, error) {
				return "ok", nil
			})
		}()
	}
	wg.Wait()
}

func TestCircuitBreakerManager_Remove(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxRequests:  1,
		Interval:     60 * time.Second,
		Timeout:      1 * time.Second,
		FailureRatio: 0.5,
		MinRequests:  2,
	}
	manager := NewCircuitBreakerManager(config)

	subID := "sub_remove"
	testErr := errors.New("test error")

	for i := 0; i < 3; i++ {
		_, _ = manager.Execute(subID, func() (interface{}, error) {
			return nil, testErr
		})
	}

	if manager.State(subID) != CircuitBreakerStateOpen {
		t.Errorf("expected open state, got %v", manager.State(subID))
	}

	manager.Remove(subID)

	if manager.State(subID) != CircuitBreakerStateClosed {
		t.Errorf("after remove, new breaker should be closed, got %v", manager.State(subID))
	}
}

// TestSimpleCircuitBreaker_ManualRecording tests the SimpleCircuitBreaker
// which is used by the Kafka handler with manual RecordSuccess/RecordFailure calls.
func TestSimpleCircuitBreaker_ManualRecording(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxRequests: 2,                      // 2 successes to close from half-open
		Timeout:     100 * time.Millisecond, // Short timeout for test
		MinRequests: 3,                      // 3 failures to open
	}

	cb := NewInMemoryCircuitBreakerAdapter(config)
	ctx := context.Background()
	subID := "test-sub"

	// Should be allowed (closed)
	allowed, _ := cb.Allow(ctx, subID)
	if !allowed {
		t.Error("expected allowed when closed")
	}

	// Record 3 failures -> should open
	_ = cb.RecordFailure(ctx, subID)
	_ = cb.RecordFailure(ctx, subID)
	_ = cb.RecordFailure(ctx, subID)

	// Should be blocked (open)
	allowed, _ = cb.Allow(ctx, subID)
	if allowed {
		t.Error("expected blocked after 3 failures")
	}

	state, _ := cb.State(ctx, subID)
	if state != CircuitStateOpen {
		t.Errorf("expected open state, got %v", state)
	}

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)

	// Should be allowed (half-open)
	allowed, _ = cb.Allow(ctx, subID)
	if !allowed {
		t.Error("expected allowed after timeout (half-open)")
	}

	state, _ = cb.State(ctx, subID)
	if state != CircuitStateHalfOpen {
		t.Errorf("expected half-open state, got %v", state)
	}

	// Record 2 successes -> should close
	_ = cb.RecordSuccess(ctx, subID)
	_ = cb.RecordSuccess(ctx, subID)

	state, _ = cb.State(ctx, subID)
	if state != CircuitStateClosed {
		t.Errorf("expected closed state after successes, got %v", state)
	}
}

// TestSimpleCircuitBreaker_FailureInHalfOpen tests that failure in half-open reopens circuit.
func TestSimpleCircuitBreaker_FailureInHalfOpen(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxRequests: 2,
		Timeout:     50 * time.Millisecond,
		MinRequests: 2,
	}

	cb := NewInMemoryCircuitBreakerAdapter(config)
	ctx := context.Background()
	subID := "test-halfopen"

	// Open the circuit
	_ = cb.RecordFailure(ctx, subID)
	_ = cb.RecordFailure(ctx, subID)

	state, _ := cb.State(ctx, subID)
	if state != CircuitStateOpen {
		t.Errorf("expected open, got %v", state)
	}

	// Wait for timeout -> half-open
	time.Sleep(60 * time.Millisecond)
	_, _ = cb.Allow(ctx, subID) // Triggers transition to half-open

	state, _ = cb.State(ctx, subID)
	if state != CircuitStateHalfOpen {
		t.Errorf("expected half-open, got %v", state)
	}

	// Failure in half-open -> should reopen
	_ = cb.RecordFailure(ctx, subID)

	state, _ = cb.State(ctx, subID)
	if state != CircuitStateOpen {
		t.Errorf("expected open after failure in half-open, got %v", state)
	}
}
