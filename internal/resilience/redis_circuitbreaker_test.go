package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisCircuitBreaker_Allow(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping integration test")
	}

	// Clean up test keys
	cleanupCBKeys(client, ctx, "test_cb_allow")

	config := DefaultRedisCircuitBreakerConfig()
	cb := NewRedisCircuitBreaker(client, config, nil)

	subID := "test_cb_allow"

	// Initially should be allowed (closed state)
	allowed, err := cb.Allow(ctx, subID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("should be allowed when circuit is closed")
	}

	// State should be closed
	state, _ := cb.State(ctx, subID)
	if state != CircuitStateClosed {
		t.Errorf("expected closed state, got %s", state)
	}

	cleanupCBKeys(client, ctx, subID)
}

func TestRedisCircuitBreaker_OpensAfterFailures(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping integration test")
	}

	subID := "test_cb_open"
	cleanupCBKeys(client, ctx, subID)

	config := RedisCircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          100 * time.Millisecond,
		Window:           time.Second,
	}
	cb := NewRedisCircuitBreaker(client, config, nil)

	// Record failures to trip the circuit
	for i := 0; i < 3; i++ {
		_ = cb.RecordFailure(ctx, subID)
	}

	// Circuit should be open
	state, _ := cb.State(ctx, subID)
	if state != CircuitStateOpen {
		t.Errorf("expected open state after failures, got %s", state)
	}

	// Should not be allowed
	allowed, _ := cb.Allow(ctx, subID)
	if allowed {
		t.Error("should not be allowed when circuit is open")
	}

	cleanupCBKeys(client, ctx, subID)
}

func TestRedisCircuitBreaker_TransitionsToHalfOpen(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping integration test")
	}

	subID := "test_cb_halfopen"
	cleanupCBKeys(client, ctx, subID)

	config := RedisCircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 2,
		Timeout:          50 * time.Millisecond, // Short timeout for testing
		Window:           time.Second,
	}
	cb := NewRedisCircuitBreaker(client, config, nil)

	// Trip the circuit
	_ = cb.RecordFailure(ctx, subID)
	_ = cb.RecordFailure(ctx, subID)

	state, _ := cb.State(ctx, subID)
	if state != CircuitStateOpen {
		t.Fatalf("expected open state, got %s", state)
	}

	// Wait for timeout
	time.Sleep(100 * time.Millisecond)

	// Allow should transition to half-open
	allowed, _ := cb.Allow(ctx, subID)
	if !allowed {
		t.Error("should be allowed after timeout (half-open)")
	}

	state, _ = cb.State(ctx, subID)
	if state != CircuitStateHalfOpen {
		t.Errorf("expected half-open state, got %s", state)
	}

	cleanupCBKeys(client, ctx, subID)
}

func TestRedisCircuitBreaker_ClosesAfterSuccesses(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping integration test")
	}

	subID := "test_cb_close"
	cleanupCBKeys(client, ctx, subID)

	config := RedisCircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 2,
		Timeout:          50 * time.Millisecond,
		Window:           time.Second,
	}
	cb := NewRedisCircuitBreaker(client, config, nil)

	// Trip the circuit
	_ = cb.RecordFailure(ctx, subID)
	_ = cb.RecordFailure(ctx, subID)

	// Wait for timeout and transition to half-open
	time.Sleep(100 * time.Millisecond)
	_, _ = cb.Allow(ctx, subID)

	// Record successes to close the circuit
	_ = cb.RecordSuccess(ctx, subID)
	_ = cb.RecordSuccess(ctx, subID)

	state, _ := cb.State(ctx, subID)
	if state != CircuitStateClosed {
		t.Errorf("expected closed state after successes, got %s", state)
	}

	cleanupCBKeys(client, ctx, subID)
}

func TestRedisCircuitBreaker_Fallback(t *testing.T) {
	// Use invalid Redis address to trigger fallback
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:9999",
	})
	defer func() { _ = client.Close() }()

	config := DefaultRedisCircuitBreakerConfig()
	cb := NewRedisCircuitBreaker(client, config, nil)

	ctx := context.Background()
	subID := "test_fallback"

	// Should fall back to in-memory and still work
	allowed, err := cb.Allow(ctx, subID)
	if err != nil {
		t.Fatalf("should not return error on fallback: %v", err)
	}
	if !allowed {
		t.Error("should be allowed via fallback")
	}
}

func cleanupCBKeys(client *redis.Client, ctx context.Context, subID string) {
	client.Del(ctx,
		"cb:"+subID+":state",
		"cb:"+subID+":failures",
		"cb:"+subID+":successes",
		"cb:"+subID+":opened_at",
	)
}
