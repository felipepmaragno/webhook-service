package retry

import (
	"testing"
	"time"
)

func TestPolicy_CalculateDelay(t *testing.T) {
	policy := Policy{
		InitialInterval: 1 * time.Second,
		MaxInterval:     1 * time.Hour,
		Multiplier:      2.0,
		Jitter:          0.0, // disable jitter for deterministic tests
	}

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{10, 512 * time.Second},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := policy.CalculateDelay(tt.attempt)
			if got != tt.expected {
				t.Errorf("CalculateDelay(%d) = %v, want %v", tt.attempt, got, tt.expected)
			}
		})
	}
}

func TestPolicy_CalculateDelay_CapsAtMaxInterval(t *testing.T) {
	policy := Policy{
		InitialInterval: 1 * time.Second,
		MaxInterval:     30 * time.Second,
		Multiplier:      2.0,
		Jitter:          0.0,
	}

	// attempt 6 would be 32s, but should cap at 30s
	got := policy.CalculateDelay(6)
	if got != 30*time.Second {
		t.Errorf("CalculateDelay(6) = %v, want %v (capped)", got, 30*time.Second)
	}
}

func TestPolicy_CalculateDelay_WithJitter(t *testing.T) {
	policy := Policy{
		InitialInterval: 1 * time.Second,
		MaxInterval:     1 * time.Hour,
		Multiplier:      2.0,
		Jitter:          0.1, // 10% jitter
	}

	baseDelay := 1 * time.Second
	minExpected := time.Duration(float64(baseDelay) * 0.9)
	maxExpected := time.Duration(float64(baseDelay) * 1.1)

	// Run multiple times to verify jitter is applied
	for i := 0; i < 100; i++ {
		got := policy.CalculateDelay(1)
		if got < minExpected || got > maxExpected {
			t.Errorf("CalculateDelay(1) = %v, want between %v and %v", got, minExpected, maxExpected)
		}
	}
}

func TestPolicy_NextAttemptTime(t *testing.T) {
	policy := Policy{
		InitialInterval: 1 * time.Second,
		MaxInterval:     1 * time.Hour,
		Multiplier:      2.0,
		Jitter:          0.0,
	}

	now := time.Date(2026, 1, 11, 12, 0, 0, 0, time.UTC)
	got := policy.NextAttemptTime(now, 1)
	expected := now.Add(1 * time.Second)

	if !got.Equal(expected) {
		t.Errorf("NextAttemptTime() = %v, want %v", got, expected)
	}
}

func TestDefaultPolicy(t *testing.T) {
	policy := DefaultPolicy()

	if policy.InitialInterval != 1*time.Second {
		t.Errorf("InitialInterval = %v, want 1s", policy.InitialInterval)
	}
	if policy.MaxInterval != 1*time.Hour {
		t.Errorf("MaxInterval = %v, want 1h", policy.MaxInterval)
	}
	if policy.Multiplier != 2.0 {
		t.Errorf("Multiplier = %v, want 2.0", policy.Multiplier)
	}
	if policy.Jitter != 0.1 {
		t.Errorf("Jitter = %v, want 0.1", policy.Jitter)
	}
	if policy.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %v, want 5", policy.MaxAttempts)
	}
}
