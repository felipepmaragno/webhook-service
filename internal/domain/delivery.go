package domain

import (
	"encoding/json"
	"time"
)

type DeliveryStatus string

const (
	DeliveryStatusPending    DeliveryStatus = "pending"
	DeliveryStatusProcessing DeliveryStatus = "processing"
	DeliveryStatusDelivered  DeliveryStatus = "delivered"
	DeliveryStatusRetrying   DeliveryStatus = "retrying"
	DeliveryStatusThrottled  DeliveryStatus = "throttled"
	DeliveryStatusFailed     DeliveryStatus = "failed"
)

type Delivery struct {
	ID                 string          `json:"id"`
	EventID            string          `json:"event_id"`
	SubscriptionID     string          `json:"subscription_id"`
	EventType          string          `json:"event_type"`
	Source             string          `json:"source"`
	Data               json.RawMessage `json:"data,omitempty"`
	SubscriptionURL    string          `json:"subscription_url"`
	SubscriptionSecret *string         `json:"-"`
	RateLimit          int             `json:"rate_limit"`
	BurstSize          int             `json:"burst_size"`
	ConcurrencyLimit   int             `json:"concurrency_limit"`
	Status             DeliveryStatus  `json:"status"`
	Attempts           int             `json:"attempts"`
	MaxAttempts        int             `json:"max_attempts"`
	NextAttemptAt      *time.Time      `json:"next_attempt_at,omitempty"`
	LastError          *string         `json:"last_error,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	DeliveredAt        *time.Time      `json:"delivered_at,omitempty"`
	ProcessingOwner    *string         `json:"processing_owner,omitempty"`
	ProcessingDeadline *time.Time      `json:"processing_deadline,omitempty"`
}

type EventProjection struct {
	Status        EventStatus
	Attempts      int
	NextAttemptAt *time.Time
	LastError     *string
	DeliveredAt   *time.Time
}

func NewDelivery(event *Event, sub *Subscription) *Delivery {
	policy := sub.EffectiveRatePolicy()
	now := time.Now()
	return &Delivery{
		ID:                 DeliveryID(event.ID, sub.ID),
		EventID:            event.ID,
		SubscriptionID:     sub.ID,
		EventType:          event.Type,
		Source:             event.Source,
		Data:               event.Data,
		SubscriptionURL:    sub.URL,
		SubscriptionSecret: sub.Secret,
		RateLimit:          policy.RequestsPerSecond,
		BurstSize:          policy.BurstSize,
		ConcurrencyLimit:   sub.EffectiveConcurrencyLimit(),
		Status:             DeliveryStatusPending,
		Attempts:           event.Attempts,
		MaxAttempts:        event.MaxAttempts,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func DeliveryID(eventID, subscriptionID string) string {
	return eventID + ":" + subscriptionID
}

func (d *Delivery) MarkAsProcessing(owner string, deadline time.Time) {
	d.Status = DeliveryStatusProcessing
	d.ProcessingOwner = &owner
	d.ProcessingDeadline = &deadline
	d.UpdatedAt = time.Now()
}

func (d *Delivery) MarkAsDelivered(deliveredAt time.Time) {
	d.Status = DeliveryStatusDelivered
	d.DeliveredAt = &deliveredAt
	d.NextAttemptAt = nil
	d.LastError = nil
	d.UpdatedAt = deliveredAt
}

func (d *Delivery) MarkAsRetrying(nextAttempt time.Time, lastError string) {
	d.Status = DeliveryStatusRetrying
	d.Attempts++
	d.NextAttemptAt = &nextAttempt
	d.LastError = &lastError
	d.UpdatedAt = time.Now()
}

func (d *Delivery) MarkAsThrottled(nextAttempt time.Time, lastError string) {
	d.Status = DeliveryStatusThrottled
	d.NextAttemptAt = &nextAttempt
	d.LastError = &lastError
	d.UpdatedAt = time.Now()
}

func (d *Delivery) MarkAsFailed(lastError string) {
	d.Status = DeliveryStatusFailed
	d.LastError = &lastError
	d.NextAttemptAt = nil
	d.UpdatedAt = time.Now()
}

func ProjectEventFromDeliveries(deliveries []*Delivery) EventProjection {
	if len(deliveries) == 0 {
		now := time.Now()
		return EventProjection{Status: EventStatusDelivered, DeliveredAt: &now}
	}

	projection := EventProjection{Status: EventStatusDelivered}
	allDelivered := true
	var deliveredAt *time.Time

	for _, delivery := range deliveries {
		if delivery == nil {
			continue
		}
		if delivery.Attempts > projection.Attempts {
			projection.Attempts = delivery.Attempts
		}
		if sooner(projection.NextAttemptAt, delivery.NextAttemptAt) {
			projection.NextAttemptAt = delivery.NextAttemptAt
		}
		if delivery.LastError != nil {
			projection.LastError = delivery.LastError
		}
		if delivery.DeliveredAt != nil && (deliveredAt == nil || delivery.DeliveredAt.After(*deliveredAt)) {
			deliveredAt = delivery.DeliveredAt
		}

		switch delivery.Status {
		case DeliveryStatusProcessing:
			projection.Status = EventStatusProcessing
			allDelivered = false
		case DeliveryStatusRetrying:
			if projection.Status != EventStatusProcessing {
				projection.Status = EventStatusRetrying
			}
			allDelivered = false
		case DeliveryStatusThrottled:
			if projection.Status != EventStatusProcessing && projection.Status != EventStatusRetrying {
				projection.Status = EventStatusThrottled
			}
			allDelivered = false
		case DeliveryStatusPending:
			if projection.Status != EventStatusProcessing && projection.Status != EventStatusRetrying && projection.Status != EventStatusThrottled {
				projection.Status = EventStatusPending
			}
			allDelivered = false
		case DeliveryStatusFailed:
			if projection.Status == EventStatusDelivered {
				projection.Status = EventStatusFailed
			}
			allDelivered = false
		case DeliveryStatusDelivered:
		}
	}

	if allDelivered {
		projection.Status = EventStatusDelivered
		projection.DeliveredAt = deliveredAt
	}
	return projection
}

func sooner(current, candidate *time.Time) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	return candidate.Before(*current)
}
