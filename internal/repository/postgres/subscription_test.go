package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func makeSub(id string, eventTypes []string) *domain.Subscription {
	return &domain.Subscription{
		ID:         id,
		URL:        "https://example.com/webhook",
		EventTypes: eventTypes,
		RateLimit:  100,
		CreatedAt:  time.Now().UTC().Truncate(time.Millisecond),
		Active:     true,
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
		got, err := repo.GetByID(ctx, sub.ID)
		if err != nil {
			t.Fatalf("GetByID failed: %v", err)
		}
		if got.URL != sub.URL {
			t.Errorf("expected URL %s, got %s", sub.URL, got.URL)
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
		got, _ := repo.GetByID(ctx, sub.ID)
		if got.Secret == nil || *got.Secret != secret {
			t.Errorf("expected secret %q, got %v", secret, got.Secret)
		}
	})
}

func TestSubscriptionRepository_GetByID(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewSubscriptionRepository(pool)

	t.Run("not found returns ErrNotFound", func(t *testing.T) {
		_, err := repo.GetByID(ctx, "nonexistent")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("found returns correct subscription", func(t *testing.T) {
		sub := makeSub("sub-getbyid-1", []string{"payment.*"})
		_ = repo.Create(ctx, sub)

		got, err := repo.GetByID(ctx, sub.ID)
		if err != nil {
			t.Fatalf("GetByID failed: %v", err)
		}
		if len(got.EventTypes) != 1 || got.EventTypes[0] != "payment.*" {
			t.Errorf("unexpected event_types: %v", got.EventTypes)
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

func TestSubscriptionRepository_GetByEventType(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewSubscriptionRepository(pool)

	_ = repo.Create(ctx, makeSub("sub-exact-1", []string{"order.created"}))
	_ = repo.Create(ctx, makeSub("sub-wildcard-1", []string{"order.*"}))
	_ = repo.Create(ctx, makeSub("sub-global-1", []string{"*"}))
	_ = repo.Create(ctx, makeSub("sub-other-1", []string{"payment.done"}))

	t.Run("exact match", func(t *testing.T) {
		subs, err := repo.GetByEventType(ctx, "order.created")
		if err != nil {
			t.Fatalf("GetByEventType failed: %v", err)
		}
		ids := subIDs(subs)
		if !contains(ids, "sub-exact-1") {
			t.Error("expected sub-exact-1")
		}
		if !contains(ids, "sub-wildcard-1") {
			t.Error("expected sub-wildcard-1 (order.* matches order.created)")
		}
		if !contains(ids, "sub-global-1") {
			t.Error("expected sub-global-1 (* matches everything)")
		}
		if contains(ids, "sub-other-1") {
			t.Error("sub-other-1 should not match order.created")
		}
	})

	t.Run("no match returns empty", func(t *testing.T) {
		subs, err := repo.GetByEventType(ctx, "unknown.event")
		if err != nil {
			t.Fatalf("GetByEventType failed: %v", err)
		}
		for _, s := range subs {
			if s.ID != "sub-global-1" {
				t.Errorf("unexpected subscription %s for unknown event type", s.ID)
			}
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

		// GetByID still works (row exists)
		got, err := repo.GetByID(ctx, sub.ID)
		if err != nil {
			t.Fatalf("GetByID after delete failed: %v", err)
		}
		if got.Active {
			t.Error("expected Active=false after Delete")
		}
	})

	t.Run("deleting nonexistent id returns ErrNotFound", func(t *testing.T) {
		err := repo.Delete(ctx, "nonexistent-sub")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
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
