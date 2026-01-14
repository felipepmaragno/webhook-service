// Package domain contains the core business entities and logic.
// These types are independent of infrastructure concerns like databases or HTTP.
package domain

import (
	"encoding/json"
	"time"
)

// EventStatus represents the lifecycle state of an event.
//
// State machine:
//
//	[pending] ---(worker picks up)---> [processing]
//	[processing] ---(success)---> [delivered]
//	[processing] ---(failure, can retry)---> [retrying]
//	[processing] ---(rate limited/circuit open)---> [throttled]
//	[retrying] ---(next_attempt_at reached)---> [processing]
//	[throttled] ---(next_attempt_at reached)---> [processing]
//	[processing] ---(failure, no retries left)---> [failed]
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
	ID            int       `json:"id"`
	EventID       string    `json:"event_id"`
	AttemptNumber int       `json:"attempt_number"`
	StatusCode    *int      `json:"status_code,omitempty"`
	ResponseBody  *string   `json:"response_body,omitempty"`
	Error         *string   `json:"error,omitempty"`
	DurationMs    int       `json:"duration_ms"`
	CreatedAt     time.Time `json:"created_at"`
}

// CanRetry returns true if the event has remaining retry attempts.
func (e *Event) CanRetry() bool {
	return e.Attempts < e.MaxAttempts
}

func (e *Event) MarkAsProcessing() {
	e.Status = EventStatusProcessing
	e.UpdatedAt = time.Now()
}

func (e *Event) MarkAsDelivered(deliveredAt time.Time) {
	e.Status = EventStatusDelivered
	e.DeliveredAt = &deliveredAt
	e.UpdatedAt = deliveredAt
}

// MarkAsRetrying schedules the event for retry with exponential backoff.
// Increments the attempt counter.
func (e *Event) MarkAsRetrying(nextAttempt time.Time, lastError string) {
	e.Status = EventStatusRetrying
	e.Attempts++
	e.NextAttemptAt = &nextAttempt
	e.LastError = &lastError
	e.UpdatedAt = time.Now()
}

// MarkAsFailed marks the event as permanently failed.
// Called when all retry attempts are exhausted.
func (e *Event) MarkAsFailed(lastError string) {
	e.Status = EventStatusFailed
	e.LastError = &lastError
	e.NextAttemptAt = nil
	e.UpdatedAt = time.Now()
}

// MarkAsThrottled marks the event as throttled (rate limited or circuit breaker open).
// Does NOT increment attempts - throttling is internal backpressure, not a delivery failure.
func (e *Event) MarkAsThrottled(nextAttempt time.Time) {
	e.Status = EventStatusThrottled
	e.NextAttemptAt = &nextAttempt
	e.UpdatedAt = time.Now()
}
