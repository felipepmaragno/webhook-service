// Package resilience provides rate limiting and circuit breaker implementations
// for protecting destination endpoints from overload.
package resilience

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// CircuitState represents the state of a circuit breaker.
type CircuitState string

const (
	CircuitStateClosed   CircuitState = "closed"
	CircuitStateOpen     CircuitState = "open"
	CircuitStateHalfOpen CircuitState = "half-open"
)

// RedisCircuitBreaker implements distributed circuit breaker using Redis.
// State is shared across all application instances, ensuring consistent
// circuit breaker behavior in horizontally scaled deployments.
//
// States:
//   - Closed: Normal operation, requests pass through
//   - Open: Circuit tripped, requests fail fast
//   - Half-Open: Testing if service recovered, limited requests allowed
//
// State transitions are atomic using Lua scripts.
type RedisCircuitBreaker struct {
	client   *redis.Client
	config   RedisCircuitBreakerConfig
	fallback *CircuitBreakerManager
	logger   *slog.Logger
}

// RedisCircuitBreakerConfig holds configuration for the Redis circuit breaker.
type RedisCircuitBreakerConfig struct {
	// FailureThreshold is the number of failures before opening the circuit
	FailureThreshold int
	// SuccessThreshold is the number of successes in half-open to close the circuit
	SuccessThreshold int
	// Timeout is how long the circuit stays open before transitioning to half-open
	Timeout time.Duration
	// Window is the time window for counting failures
	Window time.Duration
}

// DefaultRedisCircuitBreakerConfig returns sensible defaults.
func DefaultRedisCircuitBreakerConfig() RedisCircuitBreakerConfig {
	return RedisCircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 3,
		Timeout:          30 * time.Second,
		Window:           60 * time.Second,
	}
}

// NewRedisCircuitBreaker creates a new Redis-backed circuit breaker.
func NewRedisCircuitBreaker(client *redis.Client, config RedisCircuitBreakerConfig, logger *slog.Logger) *RedisCircuitBreaker {
	if logger == nil {
		logger = slog.Default()
	}

	return &RedisCircuitBreaker{
		client:   client,
		config:   config,
		fallback: NewCircuitBreakerManager(DefaultCircuitBreakerConfig()),
		logger:   logger,
	}
}

func (r *RedisCircuitBreaker) keyState(subID string) string {
	return fmt.Sprintf("cb:%s:state", subID)
}

func (r *RedisCircuitBreaker) keyFailures(subID string) string {
	return fmt.Sprintf("cb:%s:failures", subID)
}

func (r *RedisCircuitBreaker) keySuccesses(subID string) string {
	return fmt.Sprintf("cb:%s:successes", subID)
}

func (r *RedisCircuitBreaker) keyOpenedAt(subID string) string {
	return fmt.Sprintf("cb:%s:opened_at", subID)
}

// allowScript checks if request is allowed and handles state transitions.
// Returns: 1 = allowed, 0 = blocked (circuit open)
var allowScript = redis.NewScript(`
local state_key = KEYS[1]
local opened_at_key = KEYS[2]
local now = tonumber(ARGV[1])
local timeout_ms = tonumber(ARGV[2])

local state = redis.call('GET', state_key)
if not state then
    state = 'closed'
end

if state == 'closed' then
    return 1
elseif state == 'open' then
    local opened_at = redis.call('GET', opened_at_key)
    if opened_at and (now - tonumber(opened_at)) >= timeout_ms then
        -- Transition to half-open
        redis.call('SET', state_key, 'half-open')
        return 1
    end
    return 0
elseif state == 'half-open' then
    return 1
end

return 1
`)

// Allow checks if a request should be allowed through the circuit breaker.
func (r *RedisCircuitBreaker) Allow(ctx context.Context, subscriptionID string) (bool, error) {
	now := time.Now().UnixMilli()
	timeoutMs := r.config.Timeout.Milliseconds()

	result, err := allowScript.Run(ctx, r.client,
		[]string{r.keyState(subscriptionID), r.keyOpenedAt(subscriptionID)},
		now, timeoutMs,
	).Int()

	if err != nil {
		r.logger.Warn("redis circuit breaker failed, using fallback",
			"error", err,
			"subscription_id", subscriptionID,
		)
		state := r.fallback.State(subscriptionID)
		return state != CircuitBreakerStateOpen, nil
	}

	return result == 1, nil
}

