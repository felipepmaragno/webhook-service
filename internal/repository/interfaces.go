package repository

import (
	"context"

	"github.com/felipemaragno/dispatch/internal/domain"
)

type EventRepository interface {
	Create(ctx context.Context, event *domain.Event) error
	GetByID(ctx context.Context, id string) (*domain.Event, error)
	GetPendingEvents(ctx context.Context, limit int) ([]*domain.Event, error)
	UpdateStatus(ctx context.Context, event *domain.Event) error
	RecordAttempt(ctx context.Context, attempt *domain.DeliveryAttempt) error
	GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error)
}

type SubscriptionRepository interface {
	Create(ctx context.Context, sub *domain.Subscription) error
	GetByID(ctx context.Context, id string) (*domain.Subscription, error)
	GetActive(ctx context.Context) ([]*domain.Subscription, error)
	GetByEventType(ctx context.Context, eventType string) ([]*domain.Subscription, error)
	Delete(ctx context.Context, id string) error
}
