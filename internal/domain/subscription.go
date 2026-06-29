package domain

import "time"

const (
	DefaultSubscriptionMaxDeliveryRate = 100
)

// Subscription defines a webhook destination.
// Subscriptions filter events by type and deliver to a configured URL.

type Subscription struct {
	ID              string    `json:"id"`
	URL             string    `json:"url"`
	EventTypes      []string  `json:"event_types"`      // Supports wildcards like "order.*"
	Secret          *string   `json:"secret,omitempty"` // For HMAC-SHA256 signatures
	MaxDeliveryRate int       `json:"max_delivery_rate"`
	CreatedAt       time.Time `json:"created_at"`
	Active          bool      `json:"active"`
}

type RatePolicy struct {
	RequestsPerSecond int
}

func (s *Subscription) EffectiveRatePolicy() RatePolicy {
	rate := s.MaxDeliveryRate
	if rate <= 0 {
		rate = DefaultSubscriptionMaxDeliveryRate
	}
	return RatePolicy{RequestsPerSecond: rate}
}

// MatchesEventType checks if an event type matches this subscription's filters.
// Supports exact matches, "*" for all events, and prefix wildcards like "order.*".
func (s *Subscription) MatchesEventType(eventType string) bool {
	for _, t := range s.EventTypes {
		if t == "*" || t == eventType {
			return true
		}
		if matchWildcard(t, eventType) {
			return true
		}
	}
	return false
}

func matchWildcard(pattern, eventType string) bool {
	if len(pattern) == 0 {
		return len(eventType) == 0
	}

	if pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(eventType) >= len(prefix) && eventType[:len(prefix)] == prefix
	}

	return pattern == eventType
}
