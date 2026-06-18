package retry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/repository"
)

type claimResult struct {
	claims []repository.ClaimedEvent
	err    error
}

type scriptedEventRepo struct {
	mu        sync.Mutex
	results   []claimResult
	calls     int
	callCh    chan int
	lastOwner string
	lastLease time.Duration
}

func newScriptedEventRepo(results ...claimResult) *scriptedEventRepo {
	return &scriptedEventRepo{results: results, callCh: make(chan int, 32)}
}

func (m *scriptedEventRepo) ClaimRetryEvents(_ context.Context, owner string, lease time.Duration, _ int) ([]repository.ClaimedEvent, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.lastOwner = owner
	m.lastLease = lease
	var result claimResult
	if len(m.results) > 0 {
		result = m.results[0]
		m.results = m.results[1:]
	}
	m.mu.Unlock()

	m.callCh <- call
	return result.claims, result.err
}

func (m *scriptedEventRepo) Create(context.Context, *domain.Event) error            { return nil }
func (m *scriptedEventRepo) CreateBatch(context.Context, []*domain.Event) error     { return nil }
func (m *scriptedEventRepo) GetByID(context.Context, string) (*domain.Event, error) { return nil, nil }
func (m *scriptedEventRepo) InitializeEventDeliveries(context.Context, *domain.Event, []*domain.Subscription) ([]*domain.Delivery, error) {
	return nil, nil
}
func (m *scriptedEventRepo) GetDeliveriesByEventID(context.Context, string) ([]*domain.Delivery, error) {
	return nil, nil
}
func (m *scriptedEventRepo) GetDeliveryByID(context.Context, string) (*domain.Delivery, error) {
	return nil, nil
}
func (m *scriptedEventRepo) ClaimDeliveries(context.Context, string, time.Duration, int) ([]repository.ClaimedDelivery, error) {
	return nil, nil
}
func (m *scriptedEventRepo) PersistDeliveryOutcome(context.Context, *domain.Delivery, []*domain.DeliveryAttempt) error {
	return nil
}
func (m *scriptedEventRepo) PersistClaimedDeliveryOutcome(context.Context, *domain.Delivery, []*domain.DeliveryAttempt) error {
	return nil
}
func (m *scriptedEventRepo) UpdateStatus(context.Context, *domain.Event) error            { return nil }
func (m *scriptedEventRepo) UpdateStatusBatch(context.Context, []*domain.Event) error     { return nil }
func (m *scriptedEventRepo) RecordAttempt(context.Context, *domain.DeliveryAttempt) error { return nil }
func (m *scriptedEventRepo) RecordAttemptBatch(context.Context, []*domain.DeliveryAttempt) error {
	return nil
}
func (m *scriptedEventRepo) PersistNewOutcomes(context.Context, []repository.EventOutcome) error {
	return nil
}
func (m *scriptedEventRepo) PersistClaimedOutcomes(context.Context, []repository.EventOutcome) error {
	return nil
}
func (m *scriptedEventRepo) GetAttemptsByEventID(context.Context, string) ([]*domain.DeliveryAttempt, error) {
	return nil, nil
}
func (m *scriptedEventRepo) Shutdown(context.Context) error { return nil }

func TestPoller_DrainsBacklogWithoutWaitingForPollInterval(t *testing.T) {
	const batches = 20
	results := make([]claimResult, 0, batches+1)
	for range batches {
		results = append(results, claimResult{claims: claims(5)})
	}
	results = append(results, claimResult{})

	repo := newScriptedEventRepo(results...)
	processor := newBlockingProcessor()
	processor.ignoreRelease = true
	poller, cancel, done := startPoller(t, repo, processor, PollerConfig{
		PollInterval:         time.Hour,
		BatchSize:            5,
		MaxConcurrentBatches: 4,
	})
	defer stopPoller(t, poller, cancel, done)

	for i := 0; i < batches; i++ {
		waitSignal(t, processor.started, "backlog batch")
	}
	for i := 0; i < batches+1; i++ {
		waitSignal(t, repo.callCh, "backlog claim")
	}

	_, maxActive, processed := processor.snapshot()
	if processed != batches*5 {
		t.Fatalf("processed %d events, want %d", processed, batches*5)
	}
	if maxActive > 4 {
		t.Fatalf("maximum active batches = %d, want at most 4", maxActive)
	}
}

