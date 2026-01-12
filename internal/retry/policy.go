// Package retry implements exponential backoff with jitter for webhook retries.
//
// The retry policy uses the following formula:
//
//	delay = InitialInterval * (Multiplier ^ (attempt - 1)) + jitter
//
// Jitter adds randomness to prevent thundering herd when many events
// fail simultaneously and retry at the same time.
//
// Example with defaults (InitialInterval=1s, Multiplier=2, Jitter=10%):
//
//	Attempt 1: ~1s    (1s ± 0.1s)
//	Attempt 2: ~2s    (2s ± 0.2s)
//	Attempt 3: ~4s    (4s ± 0.4s)
//	Attempt 4: ~8s    (8s ± 0.8s)
//	Attempt 5: ~16s   (16s ± 1.6s)
package retry

import (
	"math"
	"math/rand"
	"time"
)

// Policy defines the retry behavior for failed webhook deliveries.
type Policy struct {
	InitialInterval time.Duration // Base delay for first retry
	MaxInterval     time.Duration // Maximum delay cap
	Multiplier      float64       // Exponential growth factor
	Jitter          float64       // Random variation (0.0-1.0)
	MaxAttempts     int           // Total attempts before giving up
}

func DefaultPolicy() Policy {
	return Policy{
		InitialInterval: 1 * time.Second,
		MaxInterval:     1 * time.Hour,
		Multiplier:      2.0,
		Jitter:          0.1,
		MaxAttempts:     5,
	}
}

// CalculateDelay returns the delay before the next retry attempt.
// The delay grows exponentially with jitter to prevent thundering herd.
func (p Policy) CalculateDelay(attempt int) time.Duration {
	delay := float64(p.InitialInterval) * math.Pow(p.Multiplier, float64(attempt-1))

	if delay > float64(p.MaxInterval) {
		delay = float64(p.MaxInterval)
	}

	if p.Jitter > 0 {
		jitterRange := delay * p.Jitter
		jitterOffset := (rand.Float64()*2 - 1) * jitterRange
		delay += jitterOffset
	}

	return time.Duration(delay)
}

// NextAttemptTime returns when the next retry should be attempted.
func (p Policy) NextAttemptTime(now time.Time, attempt int) time.Time {
	return now.Add(p.CalculateDelay(attempt))
}
