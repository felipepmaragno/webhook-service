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
