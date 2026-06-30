package app

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
	kafkago "github.com/segmentio/kafka-go"
)

type fakeKafkaConn struct {
	partitions []kafkago.Partition
	readErr    error
	closed     bool
}

func (f *fakeKafkaConn) ReadPartitions(topics ...string) ([]kafkago.Partition, error) {
	return f.partitions, f.readErr
}

func (f *fakeKafkaConn) Close() error {
	f.closed = true
	return nil
}

func TestKafkaTopicChecker(t *testing.T) {
	t.Run("healthy topic metadata", func(t *testing.T) {
		conn := &fakeKafkaConn{partitions: []kafkago.Partition{{Topic: "events.pending", ID: 0}}}
		checker := kafkaTopicChecker{
			brokers: []string{"broker:9092"},
			topic:   "events.pending",
			dial: func(ctx context.Context, network string, address string) (kafkaMetadataConn, error) {
				if network != "tcp" {
					t.Fatalf("expected tcp network, got %s", network)
				}
				if address != "broker:9092" {
					t.Fatalf("expected first broker address, got %s", address)
				}
				return conn, nil
			},
		}

		if err := checker.Ping(context.Background()); err != nil {
			t.Fatalf("expected healthy kafka check: %v", err)
		}
		if !conn.closed {
			t.Fatal("expected kafka metadata connection to close")
		}
	})

	t.Run("missing broker", func(t *testing.T) {
		checker := kafkaTopicChecker{topic: "events.pending"}
		if err := checker.Ping(context.Background()); err == nil {
			t.Fatal("expected missing broker error")
		}
	})

	t.Run("dial failure", func(t *testing.T) {
		checker := kafkaTopicChecker{
			brokers: []string{"broker:9092"},
			topic:   "events.pending",
			dial: func(ctx context.Context, network string, address string) (kafkaMetadataConn, error) {
				return nil, errors.New("dial failed")
			},
		}
		if err := checker.Ping(context.Background()); err == nil {
			t.Fatal("expected dial error")
		}
	})

	t.Run("empty partitions", func(t *testing.T) {
		checker := kafkaTopicChecker{
			brokers: []string{"broker:9092"},
			topic:   "events.pending",
			dial: func(ctx context.Context, network string, address string) (kafkaMetadataConn, error) {
				return &fakeKafkaConn{}, nil
			},
		}
		if err := checker.Ping(context.Background()); err == nil {
			t.Fatal("expected empty partition error")
		}
	})
}

type fakeRedisClient struct {
	err error
}

func (f fakeRedisClient) Ping(ctx context.Context) *redis.StatusCmd {
	return redis.NewStatusResult("PONG", f.err)
}

func TestRedisReadinessCheck(t *testing.T) {
	if check := redisReadinessCheck("", nil); check != nil {
		t.Fatal("expected no redis check when Redis URL is absent")
	}

	check := redisReadinessCheck("redis://redis:6379/0", fakeRedisClient{})
	if check == nil {
		t.Fatal("expected redis check when Redis URL is configured")
	}
	if err := check.Checker.Ping(context.Background()); err != nil {
		t.Fatalf("expected healthy redis check: %v", err)
	}

	check = redisReadinessCheck("redis://redis:6379/0", nil)
	if check == nil {
		t.Fatal("expected fail-closed redis check when client is unavailable")
	}
	if err := check.Checker.Ping(context.Background()); err == nil {
		t.Fatal("expected redis readiness failure without client")
	}
}