// recordSuccessScript handles success recording and state transitions.
var recordSuccessScript = redis.NewScript(`
local state_key = KEYS[1]
local successes_key = KEYS[2]
local failures_key = KEYS[3]
local success_threshold = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])

local state = redis.call('GET', state_key)
if not state then
    state = 'closed'
end

if state == 'half-open' then
    local successes = redis.call('INCR', successes_key)
    redis.call('PEXPIRE', successes_key, window_ms)
    
    if successes >= success_threshold then
        -- Close the circuit
        redis.call('SET', state_key, 'closed')
        redis.call('DEL', failures_key)
        redis.call('DEL', successes_key)
    end
elseif state == 'closed' then
    -- Reset failure count on success
    redis.call('DEL', failures_key)
end

return 1
`)

// RecordSuccess records a successful request.
func (r *RedisCircuitBreaker) RecordSuccess(ctx context.Context, subscriptionID string) error {
	windowMs := r.config.Window.Milliseconds()

	_, err := recordSuccessScript.Run(ctx, r.client,
		[]string{
			r.keyState(subscriptionID),
			r.keySuccesses(subscriptionID),
			r.keyFailures(subscriptionID),
		},
		r.config.SuccessThreshold, windowMs,
	).Result()

	if err != nil {
		r.logger.Warn("redis circuit breaker record success failed",
			"error", err,
			"subscription_id", subscriptionID,
		)
		// Fallback circuit breaker handles this internally via Execute
		return nil
	}

	return nil
}

// recordFailureScript handles failure recording and state transitions.
var recordFailureScript = redis.NewScript(`
local state_key = KEYS[1]
local failures_key = KEYS[2]
local opened_at_key = KEYS[3]
local successes_key = KEYS[4]
local failure_threshold = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

local state = redis.call('GET', state_key)
if not state then
    state = 'closed'
end

if state == 'closed' then
    local failures = redis.call('INCR', failures_key)
    redis.call('PEXPIRE', failures_key, window_ms)
    
    if failures >= failure_threshold then
        -- Open the circuit
        redis.call('SET', state_key, 'open')
        redis.call('SET', opened_at_key, now)
        redis.call('PEXPIRE', opened_at_key, window_ms * 2)
    end
elseif state == 'half-open' then
    -- Any failure in half-open reopens the circuit
    redis.call('SET', state_key, 'open')
    redis.call('SET', opened_at_key, now)
    redis.call('PEXPIRE', opened_at_key, window_ms * 2)
    redis.call('DEL', successes_key)
end

return 1
`)

// RecordFailure records a failed request.
func (r *RedisCircuitBreaker) RecordFailure(ctx context.Context, subscriptionID string) error {
	now := time.Now().UnixMilli()
	windowMs := r.config.Window.Milliseconds()

	_, err := recordFailureScript.Run(ctx, r.client,
		[]string{
			r.keyState(subscriptionID),
			r.keyFailures(subscriptionID),
			r.keyOpenedAt(subscriptionID),
			r.keySuccesses(subscriptionID),
		},
		r.config.FailureThreshold, windowMs, now,
	).Result()

	if err != nil {
		r.logger.Warn("redis circuit breaker record failure failed",
			"error", err,
			"subscription_id", subscriptionID,
		)
		// Fallback circuit breaker handles this internally via Execute
		return nil
	}

	return nil
}

// State returns the current state of the circuit breaker.
func (r *RedisCircuitBreaker) State(ctx context.Context, subscriptionID string) (CircuitState, error) {
	state, err := r.client.Get(ctx, r.keyState(subscriptionID)).Result()
	if err == redis.Nil {
		return CircuitStateClosed, nil
	}
	if err != nil {
		r.logger.Warn("redis circuit breaker state failed, using fallback",
			"error", err,
			"subscription_id", subscriptionID,
		)
		fallbackState := r.fallback.State(subscriptionID)
		return r.convertFallbackState(fallbackState), nil
	}

	return CircuitState(state), nil
}

func (r *RedisCircuitBreaker) convertFallbackState(state CircuitBreakerState) CircuitState {
	switch state {
	case CircuitBreakerStateClosed:
		return CircuitStateClosed
	case CircuitBreakerStateOpen:
		return CircuitStateOpen
	case CircuitBreakerStateHalfOpen:
		return CircuitStateHalfOpen
	default:
		return CircuitStateClosed
	}
}

// GetFailureCount returns the current failure count for a subscription.
func (r *RedisCircuitBreaker) GetFailureCount(ctx context.Context, subscriptionID string) (int, error) {
	count, err := r.client.Get(ctx, r.keyFailures(subscriptionID)).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	n, _ := strconv.Atoi(count)
	return n, nil
}

// Close closes the Redis client connection.
func (r *RedisCircuitBreaker) Close() error {
	return r.client.Close()
}
