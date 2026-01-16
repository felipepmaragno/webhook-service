package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
)

// Producer publishes events to Kafka.
type Producer struct {
	writer *kafka.Writer
	logger *slog.Logger
}

// ProducerConfig configures the Kafka producer.
type ProducerConfig struct {
	Brokers      []string
	Topic        string
	BatchSize    int
	BatchTimeout time.Duration
	Async        bool
}

// DefaultProducerConfig returns sensible defaults for production.
func DefaultProducerConfig() ProducerConfig {
	return ProducerConfig{
		Brokers:      []string{"localhost:9092"},
		Topic:        "events.pending",
		BatchSize:    100,
		BatchTimeout: 10 * time.Millisecond,
		Async:        false, // Sync for reliability
	}
}

// NewProducer creates a Kafka producer for the API.
func NewProducer(config ProducerConfig, logger *slog.Logger) *Producer {
	writer := &kafka.Writer{
		Addr:         kafka.TCP(config.Brokers...),
		Topic:        config.Topic,
		Balancer:     &kafka.RoundRobin{},
		BatchSize:    config.BatchSize,
		BatchTimeout: config.BatchTimeout,
		RequiredAcks: kafka.RequireAll, // Wait for all replicas
		Async:        config.Async,
		Compression:  kafka.Snappy,
	}

	return &Producer{
		writer: writer,
		logger: logger,
	}
}

// Publish sends an event to Kafka.
func (p *Producer) Publish(ctx context.Context, event EventMessage) error {
	value, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	msg := kafka.Message{
		Key:   []byte(event.ID),
		Value: value,
	}

	if err := p.writer.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}

	return nil
}

// PublishBatch sends multiple events to Kafka.
func (p *Producer) PublishBatch(ctx context.Context, events []EventMessage) error {
	messages := make([]kafka.Message, len(events))
	for i, event := range events {
		value, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal event %d: %w", i, err)
		}
		messages[i] = kafka.Message{
			Key:   []byte(event.ID),
			Value: value,
		}
	}

	if err := p.writer.WriteMessages(ctx, messages...); err != nil {
		return fmt.Errorf("write messages: %w", err)
	}

	return nil
}

// Close closes the producer.
func (p *Producer) Close() error {
	return p.writer.Close()
}

// LoadTestProducer generates test events for load testing.
type LoadTestProducer struct {
	writer *kafka.Writer
	logger *slog.Logger
}

// NewLoadTestProducer creates a producer for load testing.
func NewLoadTestProducer(brokers []string, topic string, logger *slog.Logger) *LoadTestProducer {
	writer := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.RoundRobin{}, // Distribute evenly
		BatchSize:    500,
		BatchTimeout: 5 * time.Millisecond,
		RequiredAcks: kafka.RequireOne, // Faster for load test
		Async:        true,             // Async for max throughput
		Compression:  kafka.Snappy,
	}

	return &LoadTestProducer{
		writer: writer,
		logger: logger,
	}
}

// ProduceEvents generates test events.
func (p *LoadTestProducer) ProduceEvents(ctx context.Context, count int, eventType string) error {
	messages := make([]kafka.Message, 0, 1000)

	for i := 0; i < count; i++ {
		event := EventMessage{
			ID:          fmt.Sprintf("evt_loadtest_%d_%d", time.Now().UnixNano(), i),
			Type:        eventType,
			Source:      "loadtest",
			Data:        json.RawMessage(`{"test": true, "index": ` + fmt.Sprintf("%d", i) + `}`),
			MaxAttempts: 3,
		}

		value, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal event %d: %w", i, err)
		}

		messages = append(messages, kafka.Message{
			Key:   []byte(event.ID),
			Value: value,
		})

		// Write in batches
		if len(messages) >= 1000 {
			if err := p.writer.WriteMessages(ctx, messages...); err != nil {
				return fmt.Errorf("write batch: %w", err)
			}
			messages = messages[:0]

			if i%10000 == 0 {
				p.logger.Info("produced events", "count", i)
			}
		}
	}

	// Write remaining
	if len(messages) > 0 {
		if err := p.writer.WriteMessages(ctx, messages...); err != nil {
			return fmt.Errorf("write final batch: %w", err)
		}
	}

	p.logger.Info("finished producing events", "total", count)
	return nil
}

// ProduceMultiTypeEvents generates events distributed across multiple types.
// This simulates a realistic scenario with many different subscriptions.
func (p *LoadTestProducer) ProduceMultiTypeEvents(ctx context.Context, count int, numTypes int) error {
	return p.produceWithPrefix(ctx, count, numTypes, "event.type")
}

// ProduceOnePerSubscription generates exactly 1 event per subscription type.
// numSubs is the number of subscriptions (and events) to generate.
func (p *LoadTestProducer) ProduceOnePerSubscription(ctx context.Context, numSubs int, typePrefix string) error {
	return p.produceWithPrefix(ctx, numSubs, numSubs, typePrefix)
}

func (p *LoadTestProducer) produceWithPrefix(ctx context.Context, count int, numTypes int, typePrefix string) error {
	messages := make([]kafka.Message, 0, 1000)

	for i := 0; i < count; i++ {
		// Distribute events across different types (round-robin)
		typeIndex := (i % numTypes) + 1
		eventType := fmt.Sprintf("%s.%d", typePrefix, typeIndex)

		event := EventMessage{
			ID:          fmt.Sprintf("evt_multi_%d_%d", time.Now().UnixNano(), i),
			Type:        eventType,
			Source:      "loadtest",
			Data:        json.RawMessage(`{"test": true, "index": ` + fmt.Sprintf("%d", i) + `}`),
			MaxAttempts: 3,
		}

		value, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshal event %d: %w", i, err)
		}

		messages = append(messages, kafka.Message{
			Key:   []byte(event.ID),
			Value: value,
		})

		// Write in batches
		if len(messages) >= 1000 {
			if err := p.writer.WriteMessages(ctx, messages...); err != nil {
				return fmt.Errorf("write batch: %w", err)
			}
			messages = messages[:0]

			if i%10000 == 0 {
				p.logger.Info("produced events", "count", i, "types", numTypes)
			}
		}
	}

	// Write remaining
	if len(messages) > 0 {
		if err := p.writer.WriteMessages(ctx, messages...); err != nil {
			return fmt.Errorf("write final batch: %w", err)
		}
	}

	p.logger.Info("finished producing multi-type events", "total", count, "types", numTypes)
	return nil
}

// Close closes the producer.
func (p *LoadTestProducer) Close() error {
	return p.writer.Close()
}
