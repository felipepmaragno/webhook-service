package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/segmentio/kafka-go"

	"github.com/felipemaragno/dispatch/internal/observability"
)

// fakeWriter implements MessageWriter for testing without a real Kafka broker.
type fakeWriter struct {
	written []kafka.Message
	writeErr error
}

func (f *fakeWriter) WriteMessages(ctx context.Context, msgs ...kafka.Message) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.written = append(f.written, msgs...)
	return nil
}

func (f *fakeWriter) Close() error { return nil }

// Verify fakeWriter implements MessageWriter at compile time.
var _ MessageWriter = (*fakeWriter)(nil)

func TestProducer_Publish_happyPath(t *testing.T) {
	writer := &fakeWriter{}
	producer := NewProducerWithWriter(writer, testLogger())

	event := EventMessage{
		ID:     "evt-pub-1",
		Type:   "order.created",
		Source: "billing",
		Data:   json.RawMessage(`{"amount":99}`),
	}

	if err := producer.Publish(context.Background(), event); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	if len(writer.written) != 1 {
		t.Fatalf("expected 1 message written, got %d", len(writer.written))
	}
	if string(writer.written[0].Key) != event.ID {
		t.Errorf("expected key %q, got %q", event.ID, writer.written[0].Key)
	}

	// Verify the payload deserializes correctly
	var got EventMessage
	if err := json.Unmarshal(writer.written[0].Value, &got); err != nil {
		t.Fatalf("failed to unmarshal message value: %v", err)
	}
	if got.ID != event.ID || got.Type != event.Type {
		t.Errorf("payload mismatch: got %+v", got)
	}
}

func TestProducer_Publish_propagatesTraceID(t *testing.T) {
	writer := &fakeWriter{}
	producer := NewProducerWithWriter(writer, testLogger())

	ctx := observability.ContextWithTraceID(context.Background(), "trace-xyz")
	event := EventMessage{ID: "evt-trace", Type: "t", Source: "s", Data: json.RawMessage(`{}`)}

	if err := producer.Publish(ctx, event); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	msg := writer.written[0]
	var traceID string
	for _, h := range msg.Headers {
		if h.Key == observability.TraceIDHeader {
			traceID = string(h.Value)
		}
	}
	if traceID != "trace-xyz" {
		t.Errorf("expected trace ID 'trace-xyz' in headers, got %q", traceID)
	}
}

func TestProducer_Publish_writerError(t *testing.T) {
	writer := &fakeWriter{writeErr: errors.New("broker unavailable")}
	producer := NewProducerWithWriter(writer, testLogger())

	err := producer.Publish(context.Background(), EventMessage{ID: "e", Type: "t", Source: "s", Data: json.RawMessage(`{}`)})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestProducer_PublishBatch_happyPath(t *testing.T) {
	writer := &fakeWriter{}
	producer := NewProducerWithWriter(writer, testLogger())

	events := []EventMessage{
		{ID: "evt-batch-1", Type: "order.created", Source: "s", Data: json.RawMessage(`{}`)},
		{ID: "evt-batch-2", Type: "order.updated", Source: "s", Data: json.RawMessage(`{}`)},
	}

	if err := producer.PublishBatch(context.Background(), events); err != nil {
		t.Fatalf("PublishBatch failed: %v", err)
	}

	if len(writer.written) != 2 {
		t.Errorf("expected 2 messages, got %d", len(writer.written))
	}
	if string(writer.written[0].Key) != "evt-batch-1" {
		t.Errorf("expected key evt-batch-1, got %q", writer.written[0].Key)
	}
}

func TestProducer_PublishBatch_emptyBatch(t *testing.T) {
	writer := &fakeWriter{}
	producer := NewProducerWithWriter(writer, testLogger())

	if err := producer.PublishBatch(context.Background(), []EventMessage{}); err != nil {
		t.Fatalf("PublishBatch([]) failed: %v", err)
	}

	// No messages should be written for empty batch
	if len(writer.written) != 0 {
		t.Errorf("expected 0 messages for empty batch, got %d", len(writer.written))
	}
}

func TestProducer_PublishBatch_writerError(t *testing.T) {
	writer := &fakeWriter{writeErr: errors.New("timeout")}
	producer := NewProducerWithWriter(writer, testLogger())

	events := []EventMessage{
		{ID: "e1", Type: "t", Source: "s", Data: json.RawMessage(`{}`)},
	}
	if err := producer.PublishBatch(context.Background(), events); err == nil {
		t.Error("expected error, got nil")
	}
}

func TestProducer_PublishBatch_doesNotPropagateTraceID(t *testing.T) {
	// This test documents the known gap: PublishBatch does not propagate trace IDs.
	// Publish does. This inconsistency is intentional to document, not fix, here.
	writer := &fakeWriter{}
	producer := NewProducerWithWriter(writer, testLogger())

	ctx := observability.ContextWithTraceID(context.Background(), "trace-batch-xyz")
	events := []EventMessage{
		{ID: "e1", Type: "t", Source: "s", Data: json.RawMessage(`{}`)},
	}

	if err := producer.PublishBatch(ctx, events); err != nil {
		t.Fatalf("PublishBatch failed: %v", err)
	}

	msg := writer.written[0]
	for _, h := range msg.Headers {
		if h.Key == observability.TraceIDHeader {
			t.Logf("NOTE: PublishBatch now propagates trace IDs (header found: %s=%s). Update this test.", h.Key, h.Value)
			return
		}
	}
	// No trace header — expected given current implementation
	t.Log("PublishBatch does not propagate trace IDs (known gap documented in audit.md)")
}
