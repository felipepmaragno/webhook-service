package repository

import (
	"context"
	"errors"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
)

var ErrClaimLost = errors.New("retry claim lost")

type ClaimedEvent struct {
	Event     *domain.Event
	Reclaimed bool
}

type ClaimedDelivery struct {
	Delivery  *domain.Delivery
	Reclaimed bool
}

type RetryBacklogStats struct {
	DueCount               int64
	OldestDueAt            *time.Time
	ExpiredProcessingCount int64
	LeasedCount            int64
}

type RetryBacklogReader interface {
	GetRetryBacklogStats(ctx context.Context) (RetryBacklogStats, error)
}

// EventOutcome groups one event state transition with the delivery attempts
// produced while computing that outcome. Repositories must persist the group
// atomically so state and history cannot diverge.
type EventOutcome struct {
	Event    *domain.Event
	Attempts []*domain.DeliveryAttempt
}

type EventRepository interface {
	Create(ctx context.Context, event *domain.Event) error
	CreateBatch(ctx context.Context, events []*domain.Event) error
	GetByID(ctx context.Context, id string) (*domain.Event, error)
	InitializeEventDeliveries(ctx context.Context, event *domain.Event, subscriptions []*domain.Subscription) ([]*domain.Delivery, error)
	GetDeliveriesByEventID(ctx context.Context, eventID string) ([]*domain.Delivery, error)
	GetDeliveryByID(ctx context.Context, id string) (*domain.Delivery, error)
	ClaimDeliveries(ctx context.Context, owner string, leaseDuration time.Duration, limit int) ([]ClaimedDelivery, error)
	PersistDeliveryOutcome(ctx context.Context, delivery *domain.Delivery, attempts []*domain.DeliveryAttempt) error
	PersistClaimedDeliveryOutcome(ctx context.Context, delivery *domain.Delivery, attempts []*domain.DeliveryAttempt) error
	ClaimRetryEvents(ctx context.Context, owner string, leaseDuration time.Duration, limit int) ([]ClaimedEvent, error)
	UpdateStatus(ctx context.Context, event *domain.Event) error
	UpdateStatusBatch(ctx context.Context, events []*domain.Event) error
	RecordAttempt(ctx context.Context, attempt *domain.DeliveryAttempt) error
	RecordAttemptBatch(ctx context.Context, attempts []*domain.DeliveryAttempt) error
	PersistNewOutcomes(ctx context.Context, outcomes []EventOutcome) error
	PersistClaimedOutcomes(ctx context.Context, outcomes []EventOutcome) error
	GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error)
	Shutdown(ctx context.Context) error
}

type SubscriptionRepository interface {
	Create(ctx context.Context, sub *domain.Subscription) error
	GetByID(ctx context.Context, id string) (*domain.Subscription, error)
	GetActive(ctx context.Context) ([]*domain.Subscription, error)
	GetByEventType(ctx context.Context, eventType string) ([]*domain.Subscription, error)
	GetByEventTypes(ctx context.Context, eventTypes []string) (map[string][]*domain.Subscription, error)
	Delete(ctx context.Context, id string) error
}
