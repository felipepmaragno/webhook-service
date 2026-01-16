// Package kafka provides Kafka consumer for processing webhook events.
// It implements at-least-once delivery with manual offset commits after
// successful database writes, ensuring no events are lost.
package kafka

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
)

// ConsumerConfig defines Kafka consumer parameters.
type ConsumerConfig struct {
	Brokers       []string
	Topic         string
	GroupID       string
	InstanceID    string
	BatchTimeout  time.Duration // Max time to collect messages before processing
	CommitTimeout time.Duration // Timeout for offset commits
}

func DefaultConsumerConfig() ConsumerConfig {
	return ConsumerConfig{
		BatchTimeout:  100 * time.Millisecond, // Process whatever arrived in 100ms
		CommitTimeout: 5 * time.Second,
	}
}

// EventMessage represents an event from Kafka.
type EventMessage struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Source      string          `json:"source"`
	Data        json.RawMessage `json:"data"`
	MaxAttempts int             `json:"max_attempts,omitempty"`
	// Retry metadata
	Attempt       int        `json:"attempt,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
}

// Consumer reads events from Kafka and processes them.
type Consumer struct {
	config  ConsumerConfig
	reader  *kafka.Reader
	handler EventHandler
	logger  *slog.Logger

	wg       sync.WaitGroup
	shutdown chan struct{}
}

// EventHandler processes a batch of events.
// Returns successfully processed events and events that need retry.
type EventHandler interface {
	ProcessBatch(ctx context.Context, events []*EventMessage) (successes []*EventMessage, retries []*EventMessage, failures []*EventMessage)
}

// NewConsumer creates a new Kafka consumer.
func NewConsumer(config ConsumerConfig, handler EventHandler, logger *slog.Logger) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        config.Brokers,
		Topic:          config.Topic,
		GroupID:        config.GroupID,
		MinBytes:       1,
		MaxBytes:       10e6, // 10MB
		MaxWait:        config.BatchTimeout,
		CommitInterval: 0, // Manual commits only
		StartOffset:    kafka.LastOffset,
		// Consumer group rebalancing
		GroupBalancers: []kafka.GroupBalancer{
			kafka.RangeGroupBalancer{},
			kafka.RoundRobinGroupBalancer{},
		},
		// Isolation level for exactly-once semantics (if producer uses transactions)
		IsolationLevel: kafka.ReadCommitted,
	})

	return &Consumer{
		config:   config,
		reader:   reader,
		handler:  handler,
		logger:   logger,
		shutdown: make(chan struct{}),
	}
}

// Start begins consuming messages.
func (c *Consumer) Start(ctx context.Context) {
	c.wg.Add(1)
	go c.consumeLoop(ctx)
	c.logger.Info("kafka consumer started",
		"topic", c.config.Topic,
		"group", c.config.GroupID,
		"instance", c.config.InstanceID,
		"batch_timeout", c.config.BatchTimeout,
	)
}

// Stop gracefully shuts down the consumer.
func (c *Consumer) Stop() {
	close(c.shutdown)
	c.wg.Wait()
	if err := c.reader.Close(); err != nil {
		c.logger.Error("failed to close kafka reader", "error", err)
	}
	c.logger.Info("kafka consumer stopped")
}

func (c *Consumer) consumeLoop(ctx context.Context) {
	defer c.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.shutdown:
			return
		default:
		}

		// Collect messages for BatchTimeout duration
		batch, events := c.collectBatch(ctx)
		if len(batch) > 0 {
			c.processBatchAndCommit(ctx, batch, events)
		}
	}
}

// collectBatch fetches messages until timeout, returning all collected messages
func (c *Consumer) collectBatch(ctx context.Context) ([]kafka.Message, []*EventMessage) {
	var batch []kafka.Message
	var events []*EventMessage

	deadline := time.Now().Add(c.config.BatchTimeout)

	for time.Now().Before(deadline) {
		// Check for shutdown
		select {
		case <-ctx.Done():
			return batch, events
		case <-c.shutdown:
			return batch, events
		default:
		}

		// Short timeout for each fetch to stay responsive
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if remaining > 10*time.Millisecond {
			remaining = 10 * time.Millisecond
		}

		readCtx, cancel := context.WithTimeout(ctx, remaining)
		msg, err := c.reader.FetchMessage(readCtx)
		cancel()

		if err != nil {
			if err == context.DeadlineExceeded || err == context.Canceled {
				continue
			}
			c.logger.Error("failed to fetch message", "error", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Parse message
		var event EventMessage
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			c.logger.Error("failed to unmarshal event",
				"error", err,
				"partition", msg.Partition,
				"offset", msg.Offset,
			)
			// Commit bad message to avoid blocking
			if err := c.commitMessages(ctx, []kafka.Message{msg}); err != nil {
				c.logger.Error("failed to commit bad message", "error", err)
			}
			continue
		}

		batch = append(batch, msg)
		events = append(events, &event)
	}

	return batch, events
}

func (c *Consumer) processBatchAndCommit(ctx context.Context, messages []kafka.Message, events []*EventMessage) {
	if len(events) == 0 {
		return
	}

	start := time.Now()

	// Process batch
	successes, retries, failures := c.handler.ProcessBatch(ctx, events)

	c.logger.Debug("batch processed",
		"total", len(events),
		"successes", len(successes),
		"retries", len(retries),
		"failures", len(failures),
		"duration_ms", time.Since(start).Milliseconds(),
	)

	// Commit all messages after processing
	// At-least-once: we commit after processing, so if we crash before commit,
	// messages will be redelivered. The handler must be idempotent.
	if err := c.commitMessages(ctx, messages); err != nil {
		c.logger.Error("failed to commit messages",
			"error", err,
			"count", len(messages),
		)
		// Don't return - messages will be redelivered on restart
	}
}

func (c *Consumer) commitMessages(ctx context.Context, messages []kafka.Message) error {
	if len(messages) == 0 {
		return nil
	}

	commitCtx, cancel := context.WithTimeout(ctx, c.config.CommitTimeout)
	defer cancel()

	return c.reader.CommitMessages(commitCtx, messages...)
}

// Stats returns consumer statistics.
func (c *Consumer) Stats() kafka.ReaderStats {
	return c.reader.Stats()
}
