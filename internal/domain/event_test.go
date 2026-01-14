package domain

import (
	"testing"
	"time"
)

func TestEvent_CanRetry(t *testing.T) {
	tests := []struct {
		name        string
		attempts    int
		maxAttempts int
		want        bool
	}{
		{"zero attempts", 0, 5, true},
		{"some attempts left", 3, 5, true},
		{"one attempt left", 4, 5, true},
		{"no attempts left", 5, 5, false},
		{"over max attempts", 6, 5, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Event{Attempts: tt.attempts, MaxAttempts: tt.maxAttempts}
			if got := e.CanRetry(); got != tt.want {
				t.Errorf("CanRetry() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvent_MarkAsDelivered(t *testing.T) {
	e := Event{Status: EventStatusProcessing}
	now := time.Now()

	e.MarkAsDelivered(now)

	if e.Status != EventStatusDelivered {
		t.Errorf("Status = %v, want %v", e.Status, EventStatusDelivered)
	}
	if e.DeliveredAt == nil || !e.DeliveredAt.Equal(now) {
		t.Errorf("DeliveredAt = %v, want %v", e.DeliveredAt, now)
	}
}

func TestEvent_MarkAsRetrying(t *testing.T) {
	e := Event{
		Status:      EventStatusProcessing,
		Attempts:    1,
		MaxAttempts: 5,
	}
	nextAttempt := time.Now().Add(time.Minute)
	errMsg := "connection refused"

	e.MarkAsRetrying(nextAttempt, errMsg)

	if e.Status != EventStatusRetrying {
		t.Errorf("Status = %v, want %v", e.Status, EventStatusRetrying)
	}
	if e.Attempts != 2 {
		t.Errorf("Attempts = %v, want 2", e.Attempts)
	}
	if e.NextAttemptAt == nil || !e.NextAttemptAt.Equal(nextAttempt) {
		t.Errorf("NextAttemptAt = %v, want %v", e.NextAttemptAt, nextAttempt)
	}
	if e.LastError == nil || *e.LastError != errMsg {
		t.Errorf("LastError = %v, want %v", e.LastError, errMsg)
	}
}

func TestEvent_MarkAsFailed(t *testing.T) {
	nextAttempt := time.Now().Add(time.Minute)
	e := Event{
		Status:        EventStatusRetrying,
		Attempts:      4,
		MaxAttempts:   5,
		NextAttemptAt: &nextAttempt,
	}
	errMsg := "max retries exceeded"

	e.MarkAsFailed(errMsg)

	if e.Status != EventStatusFailed {
		t.Errorf("Status = %v, want %v", e.Status, EventStatusFailed)
	}
	if e.Attempts != 4 {
		t.Errorf("Attempts = %v, want 4 (MarkAsFailed should not increment)", e.Attempts)
	}
	if e.NextAttemptAt != nil {
		t.Errorf("NextAttemptAt = %v, want nil", e.NextAttemptAt)
	}
	if e.LastError == nil || *e.LastError != errMsg {
		t.Errorf("LastError = %v, want %v", e.LastError, errMsg)
	}
}

func TestEvent_RescheduleWithoutAttemptIncrement(t *testing.T) {
	e := Event{
		Status:      EventStatusProcessing,
		Attempts:    2,
		MaxAttempts: 5,
	}
	nextAttempt := time.Now().Add(30 * time.Second)

	e.MarkAsThrottled(nextAttempt)

	if e.Status != EventStatusThrottled {
		t.Errorf("Status = %v, want %v", e.Status, EventStatusThrottled)
	}
	if e.Attempts != 2 {
		t.Errorf("Attempts = %v, want 2 (should not increment)", e.Attempts)
	}
	if e.NextAttemptAt == nil || !e.NextAttemptAt.Equal(nextAttempt) {
		t.Errorf("NextAttemptAt = %v, want %v", e.NextAttemptAt, nextAttempt)
	}
}
