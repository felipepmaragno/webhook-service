package app

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/felipemaragno/dispatch/internal/observability"
)

func TestRetentionMetricsObserverRecordsEverySignal(t *testing.T) {
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = registry
	prometheus.DefaultGatherer = registry
	metrics := observability.NewMetrics("retention_observer_test")
	observer := retentionMetricsObserver{metrics: metrics}
	completedAt := time.Unix(1234, 0)

	observer.AttemptBodiesRedacted(3)
	observer.TerminalEventsDeleted(2)
	observer.CycleFailed()
	observer.CycleCompleted(250*time.Millisecond, completedAt)

	assertGatheredMetric(t, registry, "retention_observer_test_retention_attempt_bodies_redacted_total", 3)
	assertGatheredMetric(t, registry, "retention_observer_test_retention_terminal_events_deleted_total", 2)
	assertGatheredMetric(t, registry, "retention_observer_test_retention_cleanup_failures_total", 1)
	assertGatheredMetric(t, registry, "retention_observer_test_retention_last_success_timestamp_seconds", 1234)
	assertGatheredMetric(t, registry, "retention_observer_test_retention_cleanup_duration_seconds", 1)
}

func assertGatheredMetric(t *testing.T, registry *prometheus.Registry, name string, want float64) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name || len(family.Metric) == 0 {
			continue
		}
		metric := family.Metric[0]
		var got float64
		switch {
		case metric.Counter != nil:
			got = metric.Counter.GetValue()
		case metric.Gauge != nil:
			got = metric.Gauge.GetValue()
		case metric.Histogram != nil:
			got = float64(metric.Histogram.GetSampleCount())
		}
		if got != want {
			t.Fatalf("metric %s = %v, want %v", name, got, want)
		}
		return
	}
	t.Fatalf("metric %s not found", name)
}
