package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func TestRetentionRepository_RedactAttemptBodies(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewRetentionRepository(pool)
	eventRepo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)

	event := makeEvent("evt-retention-bodies")
	sub := makeSub("sub-retention-bodies", []string{"order.created"})
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	deliveries, err := eventRepo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub})
	if err != nil {
		t.Fatalf("initialize delivery: %v", err)
	}
	delivery := deliveries[0]
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	for _, createdAt := range []time.Time{old, old.Add(time.Minute), recent} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO delivery_attempts (
				event_id, delivery_id, subscription_id, attempt_number, response_body, duration_ms, created_at
			) VALUES ($1, $2, $3, 1, 'body', 10, $4)
		`, event.ID, delivery.ID, sub.ID, createdAt); err != nil {
			t.Fatalf("insert attempt: %v", err)
		}
	}

	count, err := repo.RedactAttemptBodies(ctx, time.Now().Add(-24*time.Hour), 1)
	if err != nil {
		t.Fatalf("RedactAttemptBodies: %v", err)
	}
	if count != 1 {
		t.Fatalf("redacted = %d, want 1", count)
	}
	count, err = repo.RedactAttemptBodies(ctx, time.Now().Add(-24*time.Hour), 10)
	if err != nil || count != 1 {
		t.Fatalf("second redaction count=%d err=%v, want 1", count, err)
	}

	var retained, redacted int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE response_body IS NOT NULL),
		       COUNT(*) FILTER (WHERE response_body IS NULL)
		FROM delivery_attempts WHERE event_id = $1
	`, event.ID).Scan(&retained, &redacted); err != nil {
		t.Fatalf("inspect attempts: %v", err)
	}
	if retained != 1 || redacted != 2 {
		t.Fatalf("retained=%d redacted=%d, want 1 and 2", retained, redacted)
	}
}

func TestRetentionRepository_DeleteTerminalEventsPreservesActiveWork(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()
	ctx := context.Background()
	retentionRepo := NewRetentionRepository(pool)
	eventRepo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)
	sub := makeSub("sub-retention", []string{"order.created"})
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	old := time.Now().Add(-60 * 24 * time.Hour)

	seed := func(id string, status domain.DeliveryStatus) {
		t.Helper()
		event := makeEvent(id)
		deliveries, err := eventRepo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub})
		if err != nil {
			t.Fatalf("initialize %s: %v", id, err)
		}
		delivery := deliveries[0]
		delivery.Status = status
		delivery.UpdatedAt = old
		if status == domain.DeliveryStatusDelivered {
			delivery.DeliveredAt = &old
		}
		persistClaimedOutcomeForTest(t, ctx, pool, eventRepo, delivery, []*domain.DeliveryAttempt{{AttemptNumber: 1}})
		if _, err := pool.Exec(ctx, "UPDATE events SET updated_at = $2 WHERE id = $1", id, old); err != nil {
			t.Fatalf("age event %s: %v", id, err)
		}
	}

	seed("evt-retention-delivered", domain.DeliveryStatusDelivered)
	seed("evt-retention-failed", domain.DeliveryStatusFailed)
	seed("evt-retention-retrying", domain.DeliveryStatusRetrying)

	count, err := retentionRepo.DeleteTerminalEvents(ctx, time.Now().Add(-30*24*time.Hour), 1)
	if err != nil || count != 1 {
		t.Fatalf("first deletion count=%d err=%v, want 1", count, err)
	}
	count, err = retentionRepo.DeleteTerminalEvents(ctx, time.Now().Add(-30*24*time.Hour), 10)
	if err != nil || count != 1 {
		t.Fatalf("second deletion count=%d err=%v, want 1", count, err)
	}

	var activeEvents, activeDeliveries, activeAttempts int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM events WHERE id = 'evt-retention-retrying'").Scan(&activeEvents); err != nil {
		t.Fatalf("count active event: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM deliveries WHERE event_id = 'evt-retention-retrying'").Scan(&activeDeliveries); err != nil {
		t.Fatalf("count active delivery: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM delivery_attempts WHERE event_id = 'evt-retention-retrying'").Scan(&activeAttempts); err != nil {
		t.Fatalf("count active attempts: %v", err)
	}
	if activeEvents != 1 || activeDeliveries != 1 || activeAttempts != 1 {
		t.Fatalf("active work was deleted: events=%d deliveries=%d attempts=%d", activeEvents, activeDeliveries, activeAttempts)
	}
}

func TestRetentionRepository_DeleteTerminalZeroDeliveryEvent(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()
	ctx := context.Background()
	retentionRepo := NewRetentionRepository(pool)
	eventRepo := NewEventRepository(pool)
	event := makeEvent("evt-retention-zero-delivery")
	event.Status = domain.EventStatusDelivered
	event.UpdatedAt = time.Now().Add(-60 * 24 * time.Hour)
	if _, err := eventRepo.InitializeEventDeliveries(ctx, event, nil); err != nil {
		t.Fatalf("initialize event: %v", err)
	}

	count, err := retentionRepo.DeleteTerminalEvents(ctx, time.Now().Add(-30*24*time.Hour), 10)
	if err != nil || count != 1 {
		t.Fatalf("deletion count=%d err=%v, want 1", count, err)
	}
}

func TestRetentionRepository_ConcurrentCleanersSplitBatches(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewRetentionRepository(pool)
	eventRepo := NewEventRepository(pool)
	old := time.Now().Add(-60 * 24 * time.Hour)
	for i := 0; i < 20; i++ {
		event := makeEvent("evt-retention-concurrent-" + string(rune('a'+i)))
		event.Status = domain.EventStatusDelivered
		event.UpdatedAt = old
		if _, err := eventRepo.InitializeEventDeliveries(ctx, event, nil); err != nil {
			t.Fatalf("initialize event %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	counts := make(chan int64, 4)
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count, err := repo.DeleteTerminalEvents(ctx, time.Now().Add(-30*24*time.Hour), 5)
			counts <- count
			errs <- err
		}()
	}
	wg.Wait()
	close(counts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent cleanup: %v", err)
		}
	}
	var total int64
	for count := range counts {
		total += count
	}
	if total != 20 {
		t.Fatalf("deleted = %d, want 20", total)
	}
}
