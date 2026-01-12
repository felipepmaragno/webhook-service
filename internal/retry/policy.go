package retry

import (
	"math"
	"math/rand"
	"time"
)

type Policy struct {
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
	Jitter          float64
	MaxAttempts     int
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

func (p Policy) NextAttemptTime(now time.Time, attempt int) time.Time {
	return now.Add(p.CalculateDelay(attempt))
}
