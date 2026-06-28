package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/repository"
)

func makeEvent(id string) *domain.Event {
	return &domain.Event{
		ID:          id,
		Type:        "order.created",
		Source:      "billing",
		Data:        json.RawMessage(`{"amount":99}`),
		Status:      domain.EventStatusPending,
		Attempts:    0,
		MaxAttempts: 5,
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
		UpdatedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestEventRepository_InitializeEventDeliveries(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)

	subA := makeSub("sub-delivery-a", []string{"order.created"})
	subA.URL = "https://example.com/a"
	subA.MaxDeliveryRate = 25
	subB := makeSub("sub-delivery-b", []string{"order.created"})
	subB.URL = "https://example.com/b"
	if err := subRepo.Create(ctx, subA); err != nil {
		t.Fatalf("create subA: %v", err)
	}
	if err := subRepo.Create(ctx, subB); err != nil {
		t.Fatalf("create subB: %v", err)
	}

	event := makeEvent("evt-init-deliveries")
	deliveries, err := repo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{subA, subB})
	if err != nil {
		t.Fatalf("InitializeEventDeliveries failed: %v", err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(deliveries))
	}
	if deliveries[0].EventID != event.ID {
		t.Errorf("delivery EventID = %q, want %q", deliveries[0].EventID, event.ID)
	}

	again, err := repo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{subA, subB})
	if err != nil {
		t.Fatalf("InitializeEventDeliveries second call failed: %v", err)
	}
	if len(again) != 2 {
		t.Fatalf("expected idempotent 2 deliveries, got %d", len(again))
	}

	got := getDeliveryForTest(t, ctx, repo, event.ID, domain.DeliveryID(event.ID, subA.ID))
	if got.SubscriptionURL != subA.URL {
		t.Errorf("SubscriptionURL = %q, want %q", got.SubscriptionURL, subA.URL)
	}
	if got.MaxDeliveryRate != 25 {
		t.Errorf("MaxDeliveryRate = %d, want 25", got.MaxDeliveryRate)
	}
}

func TestEventRepository_InitializeEventDeliveries_NoSubscriptionsCompletesEvent(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	event := makeEvent("evt-no-deliveries")

	deliveries, err := repo.InitializeEventDeliveries(ctx, event, nil)
	if err != nil {
		t.Fatalf("InitializeEventDeliveries failed: %v", err)
	}
	if len(deliveries) != 0 {
		t.Fatalf("expected no deliveries, got %d", len(deliveries))
	}
	got, err := repo.GetByID(ctx, event.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != domain.EventStatusDelivered {
		t.Errorf("Status = %s, want delivered", got.Status)
	}
	if got.DeliveredAt == nil {
		t.Error("DeliveredAt should be set for zero-delivery event")
	}
}

func TestEventRepository_ClaimDeliveriesAndPersistClaimedOutcome(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)

	sub := makeSub("sub-claim-delivery", []string{"order.created"})
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create sub: %v", err)
	}
	event := makeEvent("evt-claim-delivery")
	if _, err := repo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub}); err != nil {
		t.Fatalf("InitializeEventDeliveries: %v", err)
	}

	claims, err := repo.ClaimDeliveries(ctx, "worker-delivery", time.Minute, 10)
	if err != nil {
		t.Fatalf("ClaimDeliveries: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	claim := claims[0]
	if claim.Reclaimed {
		t.Fatal("new pending delivery should not be reclaimed")
	}
	if claim.Delivery.ProcessingOwner == nil || *claim.Delivery.ProcessingOwner != "worker-delivery" {
		t.Fatalf("unexpected owner: %v", claim.Delivery.ProcessingOwner)
	}

	statusCode := http.StatusOK
	claim.Delivery.Attempts = 1
	claim.Delivery.MarkAsDelivered(time.Now().UTC())
	if err := repo.PersistClaimedDeliveryOutcome(ctx, claim.Delivery, []*domain.DeliveryAttempt{{
		AttemptNumber: 1,
		StatusCode:    &statusCode,
		DurationMs:    10,
	}}); err != nil {
		t.Fatalf("PersistClaimedDeliveryOutcome: %v", err)
	}

	got := getDeliveryForTest(t, ctx, repo, event.ID, claim.Delivery.ID)
	if got.Status != domain.DeliveryStatusDelivered {
		t.Errorf("Status = %s, want delivered", got.Status)
	}
	if got.ProcessingOwner != nil || got.ProcessingDeadline != nil {
		t.Fatal("claim metadata should be cleared after claimed delivery outcome")
	}
}

func TestEventRepository_PersistClaimedDeliveryOutcomeRejectsStaleClaim(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)

	sub := makeSub("sub-stale-delivery", []string{"order.created"})
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create sub: %v", err)
	}
	event := makeEvent("evt-stale-delivery")
	if _, err := repo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub}); err != nil {
		t.Fatalf("InitializeEventDeliveries: %v", err)
	}

	first, err := repo.ClaimDeliveries(ctx, "worker-first", time.Nanosecond, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: claims=%d err=%v", len(first), err)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := repo.ClaimDeliveries(ctx, "worker-second", time.Minute, 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("second claim: claims=%d err=%v", len(second), err)
	}
	if !second[0].Reclaimed {
		t.Fatal("second claim should reclaim expired processing delivery")
	}

	firstDelivery := first[0].Delivery
	firstDelivery.MarkAsFailed("too late")
	if err := repo.PersistClaimedDeliveryOutcome(ctx, firstDelivery, nil); !errors.Is(err, repository.ErrClaimLost) {
		t.Fatalf("expected ErrClaimLost, got %v", err)
	}
}

