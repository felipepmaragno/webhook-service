// Package retry provides retry policies and polling for failed event reprocessing.
package retry

import (
	"context"
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

	wg     sync.WaitGroup
	stopCh chan struct{}
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
	}
}

// Start begins polling for retry events.
// This method blocks until Stop is called or context is cancelled.
func (p *Poller) Start(ctx context.Context) {
	p.logger.Info("retry poller started",
		"poll_interval", p.config.PollInterval,
		"batch_size", p.config.BatchSize,
		"instance_id", p.config.InstanceID,
		"lease_duration", p.config.LeaseDuration,
	)

	ticker := time.NewTicker(p.config.PollInterval)
	defer ticker.Stop()

	// Process immediately on start, then on interval
	p.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("retry poller stopping due to context cancellation")
			return
		case <-p.stopCh:
			p.logger.Info("retry poller stopping due to stop signal")
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// Stop signals the poller to stop and waits for in-flight work to complete.
func (p *Poller) Stop() {
	close(p.stopCh)
	p.wg.Wait()
}

func (p *Poller) poll(ctx context.Context) {
	claimed, err := p.eventRepo.ClaimRetryEvents(ctx, p.config.InstanceID, p.config.LeaseDuration, p.config.BatchSize)
	if err != nil {
		p.logger.Error("failed to fetch retry events", "error", err)
		return
	}

	if len(claimed) == 0 {
		return
	}

	events := make([]*domain.Event, 0, len(claimed))
	reclaimed := 0
	for _, claim := range claimed {
		events = append(events, claim.Event)
		if claim.Reclaimed {
			reclaimed++
		}
	}
	leaseDeadline := claimed[0].Event.ProcessingDeadline
	p.logger.Debug("claimed events for retry", "count", len(events), "reclaimed", reclaimed,
		"owner", p.config.InstanceID, "lease_deadline", leaseDeadline)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.processRetryBatch(ctx, events)
	}()
}

func (p *Poller) processRetryBatch(ctx context.Context, events []*domain.Event) {
	// Convert domain.Event to format expected by processor
	delivered, retrying, failed, err := p.processor.ProcessEvents(ctx, events)
	if err != nil {
		p.logger.Error("retry batch persistence failed", "error", err, "total", len(events))
		return
	}

	p.logger.Info("retry batch processed",
		"total", len(events),
		"delivered", len(delivered),
		"retrying", len(retrying),
		"failed", len(failed),
	)
}
