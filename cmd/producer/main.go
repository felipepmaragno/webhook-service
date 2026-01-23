// Producer for load testing - generates events directly to Kafka.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/felipemaragno/dispatch/internal/kafka"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Flags
	count := flag.Int("count", 100000, "Number of events to produce")
	eventType := flag.String("type", "loadtest.event", "Event type (use 'multi' for distributed, 'onesub' for 1 per subscription)")
	numTypes := flag.Int("types", 100, "Number of different event types when using 'multi'")
	typePrefix := flag.String("prefix", "load.event", "Event type prefix for 'onesub' mode")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		logger.Info("received shutdown signal")
		cancel()
	}()

	// Kafka configuration
	kafkaBrokers := strings.Split(os.Getenv("KAFKA_BROKERS"), ",")
	if len(kafkaBrokers) == 0 || kafkaBrokers[0] == "" {
		kafkaBrokers = []string{"localhost:9092"}
	}

	kafkaTopic := os.Getenv("KAFKA_TOPIC")
	if kafkaTopic == "" {
		kafkaTopic = "events.pending"
	}

	logger.Info("starting load test producer",
		"brokers", kafkaBrokers,
		"topic", kafkaTopic,
		"count", *count,
		"event_type", *eventType,
		"num_types", *numTypes,
	)

	producer := kafka.NewLoadTestProducer(kafkaBrokers, kafkaTopic, logger)
	defer func() { _ = producer.Close() }()

	start := time.Now()

	var err error
	switch *eventType {
	case "multi":
		// Distribute events across multiple types (for multi-subscription testing)
		err = producer.ProduceMultiTypeEvents(ctx, *count, *numTypes)
	case "onesub":
		// 1 event per subscription (count = number of subscriptions)
		err = producer.ProduceOnePerSubscription(ctx, *count, *typePrefix)
	default:
		err = producer.ProduceEvents(ctx, *count, *eventType)
	}
	if err != nil {
		logger.Error("failed to produce events", "error", err)
		os.Exit(1)
	}

	duration := time.Since(start)
	rate := float64(*count) / duration.Seconds()

	logger.Info("load test complete",
		"events", *count,
		"duration", duration,
		"rate", rate,
	)
}
