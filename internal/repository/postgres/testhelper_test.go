package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/felipemaragno/dispatch/internal/domain"
)

// setupIntegrationDB starts a PostgreSQL container with the full production schema
// applied from the migrations directory. Use this for all repository integration tests.
func setupIntegrationDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		t.Fatalf("failed to get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		t.Fatalf("failed to connect to postgres: %v", err)
	}

	if err := applyMigrations(ctx, pool); err != nil {
		pool.Close()
		_ = pgContainer.Terminate(ctx)
		t.Fatalf("failed to apply migrations: %v", err)
	}

	cleanup := func() {
		pool.Close()
		_ = pgContainer.Terminate(ctx)
	}

	return pool, cleanup
}

func persistClaimedOutcomeForTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repo *EventRepository,
	delivery *domain.Delivery,
	attempts []*domain.DeliveryAttempt,
) {
	t.Helper()
	owner := "test-setup-owner"
	deadline := time.Now().UTC().Add(time.Hour)
	if _, err := pool.Exec(ctx, `
		UPDATE deliveries
		SET status = 'processing', processing_owner = $2, processing_deadline = $3
		WHERE id = $1
	`, delivery.ID, owner, deadline); err != nil {
		t.Fatalf("prepare claimed delivery %s: %v", delivery.ID, err)
	}
	delivery.ProcessingOwner = &owner
	delivery.ProcessingDeadline = &deadline
	if err := repo.PersistClaimedDeliveryOutcome(ctx, delivery, attempts); err != nil {
		t.Fatalf("persist claimed delivery %s: %v", delivery.ID, err)
	}
}

func getDeliveryForTest(t *testing.T, ctx context.Context, repo *EventRepository, eventID, deliveryID string) *domain.Delivery {
	t.Helper()
	deliveries, err := repo.GetDeliveriesByEventID(ctx, eventID)
	if err != nil {
		t.Fatalf("get deliveries for %s: %v", eventID, err)
	}
	for _, delivery := range deliveries {
		if delivery.ID == deliveryID {
			return delivery
		}
	}
	t.Fatalf("delivery %s not found for event %s", deliveryID, eventID)
	return nil
}

// applyMigrations reads and executes all migration files in order.
// Mirrors what happens in production via cmd/migrate.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	migrationsDir := "../../../migrations"
	migrations := []string{
		migrationsDir + "/001_initial_schema.up.sql",
	}

	for _, path := range migrations {
		sql, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return err
		}
	}
	return nil
}
