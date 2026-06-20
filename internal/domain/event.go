// Package domain contains the core business entities and logic.
// These types are independent of infrastructure concerns like databases or HTTP.
package domain

import (
	"encoding/json"
	"time"
)

// EventStatus is the aggregate projection of the event's delivery rows.
type EventStatus string

const (
	EventStatusPending    EventStatus = "pending"
	EventStatusProcessing EventStatus = "processing"
	EventStatusDelivered  EventStatus = "delivered"
	EventStatusRetrying   EventStatus = "retrying"
	EventStatusThrottled  EventStatus = "throttled"
	EventStatusFailed     EventStatus = "failed"
)

// Event represents a webhook event to be delivered.
// Events are created via the API and processed by workers.
type Event struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Source        string          `json:"source"`
	Data          json.RawMessage `json:"data"`
	Status        EventStatus     `json:"status"`
	Attempts      int             `json:"attempts"`
	MaxAttempts   int             `json:"max_attempts"`
	NextAttemptAt *time.Time      `json:"next_attempt_at,omitempty"`
	LastError     *string         `json:"last_error,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	DeliveredAt   *time.Time      `json:"delivered_at,omitempty"`
}

// DeliveryAttempt records a single webhook delivery attempt.
// Used for debugging and auditing delivery history.
type DeliveryAttempt struct {
	ID             int       `json:"id"`
	EventID        string    `json:"event_id"`
	DeliveryID     string    `json:"delivery_id"`
	SubscriptionID string    `json:"subscription_id"`
	AttemptNumber  int       `json:"attempt_number"`
	Generation     int       `json:"generation"`
	StatusCode     *int      `json:"status_code,omitempty"`
	ResponseBody   *string   `json:"response_body,omitempty"`
	Error          *string   `json:"error,omitempty"`
	DurationMs     int       `json:"duration_ms"`
	CreatedAt      time.Time `json:"created_at"`
}
