package retry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
)

// mockEventRepo implements repository.EventRepository for testing.
type mockEventRepo struct {
	mu             sync.Mutex
	events         []*domain.Event
	getPendingErr  error
	getPendingCall int
}

func (m *mockEventRepo) GetPendingEvents(ctx context.Context, limit int) ([]*domain.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getPendingCall++
	if m.getPendingErr != nil {
		return nil, m.getPendingErr
	}
	if len(m.events) == 0 {
		return nil, nil
	}
	// Return up to limit events
	result := m.events
	if len(result) > limit {
		result = result[:limit]
	}
	// Clear events after returning (simulating they were picked up)
	m.events = nil
	return result, nil
}

// Implement other required methods as no-ops
func (m *mockEventRepo) Create(ctx context.Context, event *domain.Event) error { return nil }
func (m *mockEventRepo) CreateBatch(ctx context.Context, events []*domain.Event) error {
	return nil
}
func (m *mockEventRepo) GetByID(ctx context.Context, id string) (*domain.Event, error) {
	return nil, nil
}
func (m *mockEventRepo) UpdateStatus(ctx context.Context, event *domain.Event) error { return nil }
func (m *mockEventRepo) UpdateStatusBatch(ctx context.Context, events []*domain.Event) error {
	return nil
}
func (m *mockEventRepo) RecordAttempt(ctx context.Context, attempt *domain.DeliveryAttempt) error {
	return nil
}
func (m *mockEventRepo) RecordAttemptBatch(ctx context.Context, attempts []*domain.DeliveryAttempt) error {
	return nil
}
func (m *mockEventRepo) GetAttemptsByEventID(ctx context.Context, eventID string) ([]*domain.DeliveryAttempt, error) {
	return nil, nil
}

// mockProcessor implements EventProcessor for testing.
type mockProcessor struct {
	mu        sync.Mutex
	processed []*domain.Event
	delivered []*domain.Event
	retrying  []*domain.Event
	failed    []*domain.Event
}

func (m *mockProcessor) ProcessEvents(ctx context.Context, events []*domain.Event) (delivered, retrying, failed []*domain.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processed = append(m.processed, events...)

	// Return configured results or mark all as delivered by default
	if m.delivered != nil || m.retrying != nil || m.failed != nil {
		return m.delivered, m.retrying, m.failed
	}
	return events, nil, nil
}

func (m *mockProcessor) getProcessedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.processed)
}

func TestPoller_ProcessesRetryEvents(t *testing.T) {
	repo := &mockEventRepo{
		events: []*domain.Event{
			{ID: "evt-1", Type: "order.created", Status: domain.EventStatusRetrying},
			{ID: "evt-2", Type: "order.created", Status: domain.EventStatusRetrying},
		},
	}
	processor := &mockProcessor{}

	config := PollerConfig{
		PollInterval: 50 * time.Millisecond,
		BatchSize:    100,
	}

	poller := NewPoller(repo, processor, config, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Start poller in background
	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	// Wait for context to expire
	<-ctx.Done()
	poller.Stop()
	<-done

	// Verify events were processed
	if processor.getProcessedCount() != 2 {
		t.Errorf("expected 2 events processed, got %d", processor.getProcessedCount())
	}
}

func TestPoller_RespectsPollingInterval(t *testing.T) {
	repo := &mockEventRepo{}
	processor := &mockProcessor{}

	config := PollerConfig{
		PollInterval: 50 * time.Millisecond,
		BatchSize:    100,
	}

	poller := NewPoller(repo, processor, config, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	<-ctx.Done()
	poller.Stop()
	<-done

	// Should have polled: immediately + ~3 times at 50ms intervals in 180ms
	// Allow some variance
	repo.mu.Lock()
	calls := repo.getPendingCall
	repo.mu.Unlock()

	if calls < 3 || calls > 5 {
		t.Errorf("expected 3-5 poll calls in 180ms with 50ms interval, got %d", calls)
	}
}

func TestPoller_StopsGracefully(t *testing.T) {
	repo := &mockEventRepo{}
	processor := &mockProcessor{}

	config := PollerConfig{
		PollInterval: 10 * time.Millisecond,
		BatchSize:    100,
	}

	poller := NewPoller(repo, processor, config, nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	// Let it run briefly
	time.Sleep(50 * time.Millisecond)

	// Stop via context cancellation
	cancel()

	// Should stop within reasonable time
	select {
	case <-done:
		// Good - stopped gracefully
	case <-time.After(500 * time.Millisecond):
		t.Error("poller did not stop within timeout")
	}
}

func TestPoller_StopsViaStopMethod(t *testing.T) {
	repo := &mockEventRepo{}
	processor := &mockProcessor{}

	config := PollerConfig{
		PollInterval: 10 * time.Millisecond,
		BatchSize:    100,
	}

	poller := NewPoller(repo, processor, config, nil)

	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	// Let it run briefly
	time.Sleep(50 * time.Millisecond)

	// Stop via Stop method
	poller.Stop()

	// Should stop within reasonable time
	select {
	case <-done:
		// Good - stopped gracefully
	case <-time.After(500 * time.Millisecond):
		t.Error("poller did not stop within timeout")
	}
}

func TestPoller_HandlesEmptyResults(t *testing.T) {
	repo := &mockEventRepo{} // No events
	processor := &mockProcessor{}

	config := PollerConfig{
		PollInterval: 20 * time.Millisecond,
		BatchSize:    100,
	}

	poller := NewPoller(repo, processor, config, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	<-ctx.Done()
	poller.Stop()
	<-done

	// No events should have been processed
	if processor.getProcessedCount() != 0 {
		t.Errorf("expected 0 events processed, got %d", processor.getProcessedCount())
	}
}

func TestPoller_DefaultConfig(t *testing.T) {
	config := DefaultPollerConfig()

	if config.PollInterval != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", config.PollInterval)
	}
	if config.BatchSize != 100 {
		t.Errorf("expected default batch size 100, got %d", config.BatchSize)
	}
	if config.MaxConcurrentBatches != 1 {
		t.Errorf("expected default max concurrent batches 1, got %d", config.MaxConcurrentBatches)
	}
}

func TestNewPoller_AppliesDefaults(t *testing.T) {
	repo := &mockEventRepo{}
	processor := &mockProcessor{}

	// Empty config - should apply defaults
	poller := NewPoller(repo, processor, PollerConfig{}, nil)

	if poller.config.PollInterval != 5*time.Second {
		t.Errorf("expected default poll interval, got %v", poller.config.PollInterval)
	}
	if poller.config.BatchSize != 100 {
		t.Errorf("expected default batch size, got %d", poller.config.BatchSize)
	}
}
