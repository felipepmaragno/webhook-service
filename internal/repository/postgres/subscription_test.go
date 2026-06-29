package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func makeSub(id string, eventTypes []string) *domain.Subscription {
	return &domain.Subscription{
		ID:              id,
		URL:             "https://example.com/webhook",
		EventTypes:      eventTypes,
		MaxDeliveryRate: 100,
		CreatedAt:       time.Now().UTC().Truncate(time.Millisecond),
		Active:          true,
	}
}

func TestSubscriptionRepository_Create(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewSubscriptionRepository(pool)

	t.Run("happy path", func(t *testing.T) {
		sub := makeSub("sub-create-1", []string{"order.created"})
		if err := repo.Create(ctx, sub); err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		got := getSubscriptionForTest(t, ctx, pool, sub.ID)
		if got.URL != sub.URL {
			t.Errorf("expected URL %s, got %s", sub.URL, got.URL)
		}
		if got.MaxDeliveryRate != sub.MaxDeliveryRate {
			t.Errorf("expected MaxDeliveryRate %d, got %d", sub.MaxDeliveryRate, got.MaxDeliveryRate)
		}
		if !got.Active {
			t.Error("expected Active=true")
		}
	})

	t.Run("with secret", func(t *testing.T) {
		sub := makeSub("sub-create-secret", []string{"*"})
		secret := "my-secret"
		sub.Secret = &secret
		if err := repo.Create(ctx, sub); err != nil {
			t.Fatalf("Create with secret failed: %v", err)
		}
		got := getSubscriptionForTest(t, ctx, pool, sub.ID)
		if got.Secret == nil || *got.Secret != secret {
			t.Errorf("expected secret %q, got %v", secret, got.Secret)
		}
	})
}

func TestSubscriptionRepository_GetActive(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewSubscriptionRepository(pool)

	t.Run("returns only active subscriptions", func(t *testing.T) {
		active := makeSub("sub-active-1", []string{"*"})
		inactive := makeSub("sub-inactive-1", []string{"*"})
		inactive.Active = false

		_ = repo.Create(ctx, active)
		_ = repo.Create(ctx, inactive)

		// Delete (soft) the inactive one
		_ = repo.Delete(ctx, inactive.ID)

		subs, err := repo.GetActive(ctx)
		if err != nil {
			t.Fatalf("GetActive failed: %v", err)
		}
		for _, s := range subs {
			if s.ID == inactive.ID {
				t.Error("inactive subscription should not be returned by GetActive")
			}
		}
		found := false
		for _, s := range subs {
			if s.ID == active.ID {
				found = true
			}
		}
		if !found {
			t.Error("active subscription not returned by GetActive")
		}
	})
}

func TestSubscriptionRepository_GetByEventTypes(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewSubscriptionRepository(pool)

	_ = repo.Create(ctx, makeSub("sub-multi-order", []string{"order.*"}))
	_ = repo.Create(ctx, makeSub("sub-multi-payment", []string{"payment.done"}))
	_ = repo.Create(ctx, makeSub("sub-multi-global", []string{"*"}))

	t.Run("empty input returns empty map", func(t *testing.T) {
		result, err := repo.GetByEventTypes(ctx, []string{})
		if err != nil {
			t.Fatalf("GetByEventTypes failed: %v", err)
		}
		if len(result) != 0 {
			t.Errorf("expected empty map, got %d keys", len(result))
		}
	})

	t.Run("groups subscriptions by event type", func(t *testing.T) {
		result, err := repo.GetByEventTypes(ctx, []string{"order.created", "payment.done"})
		if err != nil {
			t.Fatalf("GetByEventTypes failed: %v", err)
		}

		orderSubs := subIDs(result["order.created"])
		if !contains(orderSubs, "sub-multi-order") {
			t.Error("expected sub-multi-order in order.created bucket")
		}
		if !contains(orderSubs, "sub-multi-global") {
			t.Error("expected sub-multi-global in order.created bucket")
		}

		paymentSubs := subIDs(result["payment.done"])
		if !contains(paymentSubs, "sub-multi-payment") {
			t.Error("expected sub-multi-payment in payment.done bucket")
		}
		if !contains(paymentSubs, "sub-multi-global") {
			t.Error("expected sub-multi-global in payment.done bucket")
		}
	})
}

func TestSubscriptionRepository_Delete(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewSubscriptionRepository(pool)

	t.Run("soft delete sets active=false", func(t *testing.T) {
		sub := makeSub("sub-delete-1", []string{"*"})
		_ = repo.Create(ctx, sub)

		if err := repo.Delete(ctx, sub.ID); err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		got := getSubscriptionForTest(t, ctx, pool, sub.ID)
		if got.Active {
			t.Error("expected Active=false after Delete")
		}
	})

	t.Run("deleting nonexistent id returns ErrNotFound", func(t *testing.T) {
		err := repo.Delete(ctx, "nonexistent-sub")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}

func TestSubscriptionRepository_UpdateSecretPreservesFrozenDeliveries(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	subRepo := NewSubscriptionRepository(pool)
	eventRepo := NewEventRepository(pool)
	oldSecret := "old-secret"
	sub := makeSub("sub-rotate-secret", []string{"order.created"})
	sub.Secret = &oldSecret
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("Create subscription: %v", err)
	}
	event := makeEvent("evt-rotate-secret")
	if _, err := eventRepo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub}); err != nil {
		t.Fatalf("InitializeEventDeliveries: %v", err)
	}

	if err := subRepo.UpdateSecret(ctx, sub.ID, "new-secret"); err != nil {
		t.Fatalf("UpdateSecret: %v", err)
	}
	updated := getSubscriptionForTest(t, ctx, pool, sub.ID)
	if updated.Secret == nil || *updated.Secret != "new-secret" {
		t.Fatalf("active secret = %v, want new-secret", updated.Secret)
	}
	deliveries, err := eventRepo.GetDeliveriesByEventID(ctx, event.ID)
	if err != nil {
		t.Fatalf("GetDeliveriesByEventID: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].SubscriptionSecret == nil || *deliveries[0].SubscriptionSecret != oldSecret {
		t.Fatalf("frozen delivery secret changed: %+v", deliveries)
	}

	if err := subRepo.UpdateSecret(ctx, "missing", "secret"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing UpdateSecret error = %v, want ErrNotFound", err)
	}
	if err := subRepo.Delete(ctx, sub.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := subRepo.UpdateSecret(ctx, sub.ID, "another"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("inactive UpdateSecret error = %v, want ErrNotFound", err)
	}
}

// --- helpers ---

func subIDs(subs []*domain.Subscription) []string {
	ids := make([]string, len(subs))
	for i, s := range subs {
		ids[i] = s.ID
	}
	return ids
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func getSubscriptionForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) *domain.Subscription {
	t.Helper()
	var sub domain.Subscription
	if err := pool.QueryRow(ctx, `
		SELECT id, url, event_types, secret, max_delivery_rate, created_at, active
		FROM subscriptions
		WHERE id = $1
	`, id).Scan(
		&sub.ID,
		&sub.URL,
		&sub.EventTypes,
		&sub.Secret,
		&sub.MaxDeliveryRate,
		&sub.CreatedAt,
		&sub.Active,
	); err != nil {
		t.Fatalf("get subscription %s: %v", id, err)
	}
	return &sub
}
