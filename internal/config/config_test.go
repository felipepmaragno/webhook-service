package config

import (
	"os"
	"testing"
	"time"
)

func TestParseAPIConfig_Defaults(t *testing.T) {
	// Clear env vars to test defaults
	for _, key := range []string{"ADDR", "DATABASE_URL", "KAFKA_BROKERS", "KAFKA_TOPIC", "LOG_LEVEL"} {
		t.Setenv(key, "")
	}

	cfg := ParseAPIConfig()

	if cfg.Addr != ":8080" {
		t.Errorf("expected default Addr :8080, got %s", cfg.Addr)
	}
	if cfg.DatabaseURL == "" {
		t.Error("expected non-empty default DatabaseURL")
	}
	if len(cfg.KafkaBrokers) != 1 || cfg.KafkaBrokers[0] != "localhost:9092" {
		t.Errorf("expected default KafkaBrokers [localhost:9092], got %v", cfg.KafkaBrokers)
	}
	if cfg.KafkaTopic != "events.pending" {
		t.Errorf("expected default KafkaTopic events.pending, got %s", cfg.KafkaTopic)
	}
}

func TestParseAPIConfig_FromEnv(t *testing.T) {
	t.Setenv("ADDR", ":9090")
	t.Setenv("DATABASE_URL", "postgres://custom:5432/db")
	t.Setenv("KAFKA_BROKERS", "broker1:9092,broker2:9092")
	t.Setenv("KAFKA_TOPIC", "custom.topic")
	t.Setenv("LOG_LEVEL", "debug")

	cfg := ParseAPIConfig()

	if cfg.Addr != ":9090" {
		t.Errorf("expected Addr :9090, got %s", cfg.Addr)
	}
	if cfg.DatabaseURL != "postgres://custom:5432/db" {
		t.Errorf("expected custom DatabaseURL, got %s", cfg.DatabaseURL)
	}
	if len(cfg.KafkaBrokers) != 2 {
		t.Errorf("expected 2 brokers, got %d", len(cfg.KafkaBrokers))
	}
	if cfg.KafkaTopic != "custom.topic" {
		t.Errorf("expected custom.topic, got %s", cfg.KafkaTopic)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected debug, got %s", cfg.LogLevel)
	}
}

