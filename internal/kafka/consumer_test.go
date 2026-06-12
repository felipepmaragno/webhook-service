package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/felipemaragno/dispatch/internal/observability"
)

// fakeReader implements MessageReader for testing without a real Kafka broker.
type fakeReader struct {
	messages []kafka.Message
	pos      int
	fetchErr error // if set, FetchMessage returns this error
	commits  [][]kafka.Message
}

func (f *fakeReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	if f.fetchErr != nil {
		return kafka.Message{}, f.fetchErr
	}
	select {
	case <-ctx.Done():
		return kafka.Message{}, ctx.Err()
	default:
	}
	if f.pos >= len(f.messages) {
		// Block until context is cancelled (simulates waiting for messages)
		<-ctx.Done()
		return kafka.Message{}, ctx.Err()
	}
	msg := f.messages[f.pos]
	f.pos++
	return msg, nil
}

func (f *fakeReader) CommitMessages(ctx context.Context, msgs ...kafka.Message) error {
	f.commits = append(f.commits, msgs)
	return nil
}

func (f *fakeReader) Close() error { return nil }

// fakeHandler implements EventHandler for testing.
type fakeHandler struct {
	processed [][]*EventMessage
	err       error
}

func (h *fakeHandler) ProcessBatch(ctx context.Context, events []*EventMessage) ([]*EventMessage, []*EventMessage, []*EventMessage, error) {
	h.processed = append(h.processed, events)
	return events, nil, nil, h.err
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func makeKafkaMessage(event EventMessage) kafka.Message {
	value, _ := json.Marshal(event)
	return kafka.Message{Value: value}
}

func TestConsumer_collectBatch_parsesMessages(t *testing.T) {
	events := []EventMessage{
		{ID: "evt-1", Type: "order.created", Source: "billing", Data: json.RawMessage(`{}`)},
		{ID: "evt-2", Type: "payment.done", Source: "payments", Data: json.RawMessage(`{}`)},
	}
	msgs := make([]kafka.Message, len(events))
	for i, e := range events {
		msgs[i] = makeKafkaMessage(e)
	}

	reader := &fakeReader{messages: msgs}
	config := ConsumerConfig{BatchTimeout: 50 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, &fakeHandler{}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	batch, parsed := consumer.collectBatch(ctx)

	if len(batch) != 2 {
		t.Errorf("expected 2 raw messages, got %d", len(batch))
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 parsed events, got %d", len(parsed))
	}
	if parsed[0].ID != "evt-1" || parsed[1].ID != "evt-2" {
		t.Errorf("unexpected event IDs: %v, %v", parsed[0].ID, parsed[1].ID)
	}
}

func TestConsumer_collectBatch_extractsTraceID(t *testing.T) {
	event := EventMessage{ID: "evt-trace", Type: "order.created", Source: "s", Data: json.RawMessage(`{}`)}
	value, _ := json.Marshal(event)
	msg := kafka.Message{
		Value: value,
		Headers: []kafka.Header{
			{Key: observability.TraceIDHeader, Value: []byte("trace-abc-123")},
		},
	}

	reader := &fakeReader{messages: []kafka.Message{msg}}
	config := ConsumerConfig{BatchTimeout: 50 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, &fakeHandler{}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, parsed := consumer.collectBatch(ctx)

	if len(parsed) == 0 {
		t.Fatal("expected 1 parsed event")
	}
	if parsed[0].TraceID != "trace-abc-123" {
		t.Errorf("expected TraceID 'trace-abc-123', got %q", parsed[0].TraceID)
	}
}

func TestConsumer_collectBatch_skipsInvalidJSON(t *testing.T) {
	msgs := []kafka.Message{
		{Value: []byte("not-json")},
		makeKafkaMessage(EventMessage{ID: "evt-valid", Type: "t", Source: "s", Data: json.RawMessage(`{}`)}),
	}

	reader := &fakeReader{messages: msgs}
	config := ConsumerConfig{BatchTimeout: 50 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, &fakeHandler{}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, parsed := consumer.collectBatch(ctx)

	if len(parsed) != 1 {
		t.Errorf("expected 1 valid event (bad message skipped), got %d", len(parsed))
	}
	if parsed[0].ID != "evt-valid" {
		t.Errorf("unexpected event ID: %s", parsed[0].ID)
	}
}

func TestConsumer_processBatchAndCommit_commitsAfterProcessing(t *testing.T) {
	handler := &fakeHandler{}
	reader := &fakeReader{}
	config := ConsumerConfig{BatchTimeout: 50 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, handler, testLogger())

	events := []*EventMessage{
		{ID: "evt-1", Type: "t", Source: "s", Data: json.RawMessage(`{}`)},
	}
	msgs := []kafka.Message{{Value: []byte("raw")}}

	consumer.processBatchAndCommit(context.Background(), msgs, events)

	if len(handler.processed) != 1 {
		t.Errorf("expected handler to be called once, called %d times", len(handler.processed))
	}
	if len(reader.commits) != 1 {
		t.Errorf("expected 1 commit, got %d", len(reader.commits))
	}
}

func TestConsumer_processBatchAndCommit_doesNotCommitAfterPersistenceFailure(t *testing.T) {
	handler := &fakeHandler{err: errors.New("database unavailable")}
	reader := &fakeReader{}
	config := ConsumerConfig{BatchTimeout: 50 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, handler, testLogger())

	events := []*EventMessage{{ID: "evt-1", Type: "t", Source: "s", Data: json.RawMessage(`{}`)}}
	msgs := []kafka.Message{{Partition: 2, Offset: 41, Value: []byte("raw")}}

	consumer.processBatchAndCommit(context.Background(), msgs, events)

	if len(handler.processed) != 1 {
		t.Fatalf("expected handler to be called once, called %d times", len(handler.processed))
	}
	if len(reader.commits) != 0 {
		t.Fatalf("expected no commit after persistence failure, got %d", len(reader.commits))
	}
}

func TestConsumer_processBatchAndCommit_emptyBatchIsNoop(t *testing.T) {
	handler := &fakeHandler{}
	reader := &fakeReader{}
	config := ConsumerConfig{BatchTimeout: 50 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, handler, testLogger())

	consumer.processBatchAndCommit(context.Background(), nil, nil)

	if len(handler.processed) != 0 {
		t.Errorf("expected handler not to be called for empty batch")
	}
	if len(reader.commits) != 0 {
		t.Errorf("expected no commits for empty batch")
	}
}

func TestConsumer_consumeLoop_stopsOnContextCancel(t *testing.T) {
	// fakeReader with no messages — blocks until context cancelled
	reader := &fakeReader{}
	handler := &fakeHandler{}
	config := ConsumerConfig{BatchTimeout: 20 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, handler, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Use Start (which does wg.Add(1) before launching consumeLoop)
	consumer.Start(ctx)

	done := make(chan struct{})
	go func() {
		consumer.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK — loop exited after context cancellation
	case <-time.After(500 * time.Millisecond):
		t.Error("consumeLoop did not stop after context cancellation")
	}
}

func TestConsumer_Stats_returnZeroForFakeReader(t *testing.T) {
	reader := &fakeReader{}
	config := ConsumerConfig{BatchTimeout: 50 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, &fakeHandler{}, testLogger())

	stats := consumer.Stats()
	// fakeReader has no Stats() method — should return zero value without panic
	_ = stats
}

func TestConsumer_collectBatch_stopOnShutdown(t *testing.T) {
	// Reader that blocks forever
	reader := &fakeReader{}
	handler := &fakeHandler{}
	config := ConsumerConfig{BatchTimeout: 500 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, handler, testLogger())

	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		// Close shutdown channel after a short delay
		time.Sleep(50 * time.Millisecond)
		close(consumer.shutdown)
	}()

	go func() {
		consumer.collectBatch(ctx)
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(600 * time.Millisecond):
		t.Error("collectBatch did not stop after shutdown signal")
	}
}

// Verify fakeReader implements MessageReader at compile time.
var _ MessageReader = (*fakeReader)(nil)

// Verify that collecting >0 events from batch messages with correct JSON works for many messages.
func TestConsumer_collectBatch_largeMessage(t *testing.T) {
	const n = 50
	msgs := make([]kafka.Message, n)
	for i := 0; i < n; i++ {
		msgs[i] = makeKafkaMessage(EventMessage{
			ID:     fmt.Sprintf("evt-%d", i),
			Type:   "order.created",
			Source: "test",
			Data:   json.RawMessage(`{}`),
		})
	}

	reader := &fakeReader{messages: msgs}
	config := ConsumerConfig{BatchTimeout: 200 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, &fakeHandler{}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, parsed := consumer.collectBatch(ctx)

	if len(parsed) == 0 {
		t.Error("expected at least some events to be parsed")
	}
	for _, e := range parsed {
		if e.ID == "" {
			t.Error("parsed event has empty ID")
		}
	}
}

// fakeHandlerError simulates handler errors for retry/failure paths.
type fakeHandlerWithRetries struct {
	successes []*EventMessage
	retries   []*EventMessage
	failures  []*EventMessage
}

func (h *fakeHandlerWithRetries) ProcessBatch(ctx context.Context, events []*EventMessage) ([]*EventMessage, []*EventMessage, []*EventMessage, error) {
	return h.successes, h.retries, h.failures, nil
}

func TestConsumer_processBatchAndCommit_stillCommitsOnPartialFailure(t *testing.T) {
	sc := &fakeHandlerWithRetries{
		successes: []*EventMessage{{ID: "ok"}},
		retries:   []*EventMessage{{ID: "retry"}},
		failures:  []*EventMessage{{ID: "fail"}},
	}
	reader := &fakeReader{}
	config := ConsumerConfig{BatchTimeout: 50 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, sc, testLogger())

	events := []*EventMessage{{ID: "ok"}, {ID: "retry"}, {ID: "fail"}}
	msgs := []kafka.Message{{}, {}, {}}

	consumer.processBatchAndCommit(context.Background(), msgs, events)

	// All messages must be committed regardless of per-event outcome
	if len(reader.commits) != 1 {
		t.Errorf("expected 1 commit call, got %d", len(reader.commits))
	}
	if len(reader.commits[0]) != 3 {
		t.Errorf("expected 3 messages committed, got %d", len(reader.commits[0]))
	}
}

func TestConsumer_collectBatch_fetchError(t *testing.T) {
	expectedErr := errors.New("broker disconnected")
	reader := &fakeReader{fetchErr: expectedErr}
	config := ConsumerConfig{BatchTimeout: 100 * time.Millisecond, CommitTimeout: time.Second}
	consumer := NewConsumerWithReader(config, reader, &fakeHandler{}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	batch, parsed := consumer.collectBatch(ctx)

	// Fetch errors are logged and retried — no messages should be returned
	if len(batch) != 0 || len(parsed) != 0 {
		t.Errorf("expected empty batch on fetch error, got batch=%d parsed=%d", len(batch), len(parsed))
	}
}
