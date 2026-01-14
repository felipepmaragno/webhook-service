package resilience

import (
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
