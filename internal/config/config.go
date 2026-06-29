// Package config centralizes environment-based configuration for all dispatch services.
// Each service has its own Config struct with Parse() and Validate() methods.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// APIConfig holds configuration for the dispatch-api service.
type APIConfig struct {
	Addr         string
	DatabaseURL  string
	KafkaBrokers []string
	KafkaTopic   string
	LogLevel     string
}

// ParseAPIConfig reads configuration from environment variables with sensible defaults.
func ParseAPIConfig() APIConfig {
	return APIConfig{
		Addr:         envOrDefault("ADDR", ":8080"),
		DatabaseURL:  envOrDefault("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/dispatch?sslmode=disable"),
		KafkaBrokers: splitEnvOrDefault("KAFKA_BROKERS", []string{"localhost:9092"}),
		KafkaTopic:   envOrDefault("KAFKA_TOPIC", "events.pending"),
		LogLevel:     envOrDefault("LOG_LEVEL", "info"),
	}
}

// Validate checks that required fields are set and values are sane.
func (c APIConfig) Validate() error {
	if c.Addr == "" {
		return fmt.Errorf("ADDR must not be empty")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL must not be empty")
	}
	if len(c.KafkaBrokers) == 0 || c.KafkaBrokers[0] == "" {
		return fmt.Errorf("KAFKA_BROKERS must not be empty")
	}
	if c.KafkaTopic == "" {
		return fmt.Errorf("KAFKA_TOPIC must not be empty")
	}
	return nil
}

// WorkerConfig holds configuration for the dispatch-worker service.
type WorkerConfig struct {
	DatabaseURL               string
	DBMaxConns                int32
	KafkaBrokers              []string
	KafkaTopic                string
	KafkaConsumerGroup        string
	InstanceID                string
	MetricsAddr               string
	RetryPollInterval         time.Duration
	RetryBatchSize            int
	RetryMaxConcurrentBatches int
	RetryLeaseDuration        time.Duration
	AttemptBodyRetention      time.Duration
	EventRetention            time.Duration
	RetentionCleanupInterval  time.Duration
	RetentionBatchSize        int
	LogLevel                  string
}

// ParseWorkerConfig reads configuration from environment variables with sensible defaults.
func ParseWorkerConfig() WorkerConfig {
	return WorkerConfig{
		DatabaseURL:               envOrDefault("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/dispatch?sslmode=disable"),
		DBMaxConns:                int32(envIntOrDefault("DB_MAX_CONNS", 30)),
		KafkaBrokers:              splitEnvOrDefault("KAFKA_BROKERS", []string{"localhost:9092"}),
		KafkaTopic:                envOrDefault("KAFKA_TOPIC", "events.pending"),
		KafkaConsumerGroup:        envOrDefault("KAFKA_CONSUMER_GROUP", "dispatch-workers"),
		InstanceID:                envOrDefault("INSTANCE_ID", "worker-1"),
		MetricsAddr:               envOrDefault("METRICS_ADDR", ":8081"),
		RetryPollInterval:         envDurationOrDefault("RETRY_POLL_INTERVAL", 5*time.Second),
		RetryBatchSize:            envIntOrDefault("RETRY_BATCH_SIZE", 100),
		RetryMaxConcurrentBatches: envIntOrDefault("RETRY_MAX_CONCURRENT_BATCHES", 1),
		RetryLeaseDuration:        envDurationOrDefault("RETRY_LEASE_DURATION", 30*time.Second),
		AttemptBodyRetention:      envDurationOrDefault("ATTEMPT_BODY_RETENTION", 7*24*time.Hour),
		EventRetention:            envDurationOrDefault("EVENT_RETENTION", 30*24*time.Hour),
		RetentionCleanupInterval:  envDurationOrDefault("RETENTION_CLEANUP_INTERVAL", time.Hour),
		RetentionBatchSize:        envIntOrDefault("RETENTION_BATCH_SIZE", 1000),
		LogLevel:                  envOrDefault("LOG_LEVEL", "debug"),
	}
}

// Validate checks that required fields are set and values are sane.
func (c WorkerConfig) Validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL must not be empty")
	}
	if c.DBMaxConns <= 0 {
		return fmt.Errorf("DB_MAX_CONNS must be positive, got %d", c.DBMaxConns)
	}
	if len(c.KafkaBrokers) == 0 || c.KafkaBrokers[0] == "" {
		return fmt.Errorf("KAFKA_BROKERS must not be empty")
	}
	if c.KafkaTopic == "" {
		return fmt.Errorf("KAFKA_TOPIC must not be empty")
	}
	if c.KafkaConsumerGroup == "" {
		return fmt.Errorf("KAFKA_CONSUMER_GROUP must not be empty")
	}
	if c.RetryPollInterval <= 0 {
		return fmt.Errorf("RETRY_POLL_INTERVAL must be positive")
	}
	if c.RetryBatchSize <= 0 {
		return fmt.Errorf("RETRY_BATCH_SIZE must be positive, got %d", c.RetryBatchSize)
	}
	if c.RetryMaxConcurrentBatches <= 0 {
		return fmt.Errorf("RETRY_MAX_CONCURRENT_BATCHES must be positive, got %d", c.RetryMaxConcurrentBatches)
	}
	if c.InstanceID == "" {
		return fmt.Errorf("INSTANCE_ID must not be empty")
	}
	if c.RetryLeaseDuration <= 0 {
		return fmt.Errorf("RETRY_LEASE_DURATION must be positive")
	}
	if c.AttemptBodyRetention <= 0 {
		return fmt.Errorf("ATTEMPT_BODY_RETENTION must be positive")
	}
	if c.EventRetention <= 0 {
		return fmt.Errorf("EVENT_RETENTION must be positive")
	}
	if c.EventRetention < c.AttemptBodyRetention {
		return fmt.Errorf("EVENT_RETENTION must be greater than or equal to ATTEMPT_BODY_RETENTION")
	}
	if c.RetentionCleanupInterval <= 0 {
		return fmt.Errorf("RETENTION_CLEANUP_INTERVAL must be positive")
	}
	if c.RetentionBatchSize <= 0 {
		return fmt.Errorf("RETENTION_BATCH_SIZE must be positive, got %d", c.RetentionBatchSize)
	}
	return nil
}

// --- helpers ---

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitEnvOrDefault(key string, fallback []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	if len(parts) == 0 || parts[0] == "" {
		return fallback
	}
	return parts
}

func envIntOrDefault(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envDurationOrDefault(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