func TestAPIConfig_Validate(t *testing.T) {
	valid := ParseAPIConfig()
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid config, got error: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*APIConfig)
	}{
		{"empty Addr", func(c *APIConfig) { c.Addr = "" }},
		{"empty DatabaseURL", func(c *APIConfig) { c.DatabaseURL = "" }},
		{"empty KafkaBrokers", func(c *APIConfig) { c.KafkaBrokers = nil }},
		{"empty KafkaTopic", func(c *APIConfig) { c.KafkaTopic = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ParseAPIConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestParseWorkerConfig_Defaults(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "DB_MAX_CONNS", "REDIS_URL", "KAFKA_BROKERS",
		"KAFKA_TOPIC", "KAFKA_CONSUMER_GROUP", "INSTANCE_ID", "METRICS_ADDR",
		"RETRY_POLL_INTERVAL", "RETRY_BATCH_SIZE", "LOG_LEVEL"} {
		t.Setenv(key, "")
	}

	cfg := ParseWorkerConfig()

	if cfg.DBMaxConns != 30 {
		t.Errorf("expected default DBMaxConns 30, got %d", cfg.DBMaxConns)
	}
	if cfg.RedisURL != "" {
		t.Errorf("expected empty RedisURL by default, got %s", cfg.RedisURL)
	}
	if cfg.KafkaConsumerGroup != "dispatch-workers" {
		t.Errorf("expected default group dispatch-workers, got %s", cfg.KafkaConsumerGroup)
	}
	if cfg.InstanceID != "worker-1" {
		t.Errorf("expected default InstanceID worker-1, got %s", cfg.InstanceID)
	}
	if cfg.MetricsAddr != ":8081" {
		t.Errorf("expected default MetricsAddr :8081, got %s", cfg.MetricsAddr)
	}
	if cfg.RetryPollInterval != 5*time.Second {
		t.Errorf("expected default RetryPollInterval 5s, got %s", cfg.RetryPollInterval)
	}
	if cfg.RetryBatchSize != 100 {
		t.Errorf("expected default RetryBatchSize 100, got %d", cfg.RetryBatchSize)
	}
}

func TestParseWorkerConfig_FromEnv(t *testing.T) {
	t.Setenv("DB_MAX_CONNS", "50")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("KAFKA_CONSUMER_GROUP", "custom-group")
	t.Setenv("INSTANCE_ID", "worker-42")
	t.Setenv("RETRY_POLL_INTERVAL", "10s")
	t.Setenv("RETRY_BATCH_SIZE", "200")

	cfg := ParseWorkerConfig()

	if cfg.DBMaxConns != 50 {
		t.Errorf("expected DBMaxConns 50, got %d", cfg.DBMaxConns)
	}
	if cfg.RedisURL != "redis://localhost:6379/0" {
		t.Errorf("expected RedisURL redis://localhost:6379/0, got %s", cfg.RedisURL)
	}
	if cfg.KafkaConsumerGroup != "custom-group" {
		t.Errorf("expected custom-group, got %s", cfg.KafkaConsumerGroup)
	}
	if cfg.InstanceID != "worker-42" {
		t.Errorf("expected worker-42, got %s", cfg.InstanceID)
	}
	if cfg.RetryPollInterval != 10*time.Second {
		t.Errorf("expected 10s, got %s", cfg.RetryPollInterval)
	}
	if cfg.RetryBatchSize != 200 {
		t.Errorf("expected 200, got %d", cfg.RetryBatchSize)
	}
}

func TestWorkerConfig_Validate(t *testing.T) {
	// Need valid defaults for base config
	for _, key := range []string{"DATABASE_URL", "DB_MAX_CONNS", "KAFKA_BROKERS",
		"KAFKA_TOPIC", "KAFKA_CONSUMER_GROUP", "RETRY_POLL_INTERVAL", "RETRY_BATCH_SIZE"} {
		t.Setenv(key, "")
	}

	valid := ParseWorkerConfig()
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid config, got error: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*WorkerConfig)
	}{
		{"empty DatabaseURL", func(c *WorkerConfig) { c.DatabaseURL = "" }},
		{"zero DBMaxConns", func(c *WorkerConfig) { c.DBMaxConns = 0 }},
		{"negative DBMaxConns", func(c *WorkerConfig) { c.DBMaxConns = -1 }},
		{"empty KafkaBrokers", func(c *WorkerConfig) { c.KafkaBrokers = nil }},
		{"empty KafkaTopic", func(c *WorkerConfig) { c.KafkaTopic = "" }},
		{"empty KafkaConsumerGroup", func(c *WorkerConfig) { c.KafkaConsumerGroup = "" }},
		{"zero RetryPollInterval", func(c *WorkerConfig) { c.RetryPollInterval = 0 }},
		{"zero RetryBatchSize", func(c *WorkerConfig) { c.RetryBatchSize = 0 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ParseWorkerConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestEnvHelpers_InvalidValues(t *testing.T) {
	// Invalid int falls back to default
	t.Setenv("DB_MAX_CONNS", "not-a-number")
	n := envIntOrDefault("DB_MAX_CONNS", 42)
	if n != 42 {
		t.Errorf("expected fallback 42, got %d", n)
	}

	// Invalid duration falls back to default
	t.Setenv("RETRY_POLL_INTERVAL", "not-a-duration")
	d := envDurationOrDefault("RETRY_POLL_INTERVAL", 7*time.Second)
	if d != 7*time.Second {
		t.Errorf("expected fallback 7s, got %s", d)
	}

	// Unset env returns default
	os.Unsetenv("NONEXISTENT_KEY")
	s := envOrDefault("NONEXISTENT_KEY", "fallback")
	if s != "fallback" {
		t.Errorf("expected fallback, got %s", s)
	}
}
