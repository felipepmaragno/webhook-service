// Package retry provides retry policies and polling for failed event reprocessing.
package retry

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/repository"
)

// EventProcessor processes events for delivery.
// This interface allows the poller to use the same delivery logic as the Kafka consumer.
type EventProcessor interface {
	ProcessEvents(ctx context.Context, events []*domain.Event) (delivered, retrying, failed []*domain.Event, err error)
}

type PollerMetrics struct {
	Claimed            func(int)
	Reclaimed          func(int)
	EmptyPoll          func()
	ClaimFailure       func()
	PersistenceFailure func(staleOwner bool)
	ActiveBatches      func(delta int)
	SchedulingLag      func(seconds float64)
	Backlog            func(stats repository.RetryBacklogStats, oldestDueAgeSeconds float64)
}

// PollerConfig holds configuration for the retry poller.
type PollerConfig struct {
	// PollInterval is how often to check for retry events (default: 5s)
	PollInterval time.Duration
	// BatchSize is the maximum number of events to fetch per poll (default: 100)
	BatchSize int
	// MaxConcurrentBatches limits parallel batch processing (default: 1)
	MaxConcurrentBatches int
	InstanceID           string
	LeaseDuration        time.Duration
}

// DefaultPollerConfig returns sensible defaults.
func DefaultPollerConfig() PollerConfig {
	return PollerConfig{
		PollInterval:         5 * time.Second,
		BatchSize:            100,
		MaxConcurrentBatches: 1,
		InstanceID:           "worker-1",
		LeaseDuration:        30 * time.Second,
	}
}

// Poller polls the database for events that need retry and processes them.
// It uses FOR UPDATE SKIP LOCKED to safely run multiple instances.
type Poller struct {
	config    PollerConfig
	eventRepo repository.EventRepository
	processor EventProcessor
	logger    *slog.Logger
	metrics   PollerMetrics

	wg        sync.WaitGroup
	stopCh    chan struct{}
	stopOnce  sync.Once
	batchDone chan struct{}
	started   chan struct{}
	done      chan struct{}
}

func (p *Poller) WithMetrics(metrics PollerMetrics) *Poller {
	p.metrics = metrics
	return p
}