func BenchmarkPollerBacklogDrain(b *testing.B) {
	for _, concurrency := range []int{1, 4} {
		b.Run("concurrency_"+string(rune('0'+concurrency)), func(b *testing.B) {
			for range b.N {
				const batches = 20
				results := make([]claimResult, 0, batches+1)
				for range batches {
					results = append(results, claimResult{claims: claims(5)})
				}
				results = append(results, claimResult{})

				repo := newScriptedEventRepo(results...)
				processor := newBlockingProcessor()
				processor.ignoreRelease = true
				processor.delay = 2 * time.Millisecond
				ctx, cancel := context.WithCancel(context.Background())
				poller := NewPoller(repo, processor, PollerConfig{
					PollInterval:         time.Hour,
					BatchSize:            5,
					MaxConcurrentBatches: concurrency,
				}, nil)
				done := make(chan struct{})
				go func() {
					poller.Start(ctx)
					close(done)
				}()

				for i := 0; i < batches; i++ {
					waitBenchmarkSignal(b, processor.started)
				}
				for i := 0; i < batches+1; i++ {
					waitBenchmarkSignal(b, repo.callCh)
				}
				cancel()
				poller.Stop()
				select {
				case <-done:
				case <-time.After(time.Second):
					b.Fatal("poller did not stop after benchmark drain")
				}
			}
		})
	}
}

func waitBenchmarkSignal(b *testing.B, ch <-chan int) {
	b.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		b.Fatal("timed out waiting for benchmark scheduler signal")
	}
}

type blockingProcessor struct {
	mu            sync.Mutex
	active        int
	maxActive     int
	processed     int
	started       chan int
	release       chan struct{}
	err           error
	ignoreRelease bool
	delay         time.Duration
}

func newBlockingProcessor() *blockingProcessor {
	return &blockingProcessor{
		started: make(chan int, 32),
		release: make(chan struct{}, 32),
	}
}

func (p *blockingProcessor) ProcessEvents(ctx context.Context, events []*domain.Event) ([]*domain.Event, []*domain.Event, []*domain.Event, error) {
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.processed += len(events)
	active := p.active
	p.mu.Unlock()
	p.started <- active
	if p.delay > 0 {
		time.Sleep(p.delay)
	}

	if !p.ignoreRelease {
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}

	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return events, nil, nil, p.err
}

func (p *blockingProcessor) snapshot() (active, maxActive, processed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active, p.maxActive, p.processed
}

func claims(count int) []repository.ClaimedEvent {
	result := make([]repository.ClaimedEvent, count)
	deadline := time.Now().Add(time.Minute)
	for i := range result {
		result[i] = repository.ClaimedEvent{Event: &domain.Event{
			ID:                 "evt-" + string(rune('a'+i)),
			Status:             domain.EventStatusProcessing,
			ProcessingDeadline: &deadline,
		}}
	}
	return result
}

func waitSignal(t *testing.T, ch <-chan int, description string) int {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return 0
	}
}

func assertNoSignal(t *testing.T, ch <-chan int, description string) {
	t.Helper()
	select {
	case value := <-ch:
		t.Fatalf("unexpected %s: %d", description, value)
	case <-time.After(40 * time.Millisecond):
	}
}

func startPoller(t *testing.T, repo *scriptedEventRepo, processor EventProcessor, config PollerConfig) (*Poller, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	poller := NewPoller(repo, processor, config, nil)
	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()
	return poller, cancel, done
}

func stopPoller(t *testing.T, poller *Poller, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	poller.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop")
	}
}

func TestPoller_EnforcesMaxConcurrentBatches(t *testing.T) {
	repo := newScriptedEventRepo(
		claimResult{claims: claims(2)},
		claimResult{claims: claims(2)},
		claimResult{claims: claims(2)},
		claimResult{},
	)
	processor := newBlockingProcessor()
	poller, cancel, done := startPoller(t, repo, processor, PollerConfig{
		PollInterval:         time.Hour,
		BatchSize:            2,
		MaxConcurrentBatches: 2,
	})
	defer stopPoller(t, poller, cancel, done)

	waitSignal(t, processor.started, "first batch")
	waitSignal(t, processor.started, "second batch")
	waitSignal(t, repo.callCh, "first claim")
	waitSignal(t, repo.callCh, "second claim")
	assertNoSignal(t, repo.callCh, "third claim while capacity is full")

	_, maxActive, _ := processor.snapshot()
	if maxActive != 2 {
		t.Fatalf("maximum active batches = %d, want 2", maxActive)
	}

	processor.release <- struct{}{}
	waitSignal(t, repo.callCh, "third claim after capacity is released")
	waitSignal(t, processor.started, "third batch")
	processor.release <- struct{}{}
	processor.release <- struct{}{}
}

func TestPoller_FullBatchDrainsImmediately(t *testing.T) {
	repo := newScriptedEventRepo(
		claimResult{claims: claims(2)},
		claimResult{claims: claims(1)},
	)
	processor := newBlockingProcessor()
	poller, cancel, done := startPoller(t, repo, processor, PollerConfig{
		PollInterval:         time.Hour,
		BatchSize:            2,
		MaxConcurrentBatches: 2,
	})
	defer stopPoller(t, poller, cancel, done)

	waitSignal(t, repo.callCh, "initial claim")
	waitSignal(t, repo.callCh, "immediate follow-up claim")
	waitSignal(t, processor.started, "first batch")
	waitSignal(t, processor.started, "partial batch")

	processor.release <- struct{}{}
	processor.release <- struct{}{}
}

