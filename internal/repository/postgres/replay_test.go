package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func TestEventRepository_ReplayFailedDelivery(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	eventRepo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)
	secret := "frozen-secret"
	sub := makeSub("sub-replay", []string{"order.created"})
	sub.Secret = &secret
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	event := makeEvent("evt-replay")
	deliveries, err := eventRepo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub})
	if err != nil {
		t.Fatalf("initialize deliveries: %v", err)
	}
	delivery := deliveries[0]
	delivery.MarkAsFailed("attempts exhausted")
	statusCode := 500
	responseBody := "receiver failed"
	persistClaimedOutcomeForTest(t, ctx, pool, eventRepo, delivery, []*domain.DeliveryAttempt{{
		AttemptNumber: 5,
		StatusCode:    &statusCode,
		ResponseBody:  &responseBody,
	}})

	scheduledAt := time.Now().UTC().Truncate(time.Millisecond)
	replayed, err := eventRepo.ReplayFailedDelivery(ctx, delivery.ID, scheduledAt)
	if err != nil {
		t.Fatalf("ReplayFailedDelivery: %v", err)
	}
	if replayed.Generation != 2 || replayed.Status != domain.DeliveryStatusRetrying || replayed.Attempts != 0 {
		t.Fatalf("unexpected replay state: %+v", replayed)
	}
	if replayed.NextAttemptAt == nil || !replayed.NextAttemptAt.Equal(scheduledAt) {
		t.Fatalf("next attempt = %v, want %v", replayed.NextAttemptAt, scheduledAt)
	}
	if replayed.LastError != nil || replayed.DeliveredAt != nil || replayed.ProcessingOwner != nil || replayed.ProcessingDeadline != nil {
		t.Fatalf("replay did not clear terminal/lease fields: %+v", replayed)
	}
	if replayed.SubscriptionSecret == nil || *replayed.SubscriptionSecret != secret || replayed.SubscriptionURL != sub.URL {
		t.Fatalf("replay changed frozen destination: %+v", replayed)
	}

	attempts, err := eventRepo.GetAttemptsByEventID(ctx, event.ID)
	if err != nil {
		t.Fatalf("get attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Generation != 1 || attempts[0].AttemptNumber != 5 {
		t.Fatalf("historical attempt changed: %+v", attempts)
	}
	projected, err := eventRepo.GetByID(ctx, event.ID)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if projected.Status != domain.EventStatusRetrying || projected.Attempts != 0 {
		t.Fatalf("unexpected replay projection: %+v", projected)
	}

	if _, err := eventRepo.ReplayFailedDelivery(ctx, delivery.ID, scheduledAt); !errors.Is(err, domain.ErrReplayNotEligible) {
		t.Fatalf("repeated replay error = %v, want ErrReplayNotEligible", err)
	}
	if _, err := eventRepo.ReplayFailedDelivery(ctx, "missing", scheduledAt); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing replay error = %v, want ErrNotFound", err)
	}
}

func TestEventRepository_ReplayFailedDeliveryRejectsNonFailedStates(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	eventRepo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)
	sub := makeSub("sub-replay-states", []string{"order.created"})
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	statuses := []domain.DeliveryStatus{
		domain.DeliveryStatusPending,
		domain.DeliveryStatusProcessing,
		domain.DeliveryStatusDelivered,
		domain.DeliveryStatusRetrying,
		domain.DeliveryStatusThrottled,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			event := makeEvent("evt-replay-state-" + string(status))
			deliveries, err := eventRepo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub})
			if err != nil {
				t.Fatalf("initialize: %v", err)
			}
			delivery := deliveries[0]
			delivery.Status = status
			delivery.UpdatedAt = time.Now()
			if status == domain.DeliveryStatusProcessing {
				owner := "worker"
				deadline := time.Now().Add(time.Minute)
				delivery.ProcessingOwner = &owner
				delivery.ProcessingDeadline = &deadline
			}
			persistClaimedOutcomeForTest(t, ctx, pool, eventRepo, delivery, nil)

			if _, err := eventRepo.ReplayFailedDelivery(ctx, delivery.ID, time.Now()); !errors.Is(err, domain.ErrReplayNotEligible) {
				t.Fatalf("replay error = %v, want ErrReplayNotEligible", err)
			}
		})
	}
}

func TestEventRepository_ReplayFailedDeliveryConcurrent(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	eventRepo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)
	sub := makeSub("sub-replay-concurrent", []string{"order.created"})
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	event := makeEvent("evt-replay-concurrent")
	deliveries, err := eventRepo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	delivery := deliveries[0]
	delivery.MarkAsFailed("terminal")
	persistClaimedOutcomeForTest(t, ctx, pool, eventRepo, delivery, nil)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := eventRepo.ReplayFailedDelivery(ctx, delivery.ID, time.Now().UTC())
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var successes, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrReplayNotEligible):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent replay error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1 each", successes, conflicts)
	}
}
