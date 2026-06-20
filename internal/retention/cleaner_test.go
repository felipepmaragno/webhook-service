package retention

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeRepository struct {
	mu           sync.Mutex
	order        []string
	redacted     int64
	deleted      int64
	redactErr    error
	deleteErr    error
	redactBefore time.Time
	deleteBefore time.Time
	limit        int
}

func (f *fakeRepository) RedactAttemptBodies(_ context.Context, before time.Time, limit int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "redact")
	f.redactBefore = before
	f.limit = limit
	return f.redacted, f.redactErr
}

func (f *fakeRepository) DeleteTerminalEvents(_ context.Context, before time.Time, limit int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "delete")
	f.deleteBefore = before
	f.limit = limit
	return f.deleted, f.deleteErr
}

type recordingObserver struct {
	redacted int64
	deleted  int64
	failed   int
	complete int
}

func (o *recordingObserver) AttemptBodiesRedacted(count int64)       { o.redacted += count }
func (o *recordingObserver) TerminalEventsDeleted(count int64)       { o.deleted += count }
func (o *recordingObserver) CycleFailed()                            { o.failed++ }
func (o *recordingObserver) CycleCompleted(time.Duration, time.Time) { o.complete++ }

func TestCleaner_RunOnceOrdersAndBoundsCleanup(t *testing.T) {
	repo := &fakeRepository{redacted: 3, deleted: 2}
	observer := &recordingObserver{}
	config := Config{
		AttemptBodyRetention: 24 * time.Hour,
		EventRetention:       48 * time.Hour,
		Interval:             time.Hour,
		BatchSize:            25,
	}
	cleaner := NewCleaner(repo, config, slog.Default(), observer)
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if err := cleaner.RunOnce(context.Background(), now); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(repo.order) != 2 || repo.order[0] != "redact" || repo.order[1] != "delete" {
		t.Fatalf("cleanup order = %v", repo.order)
	}
	if !repo.redactBefore.Equal(now.Add(-24*time.Hour)) || !repo.deleteBefore.Equal(now.Add(-48*time.Hour)) {
		t.Fatalf("unexpected cutoffs: redact=%v delete=%v", repo.redactBefore, repo.deleteBefore)
	}
	if repo.limit != 25 || observer.redacted != 3 || observer.deleted != 2 {
		t.Fatalf("limit=%d observer=%+v", repo.limit, observer)
	}
}

func TestCleaner_RunOnceStopsAfterFailure(t *testing.T) {
	repo := &fakeRepository{redactErr: errors.New("database unavailable")}
	cleaner := NewCleaner(repo, DefaultConfig(), slog.Default(), nil)

	if err := cleaner.RunOnce(context.Background(), time.Now()); err == nil {
		t.Fatal("expected cleanup error")
	}
	if len(repo.order) != 1 || repo.order[0] != "redact" {
		t.Fatalf("cleanup continued after redaction failure: %v", repo.order)
	}
}

func TestCleaner_StartReportsFailureAndStops(t *testing.T) {
	repo := &fakeRepository{deleteErr: errors.New("database unavailable")}
	observer := &recordingObserver{}
	cleaner := NewCleaner(repo, Config{Interval: time.Hour}, slog.Default(), observer)
	done := make(chan struct{})
	go func() {
		cleaner.Start(context.Background())
		close(done)
	}()

	select {
	case <-cleaner.started:
	case <-time.After(time.Second):
		t.Fatal("cleaner did not start")
	}
	cleaner.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleaner did not stop")
	}
	if observer.failed != 1 || observer.complete != 0 {
		t.Fatalf("observer = %+v", observer)
	}
}

type flakyRepository struct {
	mu    sync.Mutex
	calls int
}

func (f *flakyRepository) RedactAttemptBodies(_ context.Context, _ time.Time, _ int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return 0, errors.New("temporary database error")
	}
	return 1, nil
}

func (f *flakyRepository) DeleteTerminalEvents(context.Context, time.Time, int) (int64, error) {
	return 1, nil
}

type signalObserver struct {
	failed    chan struct{}
	completed chan struct{}
}

func (o *signalObserver) AttemptBodiesRedacted(int64) {}
func (o *signalObserver) TerminalEventsDeleted(int64) {}
func (o *signalObserver) CycleFailed() {
	select {
	case o.failed <- struct{}{}:
	default:
	}
}
func (o *signalObserver) CycleCompleted(time.Duration, time.Time) {
	select {
	case o.completed <- struct{}{}:
	default:
	}
}

func TestCleaner_ContinuesAfterCycleFailure(t *testing.T) {
	observer := &signalObserver{failed: make(chan struct{}, 1), completed: make(chan struct{}, 1)}
	cleaner := NewCleaner(&flakyRepository{}, Config{Interval: 5 * time.Millisecond}, slog.Default(), observer)
	go cleaner.Start(context.Background())
	defer cleaner.Stop()

	select {
	case <-observer.failed:
	case <-time.After(time.Second):
		t.Fatal("first cleanup failure was not observed")
	}
	select {
	case <-observer.completed:
	case <-time.After(time.Second):
		t.Fatal("cleaner did not recover on a later cycle")
	}
}

type blockingRepository struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingRepository) RedactAttemptBodies(context.Context, time.Time, int) (int64, error) {
	close(b.started)
	<-b.release
	return 0, nil
}

func (b *blockingRepository) DeleteTerminalEvents(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func TestCleaner_StopWaitsForInflightCycle(t *testing.T) {
	repo := &blockingRepository{started: make(chan struct{}), release: make(chan struct{})}
	cleaner := NewCleaner(repo, DefaultConfig(), slog.Default(), nil)
	go cleaner.Start(context.Background())
	<-repo.started
	stopped := make(chan struct{})
	go func() {
		cleaner.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop returned before in-flight cleanup completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after cleanup completed")
	}
}
