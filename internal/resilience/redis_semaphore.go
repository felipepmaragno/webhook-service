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

// Semaphore controls concurrent access to a resource.
type Semaphore interface {
	// Acquire attempts to acquire a slot. Returns true if acquired, false if limit reached.
	// The caller must call Release when done if Acquire returns true.
	Acquire(ctx context.Context, key string) (bool, error)
	// Release releases a previously acquired slot.
	Release(ctx context.Context, key string) error
}

// RedisSemaphore implements distributed semaphore using Redis.
// It uses a counter with TTL to track concurrent usage across instances.
// This ensures that multiple workers don't overwhelm a single destination.
type RedisSemaphore struct {
	client   *redis.Client
	limit    int
	ttl      time.Duration
	fallback *LocalSemaphoreManager
	logger   *slog.Logger
}

// RedisSemaphoreConfig holds configuration for the Redis semaphore.
type RedisSemaphoreConfig struct {
	// Limit is the maximum concurrent acquisitions per key (default: 100)
	Limit int
	// TTL is how long an acquisition is valid before auto-release (default: 30s)
	// This prevents deadlocks if a worker crashes without releasing.
	TTL time.Duration
}

// DefaultRedisSemaphoreConfig returns sensible defaults.
func DefaultRedisSemaphoreConfig() RedisSemaphoreConfig {
	return RedisSemaphoreConfig{
		Limit: 100,
		TTL:   30 * time.Second,
	}
}

// NewRedisSemaphore creates a new Redis-backed distributed semaphore.
func NewRedisSemaphore(client *redis.Client, config RedisSemaphoreConfig, logger *slog.Logger) *RedisSemaphore {
	if config.Limit <= 0 {
		config.Limit = 100
	}
	if config.TTL == 0 {
		config.TTL = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &RedisSemaphore{
		client:   client,
		limit:    config.Limit,
		ttl:      config.TTL,
		fallback: NewLocalSemaphoreManager(config.Limit),
		logger:   logger,
	}
}

// acquireScript atomically checks and increments the semaphore counter.
// Returns 1 if acquired, 0 if limit reached.
var acquireScript = redis.NewScript(`
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local ttl_ms = tonumber(ARGV[2])

local current = redis.call('GET', key)
if not current then
    current = 0
else
    current = tonumber(current)
end

if current < limit then
    redis.call('INCR', key)
    redis.call('PEXPIRE', key, ttl_ms)
    return 1
else
    return 0
end
`)

// Acquire attempts to acquire a semaphore slot for the given key.
// Returns true if acquired, false if the limit is reached.
func (s *RedisSemaphore) Acquire(ctx context.Context, key string) (bool, error) {
	redisKey := fmt.Sprintf("sem:%s", key)
	ttlMs := s.ttl.Milliseconds()

	result, err := acquireScript.Run(ctx, s.client, []string{redisKey}, s.limit, ttlMs).Int()
	if err != nil {
		s.logger.Warn("redis semaphore acquire failed, using fallback",
			"error", err,
			"key", key,
		)
		return s.fallback.Acquire(key), nil
	}

	return result == 1, nil
}

// Release releases a semaphore slot for the given key.
func (s *RedisSemaphore) Release(ctx context.Context, key string) error {
	redisKey := fmt.Sprintf("sem:%s", key)

	// Decrement but don't go below 0
	result, err := s.client.Decr(ctx, redisKey).Result()
	if err != nil {
		s.logger.Warn("redis semaphore release failed",
			"error", err,
			"key", key,
		)
		s.fallback.Release(key)
		return nil
	}

	// If we went negative, reset to 0 (shouldn't happen in normal operation)
	if result < 0 {
		s.client.Set(ctx, redisKey, 0, s.ttl)
	}

	return nil
}

// LocalSemaphoreManager provides in-memory semaphores as fallback.
type LocalSemaphoreManager struct {
	limit      int
	semaphores map[string]chan struct{}
}

// NewLocalSemaphoreManager creates a new local semaphore manager.
func NewLocalSemaphoreManager(limit int) *LocalSemaphoreManager {
	return &LocalSemaphoreManager{
		limit:      limit,
		semaphores: make(map[string]chan struct{}),
	}
}

// Acquire attempts to acquire a local semaphore slot (non-blocking).
func (m *LocalSemaphoreManager) Acquire(key string) bool {
	sem, exists := m.semaphores[key]
	if !exists {
		sem = make(chan struct{}, m.limit)
		m.semaphores[key] = sem
	}

	select {
	case sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release releases a local semaphore slot.
func (m *LocalSemaphoreManager) Release(key string) {
	if sem, exists := m.semaphores[key]; exists {
		select {
		case <-sem:
		default:
		}
	}
}