// NewPoller creates a new retry poller.
func NewPoller(
	eventRepo repository.EventRepository,
	processor EventProcessor,
	config PollerConfig,
	logger *slog.Logger,
) *Poller {
	if config.PollInterval == 0 {
		config.PollInterval = 5 * time.Second
	}
	if config.BatchSize == 0 {
		config.BatchSize = 100
	}
	if config.MaxConcurrentBatches == 0 {
		config.MaxConcurrentBatches = 1
	}
	if config.InstanceID == "" {
		config.InstanceID = "worker-1"
	}
	if config.LeaseDuration == 0 {
		config.LeaseDuration = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Poller{
		config:    config,
		eventRepo: eventRepo,
		processor: processor,
		logger:    logger,
		stopCh:    make(chan struct{}),
		batchDone: make(chan struct{}, config.MaxConcurrentBatches),
		started:   make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start begins polling for retry events.
// This method blocks until Stop is called or context is cancelled.
func (p *Poller) Start(ctx context.Context) {
	close(p.started)
	defer close(p.done)
	p.logger.Info("retry poller started",
		"poll_interval", p.config.PollInterval,
		"batch_size", p.config.BatchSize,
		"max_concurrent_batches", p.config.MaxConcurrentBatches,
		"instance_id", p.config.InstanceID,
		"lease_duration", p.config.LeaseDuration,
	)

	ticker := time.NewTicker(p.config.PollInterval)
	defer ticker.Stop()

	activeBatches := 0
	draining := true

	for {
		for draining && activeBatches < p.config.MaxConcurrentBatches {
			select {
			case <-ctx.Done():
				p.logger.Info("retry poller stopping due to context cancellation")
				return
			case <-p.stopCh:
				p.logger.Info("retry poller stopping due to stop signal")
				return
			default:
			}

			claimed, err := p.claim(ctx)
			if err != nil {
				draining = false
				break
			}
			if len(claimed) == 0 {
				p.recordEmptyPoll()
				p.refreshBacklog(ctx)
				draining = false
				break
			}

			activeBatches++
			if len(claimed) < p.config.BatchSize {
				draining = false
			}
			p.startBatch(ctx, claimed)
			if !draining {
				p.refreshBacklog(ctx)
			}
		}

		select {
		case <-ctx.Done():
			p.logger.Info("retry poller stopping due to context cancellation")
			return
		case <-p.stopCh:
			p.logger.Info("retry poller stopping due to stop signal")
			return
		case <-ticker.C:
			draining = true
		case <-p.batchDone:
			activeBatches--
		}
	}
}

// Stop signals the poller to stop and waits for in-flight work to complete.
func (p *Poller) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	select {
	case <-p.started:
		<-p.done
	default:
		return
	}
	p.wg.Wait()
}

func (p *Poller) claim(ctx context.Context) ([]repository.ClaimedEvent, error) {
	claimed, err := p.eventRepo.ClaimRetryEvents(ctx, p.config.InstanceID, p.config.LeaseDuration, p.config.BatchSize)
	if err != nil {
		p.logger.Error("failed to fetch retry events", "error", err)
		if p.metrics.ClaimFailure != nil {
			p.metrics.ClaimFailure()
		}
		return nil, err
	}
	return claimed, nil
}

func (p *Poller) startBatch(ctx context.Context, claimed []repository.ClaimedEvent) {
	events := make([]*domain.Event, 0, len(claimed))
	reclaimed := 0
	for _, claim := range claimed {
		events = append(events, claim.Event)
		if claim.Reclaimed {
			reclaimed++
		}
		p.recordSchedulingLag(claim)
	}
	if p.metrics.Claimed != nil {
		p.metrics.Claimed(len(claimed))
	}
	if reclaimed > 0 && p.metrics.Reclaimed != nil {
		p.metrics.Reclaimed(reclaimed)
	}
	if p.metrics.ActiveBatches != nil {
		p.metrics.ActiveBatches(1)
	}
	leaseDeadline := claimed[0].Event.ProcessingDeadline
	p.logger.Debug("claimed events for retry", "count", len(events), "reclaimed", reclaimed,
		"owner", p.config.InstanceID, "lease_deadline", leaseDeadline)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() { p.batchDone <- struct{}{} }()
		defer func() {
			if p.metrics.ActiveBatches != nil {
				p.metrics.ActiveBatches(-1)
			}
		}()
		p.processRetryBatch(ctx, events)
	}()
}

func (p *Poller) processRetryBatch(ctx context.Context, events []*domain.Event) {
	// Convert domain.Event to format expected by processor
	delivered, retrying, failed, err := p.processor.ProcessEvents(ctx, events)
	if err != nil {
		p.logger.Error("retry batch persistence failed", "error", err, "total", len(events))
		if p.metrics.PersistenceFailure != nil {
			p.metrics.PersistenceFailure(errors.Is(err, repository.ErrClaimLost))
		}
		return
	}

	p.logger.Info("retry batch processed",
		"total", len(events),
		"delivered", len(delivered),
		"retrying", len(retrying),
		"failed", len(failed),
	)
}

func (p *Poller) recordEmptyPoll() {
	if p.metrics.EmptyPoll != nil {
		p.metrics.EmptyPoll()
	}
}

func (p *Poller) recordSchedulingLag(claim repository.ClaimedEvent) {
	if p.metrics.SchedulingLag == nil || claim.Event == nil {
		return
	}
	scheduledAt := claim.Event.NextAttemptAt
	if claim.Reclaimed {
		scheduledAt = claim.Event.ProcessingDeadline
	}
	if scheduledAt == nil {
		return
	}
	lag := time.Since(*scheduledAt).Seconds()
	if lag < 0 {
		lag = 0
	}
	p.metrics.SchedulingLag(lag)
}

func (p *Poller) refreshBacklog(ctx context.Context) {
	if p.metrics.Backlog == nil {
		return
	}
	reader, ok := p.eventRepo.(repository.RetryBacklogReader)
	if !ok {
		return
	}
	stats, err := reader.GetRetryBacklogStats(ctx)
	if err != nil {
		p.logger.Error("failed to collect retry backlog stats", "error", err)
		return
	}
	oldestAge := 0.0
	if stats.OldestDueAt != nil {
		oldestAge = time.Since(*stats.OldestDueAt).Seconds()
		if oldestAge < 0 {
			oldestAge = 0
		}
	}
	p.metrics.Backlog(stats, oldestAge)
}