func TestPoller_PartialBatchReturnsToIntervalWaiting(t *testing.T) {
	repo := newScriptedEventRepo(
		claimResult{claims: claims(1)},
		claimResult{},
	)
	processor := newBlockingProcessor()
	poller, cancel, done := startPoller(t, repo, processor, PollerConfig{
		PollInterval:         time.Hour,
		BatchSize:            2,
		MaxConcurrentBatches: 2,
	})
	defer stopPoller(t, poller, cancel, done)

	waitSignal(t, repo.callCh, "partial claim")
	waitSignal(t, processor.started, "partial batch")
	processor.release <- struct{}{}
	assertNoSignal(t, repo.callCh, "follow-up claim after partial batch")
}

func TestPoller_EmptyBatchReturnsToIntervalWaiting(t *testing.T) {
	repo := newScriptedEventRepo(claimResult{}, claimResult{})
	processor := newBlockingProcessor()
	poller, cancel, done := startPoller(t, repo, processor, PollerConfig{
		PollInterval:         time.Hour,
		BatchSize:            2,
		MaxConcurrentBatches: 2,
	})
	defer stopPoller(t, poller, cancel, done)

	waitSignal(t, repo.callCh, "empty claim")
	assertNoSignal(t, repo.callCh, "follow-up claim after empty result")
}

func TestPoller_ShutdownStopsClaimsAndWaitsForInflightBatch(t *testing.T) {
	repo := newScriptedEventRepo(
		claimResult{claims: claims(1)},
		claimResult{claims: claims(1)},
	)
	processor := newBlockingProcessor()
	poller, cancel, done := startPoller(t, repo, processor, PollerConfig{
		PollInterval:         time.Hour,
		BatchSize:            2,
		MaxConcurrentBatches: 1,
	})

	waitSignal(t, processor.started, "in-flight batch")
	waitSignal(t, repo.callCh, "initial claim")
	stopReturned := make(chan struct{})
	go func() {
		poller.Stop()
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
		t.Fatal("Stop returned before the in-flight batch completed")
	case <-time.After(40 * time.Millisecond):
	}
	assertNoSignal(t, repo.callCh, "claim after stop")

	processor.release <- struct{}{}
	select {
	case <-stopReturned:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after the in-flight batch completed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestPoller_UsesClaimIdentityDefaults(t *testing.T) {
	repo := newScriptedEventRepo(claimResult{})
	processor := newBlockingProcessor()
	poller, cancel, done := startPoller(t, repo, processor, PollerConfig{PollInterval: time.Hour})
	waitSignal(t, repo.callCh, "claim")
	stopPoller(t, poller, cancel, done)

	repo.mu.Lock()
	owner, lease := repo.lastOwner, repo.lastLease
	repo.mu.Unlock()
	if owner != "worker-1" || lease != 30*time.Second {
		t.Fatalf("claim identity owner=%q lease=%s, want worker-1 and 30s", owner, lease)
	}
}

func TestPoller_LogsProcessorPersistenceFailure(t *testing.T) {
	repo := newScriptedEventRepo()
	processor := newBlockingProcessor()
	processor.err = errors.New("database unavailable")
	processor.ignoreRelease = true
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError}))
	poller := NewPoller(repo, processor, DefaultPollerConfig(), logger)

	poller.processRetryBatch(context.Background(), []*domain.Event{{ID: "evt-1"}})

	if !strings.Contains(logs.String(), "retry batch persistence failed") {
		t.Fatalf("expected persistence failure log, got %q", logs.String())
	}
}

func TestPoller_DefaultConfig(t *testing.T) {
	config := DefaultPollerConfig()
	if config.PollInterval != 5*time.Second || config.BatchSize != 100 || config.MaxConcurrentBatches != 1 {
		t.Fatalf("unexpected default capacity config: %+v", config)
	}
	if config.InstanceID != "worker-1" || config.LeaseDuration != 30*time.Second {
		t.Fatalf("unexpected default lease config: %+v", config)
	}
}

func TestNewPoller_AppliesDefaults(t *testing.T) {
	poller := NewPoller(newScriptedEventRepo(), newBlockingProcessor(), PollerConfig{}, nil)
	if poller.config.PollInterval != 5*time.Second || poller.config.BatchSize != 100 || poller.config.MaxConcurrentBatches != 1 {
		t.Fatalf("unexpected applied defaults: %+v", poller.config)
	}
	if poller.config.InstanceID != "worker-1" || poller.config.LeaseDuration != 30*time.Second {
		t.Fatalf("unexpected applied lease defaults: %+v", poller.config)
	}
}
