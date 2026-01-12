package domain

import (
	"encoding/json"
	"time"
)

type EventStatus string

const (
	EventStatusPending    EventStatus = "pending"
	EventStatusProcessing EventStatus = "processing"
	EventStatusDelivered  EventStatus = "delivered"
	EventStatusRetrying   EventStatus = "retrying"
	EventStatusFailed     EventStatus = "failed"
)

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

func (e *Event) MarkAsRetrying(nextAttempt time.Time, lastError string) {
	e.Status = EventStatusRetrying
	e.Attempts++
	e.NextAttemptAt = &nextAttempt
	e.LastError = &lastError
	e.UpdatedAt = time.Now()
}

func (e *Event) MarkAsFailed(lastError string) {
	e.Status = EventStatusFailed
	e.Attempts++
	e.LastError = &lastError
	e.NextAttemptAt = nil
	e.UpdatedAt = time.Now()
}

func (e *Event) RescheduleWithoutAttemptIncrement(nextAttempt time.Time) {
	e.Status = EventStatusRetrying
	e.NextAttemptAt = &nextAttempt
	e.UpdatedAt = time.Now()
}
