// Package retention runs bounded cleanup for persisted delivery history.
package retention

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Repository interface {
	RedactAttemptBodies(ctx context.Context, before time.Time, limit int) (int64, error)
	DeleteTerminalEvents(ctx context.Context, before time.Time, limit int) (int64, error)
}

type Observer interface {
	AttemptBodiesRedacted(count int64)
	TerminalEventsDeleted(count int64)
	CycleFailed()
	CycleCompleted(duration time.Duration, completedAt time.Time)
}

type noopObserver struct{}

func (noopObserver) AttemptBodiesRedacted(int64)             {}
func (noopObserver) TerminalEventsDeleted(int64)             {}
func (noopObserver) CycleFailed()                            {}
func (noopObserver) CycleCompleted(time.Duration, time.Time) {}

type Config struct {
	AttemptBodyRetention time.Duration
	EventRetention       time.Duration
	Interval             time.Duration
	BatchSize            int
}

func DefaultConfig() Config {
	return Config{
		AttemptBodyRetention: 7 * 24 * time.Hour,
		EventRetention:       30 * 24 * time.Hour,
		Interval:             time.Hour,
		BatchSize:            1000,
	}
}

type Cleaner struct {
	repo     Repository
	config   Config
	logger   *slog.Logger
	observer Observer

	stopCh   chan struct{}
	started  chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func NewCleaner(repo Repository, config Config, logger *slog.Logger, observer Observer) *Cleaner {
	defaults := DefaultConfig()
	if config.AttemptBodyRetention <= 0 {
		config.AttemptBodyRetention = defaults.AttemptBodyRetention
	}
	if config.EventRetention <= 0 {
		config.EventRetention = defaults.EventRetention
	}
	if config.Interval <= 0 {
		config.Interval = defaults.Interval
	}
	if config.BatchSize <= 0 {
		config.BatchSize = defaults.BatchSize
	}
	if logger == nil {
		logger = slog.Default()
	}
	if observer == nil {
		observer = noopObserver{}
	}
	return &Cleaner{
		repo: repo, config: config, logger: logger, observer: observer,
		stopCh: make(chan struct{}), started: make(chan struct{}), done: make(chan struct{}),
	}
}

func (c *Cleaner) Start(ctx context.Context) {
	close(c.started)
	defer close(c.done)

	c.logger.Info("retention cleaner started",
		"attempt_body_retention", c.config.AttemptBodyRetention,
		"event_retention", c.config.EventRetention,
		"interval", c.config.Interval,
		"batch_size", c.config.BatchSize,
	)
	c.runAndObserve(ctx)

	ticker := time.NewTicker(c.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.runAndObserve(ctx)
		}
	}
}

func (c *Cleaner) Stop() {
	c.stopOnce.Do(func() { close(c.stopCh) })
	select {
	case <-c.started:
		<-c.done
	default:
	}
}

func (c *Cleaner) RunOnce(ctx context.Context, now time.Time) error {
	redacted, err := c.repo.RedactAttemptBodies(ctx, now.Add(-c.config.AttemptBodyRetention), c.config.BatchSize)
	if err != nil {
		return err
	}
	c.observer.AttemptBodiesRedacted(redacted)

	deleted, err := c.repo.DeleteTerminalEvents(ctx, now.Add(-c.config.EventRetention), c.config.BatchSize)
	if err != nil {
		return err
	}
	c.observer.TerminalEventsDeleted(deleted)
	c.logger.Info("retention cleanup completed", "attempt_bodies_redacted", redacted, "terminal_events_deleted", deleted)
	return nil
}

func (c *Cleaner) runAndObserve(ctx context.Context) {
	startedAt := time.Now()
	if err := c.RunOnce(ctx, startedAt); err != nil {
		c.observer.CycleFailed()
		c.logger.Error("retention cleanup failed", "error", err)
		return
	}
	c.observer.CycleCompleted(time.Since(startedAt), time.Now())
}
