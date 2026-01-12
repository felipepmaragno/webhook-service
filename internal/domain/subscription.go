package domain

import "time"

type Subscription struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	EventTypes []string  `json:"event_types"`
	Secret     *string   `json:"secret,omitempty"`
	RateLimit  int       `json:"rate_limit"`
	CreatedAt  time.Time `json:"created_at"`
	Active     bool      `json:"active"`
}

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