func TestEventRepository_GetByID(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)

	t.Run("not found returns ErrNotFound", func(t *testing.T) {
		_, err := repo.GetByID(ctx, "nonexistent")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("found returns correct event", func(t *testing.T) {
		evt := makeEvent("evt-getbyid-1")
		if _, err := repo.InitializeEventDeliveries(ctx, evt, nil); err != nil {
			t.Fatalf("InitializeEventDeliveries: %v", err)
		}

		got, err := repo.GetByID(ctx, evt.ID)
		if err != nil {
			t.Fatalf("GetByID failed: %v", err)
		}
		if got.Status != domain.EventStatusDelivered {
			t.Errorf("expected status delivered, got %s", got.Status)
		}
		// PostgreSQL normalizes JSON formatting, so compare parsed content not raw bytes
		var gotData, wantData map[string]interface{}
		if err := json.Unmarshal(got.Data, &gotData); err != nil {
			t.Fatalf("failed to parse returned data: %v", err)
		}
		if err := json.Unmarshal(evt.Data, &wantData); err != nil {
			t.Fatalf("failed to parse original data: %v", err)
		}
		if gotData["amount"] != wantData["amount"] {
			t.Errorf("expected data %v, got %v", wantData, gotData)
		}
	})
}

func TestEventRepository_GetRetryBacklogStats(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)
	now := time.Now().UTC()

	sub := makeSub("sub-stats", []string{"order.created"})
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create stats subscription: %v", err)
	}

	insert := func(id string, status domain.DeliveryStatus, nextAttempt, deadline *time.Time) {
		t.Helper()
		event := makeEvent(id)
		deliveries, err := repo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub})
		if err != nil {
			t.Fatalf("initialize %s: %v", id, err)
		}
		delivery := deliveries[0]
		delivery.Status = status
		delivery.NextAttemptAt = nextAttempt
		delivery.UpdatedAt = now
		if deadline != nil {
			owner := "worker-a"
			delivery.ProcessingOwner = &owner
			delivery.ProcessingDeadline = deadline
		}
		if _, err := pool.Exec(ctx, `
			UPDATE deliveries
			SET status = $2, next_attempt_at = $3, processing_owner = $4,
			    processing_deadline = $5, updated_at = $6
			WHERE id = $1
		`, delivery.ID, delivery.Status, delivery.NextAttemptAt, delivery.ProcessingOwner,
			delivery.ProcessingDeadline, delivery.UpdatedAt); err != nil {
			t.Fatalf("set delivery state %s: %v", id, err)
		}
	}

	oldestDue := now.Add(-2 * time.Minute)
	recentDue := now.Add(-30 * time.Second)
	future := now.Add(time.Hour)
	expired := now.Add(-time.Minute)
	leased := now.Add(time.Minute)
	insert("evt-stats-oldest", domain.DeliveryStatusRetrying, &oldestDue, nil)
	insert("evt-stats-recent", domain.DeliveryStatusThrottled, &recentDue, nil)
	insert("evt-stats-future", domain.DeliveryStatusRetrying, &future, nil)
	insert("evt-stats-expired", domain.DeliveryStatusProcessing, nil, &expired)
	insert("evt-stats-leased", domain.DeliveryStatusProcessing, nil, &leased)
	insert("evt-stats-delivered", domain.DeliveryStatusDelivered, nil, nil)

	stats, err := repo.GetRetryBacklogStats(ctx)
	if err != nil {
		t.Fatalf("GetRetryBacklogStats: %v", err)
	}
	if stats.DueCount != 2 || stats.ExpiredProcessingCount != 1 || stats.LeasedCount != 1 {
		t.Fatalf("unexpected backlog stats: %+v", stats)
	}
	if stats.OldestDueAt == nil || stats.OldestDueAt.After(oldestDue.Add(time.Second)) {
		t.Fatalf("oldest due time = %v, want around %v", stats.OldestDueAt, oldestDue)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin explain transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scan: %v", err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+retryBacklogStatsQuery)
	if err != nil {
		t.Fatalf("explain backlog query: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read explain plan: %v", err)
	}
	if !strings.Contains(plan.String(), "idx_deliveries_retry_claimable") {
		t.Fatalf("backlog query did not use retry claim index:\n%s", plan.String())
	}
}
