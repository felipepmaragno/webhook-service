package resilience

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/felipemaragno/dispatch/internal/testutil"
)

func setupRedisClient(t *testing.T) (*redis.Client, func()) {
	t.Helper()

	if err := testutil.DockerAvailable(); err != nil {
		t.Skipf("docker not available for testcontainers: %v", err)
	}

	ctx := context.Background()
	redisContainer, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}

	connStr, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		_ = redisContainer.Terminate(ctx)
		t.Fatalf("get redis connection string: %v", err)
	}

	opts, err := redis.ParseURL(connStr)
	if err != nil {
		_ = redisContainer.Terminate(ctx)
		t.Fatalf("parse redis connection string: %v", err)
	}

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		_ = redisContainer.Terminate(ctx)
		t.Fatalf("ping redis: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		_ = redisContainer.Terminate(ctx)
	}
	return client, cleanup
}
