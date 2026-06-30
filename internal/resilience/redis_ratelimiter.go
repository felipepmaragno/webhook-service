package resilience

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/felipemaragno/dispatch/internal/domain"
)

type RedisRateLimiterConfig struct {
	Window time.Duration
}

func DefaultRedisRateLimiterConfig() RedisRateLimiterConfig {
	return RedisRateLimiterConfig{Window: time.Second}
}

// RedisRateLimiter implements distributed max-delivery-rate enforcement with a
// Redis sorted-set sliding window. Redis errors fail closed so a configured
// distributed limit does not silently degrade into an uncoordinated local limit.
type RedisRateLimiter struct {
	client  *redis.Client
	window  time.Duration
	logger  *slog.Logger
	counter uint64
}

func NewRedisRateLimiter(client *redis.Client, config RedisRateLimiterConfig, logger *slog.Logger) *RedisRateLimiter {
	if config.Window <= 0 {
		config.Window = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RedisRateLimiter{
		client: client,
		window: config.Window,
		logger: logger,
	}
}

var redisSlidingWindowScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local member = ARGV[4]

redis.call('ZREMRANGEBYSCORE', key, 0, now - window)

local count = redis.call('ZCARD', key)
if count < limit then
    redis.call('ZADD', key, now, member)
    redis.call('PEXPIRE', key, window)
    return 1
end

redis.call('PEXPIRE', key, window)
return 0
`)

func (r *RedisRateLimiter) Allow(ctx context.Context, subscriptionID string, policy domain.RatePolicy) (RateLimitDecision, error) {
	limit := policy.RequestsPerSecond
	if limit <= 0 {
		limit = domain.DefaultSubscriptionMaxDeliveryRate
	}

	now := time.Now().UnixMilli()
	windowMs := r.window.Milliseconds()
	member := fmt.Sprintf("%d:%d", now, atomic.AddUint64(&r.counter, 1))
	key := fmt.Sprintf("ratelimit:%s", subscriptionID)

	result, err := redisSlidingWindowScript.Run(ctx, r.client, []string{key}, now, windowMs, limit, member).Int()
	if err != nil {
		r.logger.Warn("redis rate limiter unavailable; failing closed", "error", err, "subscription_id", subscriptionID)
		return RateLimitDecision{Allowed: false, RetryAfter: r.window}, err
	}
	if result == 1 {
		return RateLimitDecision{Allowed: true}, nil
	}
	return RateLimitDecision{Allowed: false, RetryAfter: r.window}, nil
}
