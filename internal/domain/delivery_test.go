package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDeliveryID(t *testing.T) {
	if got := DeliveryID("evt-1", "sub-1"); got != "evt-1:sub-1" {
		t.Errorf("DeliveryID = %q, want %q", got, "evt-1:sub-1")
	}
}

func TestNewDeliverySnapshotsSubscription(t *testing.T) {
	secret := "secret"
	event := &Event{
		ID:          "evt-1",
		Type:        "order.created",
		Source:      "billing",
		Data:        json.RawMessage(`{"id":1}`),
		MaxAttempts: 5,
	}
	sub := &Subscription{
		ID:               "sub-1",
		URL:              "https://example.com/webhook",
		Secret:           &secret,
		RateLimit:        25,
		BurstSize:        7,
		ConcurrencyLimit: 3,
	}

	delivery := NewDelivery(event, sub)

	if delivery.ID != "evt-1:sub-1" {
		t.Errorf("ID = %q", delivery.ID)
	}
	if delivery.SubscriptionURL != sub.URL {
		t.Errorf("SubscriptionURL = %q, want %q", delivery.SubscriptionURL, sub.URL)
	}
	if delivery.SubscriptionSecret == nil || *delivery.SubscriptionSecret != secret {
		t.Errorf("SubscriptionSecret = %v, want %q", delivery.SubscriptionSecret, secret)
	}
	if delivery.RateLimit != 25 || delivery.BurstSize != 7 || delivery.ConcurrencyLimit != 3 {
		t.Errorf("unexpected policy snapshot: rate=%d burst=%d concurrency=%d", delivery.RateLimit, delivery.BurstSize, delivery.ConcurrencyLimit)
	}
	if delivery.Status != DeliveryStatusPending {
		t.Errorf("Status = %s, want pending", delivery.Status)
	}
	if delivery.Generation != 1 {
		t.Errorf("Generation = %d, want 1", delivery.Generation)
	}
}

func TestDeliveryScheduleReplay(t *testing.T) {
	scheduledAt := time.Now().UTC().Truncate(time.Millisecond)
	lastError := "terminal"
	deliveredAt := scheduledAt.Add(-time.Minute)
	owner := "old-worker"
	deadline := scheduledAt.Add(time.Minute)
	delivery := &Delivery{
		Status: DeliveryStatusFailed, Generation: 1, Attempts: 5,
		LastError: &lastError, DeliveredAt: &deliveredAt,
		ProcessingOwner: &owner, ProcessingDeadline: &deadline,
	}

	if err := delivery.ScheduleReplay(scheduledAt); err != nil {
		t.Fatalf("ScheduleReplay: %v", err)
	}
	if delivery.Status != DeliveryStatusRetrying || delivery.Generation != 2 || delivery.Attempts != 0 {
		t.Fatalf("unexpected replay state: %+v", delivery)
	}
	if delivery.NextAttemptAt == nil || !delivery.NextAttemptAt.Equal(scheduledAt) || !delivery.UpdatedAt.Equal(scheduledAt) {
		t.Fatalf("unexpected replay schedule: %+v", delivery)
	}
	if delivery.LastError != nil || delivery.DeliveredAt != nil || delivery.ProcessingOwner != nil || delivery.ProcessingDeadline != nil {
		t.Fatalf("replay fields were not cleared: %+v", delivery)
	}
	if err := delivery.ScheduleReplay(scheduledAt); err != ErrReplayNotEligible {
		t.Fatalf("second ScheduleReplay error = %v, want ErrReplayNotEligible", err)
	}
}

func TestDeliveryTransitions(t *testing.T) {
	delivery := &Delivery{Status: DeliveryStatusPending}

	deadline := time.Now().Add(time.Minute)
	delivery.MarkAsProcessing("worker-1", deadline)
	if delivery.Status != DeliveryStatusProcessing {
		t.Errorf("Status = %s, want processing", delivery.Status)
	}
	if delivery.ProcessingOwner == nil || *delivery.ProcessingOwner != "worker-1" {
		t.Errorf("ProcessingOwner = %v", delivery.ProcessingOwner)
	}

	next := time.Now().Add(time.Second)
	delivery.MarkAsThrottled(next, "rate limited")
	if delivery.Status != DeliveryStatusThrottled {
		t.Errorf("Status = %s, want throttled", delivery.Status)
	}
	if delivery.Attempts != 0 {
		t.Errorf("throttling should not increment attempts, got %d", delivery.Attempts)
	}

	delivery.MarkAsRetrying(next, "temporary failure")
	if delivery.Status != DeliveryStatusRetrying {
		t.Errorf("Status = %s, want retrying", delivery.Status)
	}
	if delivery.Attempts != 1 {
		t.Errorf("retrying should increment attempts, got %d", delivery.Attempts)
	}

	deliveredAt := time.Now()
	delivery.MarkAsDelivered(deliveredAt)
	if delivery.Status != DeliveryStatusDelivered {
		t.Errorf("Status = %s, want delivered", delivery.Status)
	}
	if delivery.DeliveredAt == nil || !delivery.DeliveredAt.Equal(deliveredAt) {
		t.Errorf("DeliveredAt = %v, want %v", delivery.DeliveredAt, deliveredAt)
	}
}

func TestProjectEventFromDeliveries(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Minute)
	errTemporary := "temporary"
	errFailed := "failed"

	tests := []struct {
		name       string
		deliveries []*Delivery
		want       EventStatus
	}{
		{
			name:       "zero deliveries is delivered",
			deliveries: nil,
			want:       EventStatusDelivered,
		},
		{
			name: "all delivered",
			deliveries: []*Delivery{
				{Status: DeliveryStatusDelivered, DeliveredAt: &now},
				{Status: DeliveryStatusDelivered, DeliveredAt: &later},
			},
			want: EventStatusDelivered,
		},
		{
			name: "processing dominates active work",
			deliveries: []*Delivery{
				{Status: DeliveryStatusFailed, LastError: &errFailed},
				{Status: DeliveryStatusProcessing},
			},
			want: EventStatusProcessing,
		},
		{
			name: "retrying dominates throttled and failed",
			deliveries: []*Delivery{
				{Status: DeliveryStatusFailed, LastError: &errFailed},
				{Status: DeliveryStatusThrottled, NextAttemptAt: &later},
				{Status: DeliveryStatusRetrying, LastError: &errTemporary, NextAttemptAt: &now},
			},
			want: EventStatusRetrying,
		},
		{
			name: "throttled dominates pending and failed",
			deliveries: []*Delivery{
				{Status: DeliveryStatusFailed, LastError: &errFailed},
				{Status: DeliveryStatusPending},
				{Status: DeliveryStatusThrottled, NextAttemptAt: &later},
			},
			want: EventStatusThrottled,
		},
		{
			name: "failed when no active work remains",
			deliveries: []*Delivery{
				{Status: DeliveryStatusDelivered, DeliveredAt: &now},
				{Status: DeliveryStatusFailed, LastError: &errFailed},
			},
			want: EventStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projection := ProjectEventFromDeliveries(tt.deliveries)
			if projection.Status != tt.want {
				t.Errorf("Status = %s, want %s", projection.Status, tt.want)
			}
		})
	}
}
