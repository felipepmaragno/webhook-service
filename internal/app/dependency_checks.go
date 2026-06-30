package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/felipemaragno/dispatch/internal/observability"
)

const dependencyCheckTimeout = 2 * time.Second

type healthCheckFunc func(context.Context) error

func (f healthCheckFunc) Ping(ctx context.Context) error {
	return f(ctx)
}

func databaseReadinessCheck(pool *pgxpool.Pool) observability.ReadinessCheck {
	return observability.ReadinessCheck{
		Name:    "database",
		Checker: healthCheckFunc(pool.Ping),
	}
}

func kafkaReadinessCheck(brokers []string, topic string) observability.ReadinessCheck {
	return observability.ReadinessCheck{
		Name: "kafka",
		Checker: kafkaTopicChecker{
			brokers: brokers,
			topic:   topic,
			dial:    defaultKafkaDial,
		},
	}
}

func redisReadinessCheck(redisURL string, client redisPinger) *observability.ReadinessCheck {
	if redisURL == "" {
		return nil
	}
	check := observability.ReadinessCheck{Name: "redis"}
	if client == nil {
		check.Checker = healthCheckFunc(func(context.Context) error {
			return errors.New("redis configured but unavailable")
		})
		return &check
	}
	check.Checker = healthCheckFunc(func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, dependencyCheckTimeout)
		defer cancel()
		return client.Ping(ctx).Err()
	})
	return &check
}

type redisPinger interface {
	Ping(ctx context.Context) *redis.StatusCmd
}

type kafkaMetadataConn interface {
	ReadPartitions(topics ...string) ([]kafkago.Partition, error)
	Close() error
}

type kafkaDialFunc func(ctx context.Context, network string, address string) (kafkaMetadataConn, error)

func defaultKafkaDial(ctx context.Context, network string, address string) (kafkaMetadataConn, error) {
	return kafkago.DialContext(ctx, network, address)
}

type kafkaTopicChecker struct {
	brokers []string
	topic   string
	dial    kafkaDialFunc
}

func (c kafkaTopicChecker) Ping(ctx context.Context) error {
	if len(c.brokers) == 0 || c.brokers[0] == "" {
		return errors.New("kafka brokers are not configured")
	}
	if c.topic == "" {
		return errors.New("kafka topic is not configured")
	}
	dial := c.dial
	if dial == nil {
		dial = defaultKafkaDial
	}

	ctx, cancel := context.WithTimeout(ctx, dependencyCheckTimeout)
	defer cancel()

	conn, err := dial(ctx, "tcp", c.brokers[0])
	if err != nil {
		return fmt.Errorf("dial kafka broker: %w", err)
	}
	defer conn.Close()

	partitions, err := conn.ReadPartitions(c.topic)
	if err != nil {
		return fmt.Errorf("read kafka topic metadata: %w", err)
	}
	if len(partitions) == 0 {
		return errors.New("kafka topic has no partitions")
	}
	return nil
}
