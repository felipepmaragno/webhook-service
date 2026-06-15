package domain

import "testing"

func TestSubscription_MatchesEventType(t *testing.T) {
	tests := []struct {
		name       string
		eventTypes []string
		eventType  string
		want       bool
	}{
		{"exact match", []string{"order.created"}, "order.created", true},
		{"no match", []string{"order.created"}, "order.updated", false},
		{"wildcard all", []string{"*"}, "anything.here", true},
		{"wildcard prefix", []string{"order.*"}, "order.created", true},
		{"wildcard prefix no match", []string{"order.*"}, "payment.created", false},
		{"multiple types match first", []string{"order.created", "order.updated"}, "order.created", true},
		{"multiple types match second", []string{"order.created", "order.updated"}, "order.updated", true},
		{"multiple types no match", []string{"order.created", "order.updated"}, "order.deleted", false},
		{"empty event types", []string{}, "order.created", false},
		{"wildcard with dot", []string{"order.item.*"}, "order.item.added", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Subscription{EventTypes: tt.eventTypes}
			if got := s.MatchesEventType(tt.eventType); got != tt.want {
				t.Errorf("MatchesEventType(%q) = %v, want %v", tt.eventType, got, tt.want)
			}
		})
	}
}

func TestSubscription_EffectiveRatePolicy(t *testing.T) {
	t.Run("uses explicit policy", func(t *testing.T) {
		sub := Subscription{RateLimit: 25, BurstSize: 7}
		policy := sub.EffectiveRatePolicy()
		if policy.RequestsPerSecond != 25 {
			t.Errorf("RequestsPerSecond = %d, want 25", policy.RequestsPerSecond)
		}
		if policy.BurstSize != 7 {
			t.Errorf("BurstSize = %d, want 7", policy.BurstSize)
		}
	})

	t.Run("defaults missing policy", func(t *testing.T) {
		sub := Subscription{}
		policy := sub.EffectiveRatePolicy()
		if policy.RequestsPerSecond != DefaultSubscriptionRateLimit {
			t.Errorf("RequestsPerSecond = %d, want %d", policy.RequestsPerSecond, DefaultSubscriptionRateLimit)
		}
		if policy.BurstSize != DefaultSubscriptionBurstSize {
			t.Errorf("BurstSize = %d, want %d", policy.BurstSize, DefaultSubscriptionBurstSize)
		}
	})
}

func TestSubscription_EffectiveConcurrencyLimit(t *testing.T) {
	explicit := Subscription{ConcurrencyLimit: 9}
	if got := explicit.EffectiveConcurrencyLimit(); got != 9 {
		t.Errorf("EffectiveConcurrencyLimit = %d, want 9", got)
	}
	missing := Subscription{}
	if got := missing.EffectiveConcurrencyLimit(); got != DefaultSubscriptionConcurrencyLimit {
		t.Errorf("EffectiveConcurrencyLimit = %d, want %d", got, DefaultSubscriptionConcurrencyLimit)
	}
}
