package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/felipemaragno/dispatch/internal/domain"
)

func TestInitialSchemaHasOnlyPerDeliveryRuntimeState(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()
	ctx := context.Background()

	var eventLeaseColumns int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_name = 'events'
		  AND column_name IN ('processing_owner', 'processing_deadline')
	`).Scan(&eventLeaseColumns); err != nil {
		t.Fatalf("inspect event lease columns: %v", err)
	}
	if eventLeaseColumns != 0 {
		t.Fatalf("event lease columns = %d, want 0", eventLeaseColumns)
	}

	for _, column := range []string{"event_id", "delivery_id", "subscription_id"} {
		var nullable string
		if err := pool.QueryRow(ctx, `
			SELECT is_nullable
			FROM information_schema.columns
			WHERE table_name = 'delivery_attempts' AND column_name = $1
		`, column).Scan(&nullable); err != nil {
			t.Fatalf("inspect delivery_attempts.%s: %v", column, err)
		}
		if nullable != "NO" {
			t.Fatalf("delivery_attempts.%s nullable = %s, want NO", column, nullable)
		}
	}

	eventRepo := NewEventRepository(pool)
	subRepo := NewSubscriptionRepository(pool)
	sub := makeSub("sub-schema-attribution", []string{"order.created"})
	otherSub := makeSub("sub-schema-other", []string{"order.created"})
	if err := subRepo.Create(ctx, sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if err := subRepo.Create(ctx, otherSub); err != nil {
		t.Fatalf("create other subscription: %v", err)
	}
	event := makeEvent("evt-schema-attribution")
	deliveries, err := eventRepo.InitializeEventDeliveries(ctx, event, []*domain.Subscription{sub})
	if err != nil {
		t.Fatalf("initialize delivery: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO delivery_attempts (event_id, attempt_number, duration_ms)
		VALUES ($1, 1, 1)
	`, event.ID); err == nil {
		t.Fatal("attempt without delivery/subscription attribution was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO delivery_attempts (
			event_id, delivery_id, subscription_id, attempt_number, duration_ms
		) VALUES ($1, $2, $3, 1, 1)
	`, event.ID, deliveries[0].ID, otherSub.ID); err == nil {
		t.Fatal("attempt with mismatched delivery/subscription attribution was accepted")
	}
}

func TestInitialSchemaDownRemovesCurrentSchema(t *testing.T) {
	pool, cleanup := setupIntegrationDB(t)
	defer cleanup()
	ctx := context.Background()

	downSQL, err := os.ReadFile("../../../migrations/001_initial_schema.down.sql")
	if err != nil {
		t.Fatalf("read initial down migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("apply initial down migration: %v", err)
	}

	var tables int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name IN ('events', 'subscriptions', 'deliveries', 'delivery_attempts')
	`).Scan(&tables); err != nil {
		t.Fatalf("inspect rolled-back tables: %v", err)
	}
	if tables != 0 {
		t.Fatalf("application tables after rollback = %d, want 0", tables)
	}
}
