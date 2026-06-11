package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisRateLimiter_Allow(t *testing.T) {
	client, cleanup := setupRedisClient(t)
	defer cleanup()

	ctx := context.Background()

	// Clean up test keys
	client.Del(ctx, "ratelimit:test_sub")

	config := RedisRateLimiterConfig{
		Window: time.Second,
	}
	limiter := NewRedisRateLimiter(client, config, nil)

	subID := "test_sub"

	// Should allow requests up to DefaultRateLimit (100)
	for i := 0; i < DefaultRateLimit; i++ {
		allowed, err := limiter.Allow(ctx, subID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// Next request should be rate limited
	allowed, err := limiter.Allow(ctx, subID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("request 101 should be rate limited")
	}

	// Clean up
	client.Del(ctx, "ratelimit:test_sub")
}

func TestRedisRateLimiter_WindowExpiry(t *testing.T) {
	client, cleanup := setupRedisClient(t)
	defer cleanup()

	ctx := context.Background()

	// Clean up test keys
	client.Del(ctx, "ratelimit:test_window")

	config := RedisRateLimiterConfig{
		Window: time.Second,
	}
	limiter := NewRedisRateLimiter(client, config, nil)

	subID := "test_window"

	// Use up all requests (DefaultRateLimit = 100)
	for i := 0; i < DefaultRateLimit; i++ {
		allowed, _ := limiter.Allow(ctx, subID)
		if !allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// Should be rate limited now
	allowed, _ := limiter.Allow(ctx, subID)
	if allowed {
		t.Error("should be rate limited after 100 requests")
	}

	// Wait for window to expire
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again after window
	allowed, _ = limiter.Allow(ctx, subID)
	if !allowed {
		t.Error("should be allowed after window expiry")
	}

	// Clean up
	client.Del(ctx, "ratelimit:test_window")
}

func TestRedisRateLimiter_Fallback(t *testing.T) {
	// Use invalid Redis address to trigger fallback
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:9999", // Invalid port
	})
	defer func() { _ = client.Close() }()

	config := DefaultRedisRateLimiterConfig()
	limiter := NewRedisRateLimiter(client, config, nil)

	ctx := context.Background()
	subID := "test_fallback"

	// Should fall back to in-memory and still work
	allowed, err := limiter.Allow(ctx, subID)
	if err != nil {
		t.Fatalf("should not return error on fallback: %v", err)
	}
	if !allowed {
		t.Error("should be allowed via fallback")
	}
}
