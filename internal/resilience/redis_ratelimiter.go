// Package resilience provides rate limiting and circuit breaker implementations
// for protecting destination endpoints from overload.
package resilience

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRateLimiter implements distributed rate limiting using Redis sorted sets.
// It uses a sliding window algorithm where each request is stored as a member
// with its timestamp as the score. This allows precise rate limiting across
// multiple application instances.
//
// Algorithm:
//  1. Remove entries older than the window
//  2. Count remaining entries
//  3. If count < limit, add new entry and allow
//  4. Otherwise, reject
//
// All operations are atomic using a Lua script.
type RedisRateLimiter struct {
	client   *redis.Client
	window   time.Duration
	fallback *RateLimiterManager
	logger   *slog.Logger
}

// RedisRateLimiterConfig holds configuration for the Redis rate limiter.
type RedisRateLimiterConfig struct {
	Window time.Duration // Sliding window size (default: 1 second)
}

// DefaultRedisRateLimiterConfig returns sensible defaults.
func DefaultRedisRateLimiterConfig() RedisRateLimiterConfig {
	return RedisRateLimiterConfig{
		Window: time.Second,
	}
}

// NewRedisRateLimiter creates a new Redis-backed rate limiter.
// Falls back to in-memory rate limiting when Redis is unavailable.
func NewRedisRateLimiter(client *redis.Client, config RedisRateLimiterConfig, logger *slog.Logger) *RedisRateLimiter {
	if config.Window == 0 {
		config.Window = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &RedisRateLimiter{
		client:   client,
		window:   config.Window,
		fallback: NewRateLimiterManager(DefaultRateLimiterConfig()),
		logger:   logger,
	}
}

// rateLimitScript is a Lua script that atomically checks and updates rate limit.
// Returns 1 if allowed, 0 if rate limited.
var rateLimitScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local member = ARGV[4]

-- Remove old entries outside the window
redis.call('ZREMRANGEBYSCORE', key, 0, now - window)

-- Count current entries
local count = redis.call('ZCARD', key)

if count < limit then
    -- Add new entry and set TTL
    redis.call('ZADD', key, now, member)
    redis.call('PEXPIRE', key, window)
    return 1
else
    return 0
end
`)

// Allow checks if a request is allowed for the given subscription.
// Returns true if allowed, false if rate limited.
// Falls back to in-memory rate limiting if Redis is unavailable.
func (r *RedisRateLimiter) Allow(ctx context.Context, subscriptionID string, limit int) (bool, error) {
	key := fmt.Sprintf("ratelimit:%s", subscriptionID)
	now := time.Now().UnixMilli()
	windowMs := r.window.Milliseconds()
	member := fmt.Sprintf("%d:%d", now, time.Now().UnixNano()%1000000) // unique member

	result, err := rateLimitScript.Run(ctx, r.client, []string{key}, now, windowMs, limit, member).Int()
	if err != nil {
		r.logger.Warn("redis rate limiter failed, using fallback",
			"error", err,
			"subscription_id", subscriptionID,
		)
		// Fallback to in-memory
		r.fallback.SetRateIfNotExists(subscriptionID, float64(limit), limit/10+1)
		return r.fallback.Allow(subscriptionID), nil
	}

	return result == 1, nil
}

// Close closes the Redis client connection.
func (r *RedisRateLimiter) Close() error {
	return r.client.Close()
}
