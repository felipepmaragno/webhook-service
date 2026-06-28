package repository

import (
	"context"
	"errors"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
)

var ErrClaimLost = errors.New("retry claim lost")

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

type EventReader interface {
	GetByID(ctx context.Context, id string) (*domain.Event, error)
}

type AttemptReader interface {
	GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error)
}

type DeliveryListReader interface {
	GetDeliveriesByEventID(ctx context.Context, eventID string) ([]*domain.Delivery, error)
}

type APIEventRepository interface {
	EventReader
	AttemptReader
	DeliveryListReader
}

type DeliveryRuntimeRepository interface {
	InitializeEventDeliveries(ctx context.Context, event *domain.Event, subscriptions []*domain.Subscription) ([]*domain.Delivery, error)
	GetDeliveriesByEventID(ctx context.Context, eventID string) ([]*domain.Delivery, error)
	ClaimEventDeliveries(ctx context.Context, eventIDs []string, owner string, leaseDuration time.Duration, limit int) ([]ClaimedDelivery, error)
	PersistClaimedDeliveryOutcome(ctx context.Context, delivery *domain.Delivery, attempts []*domain.DeliveryAttempt) error
}

type RetryDeliveryRepository interface {
	RetryBacklogReader
	ClaimDeliveries(ctx context.Context, owner string, leaseDuration time.Duration, limit int) ([]ClaimedDelivery, error)
}
