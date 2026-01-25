// Benchmark script for measuring real throughput
// Usage: go run scripts/benchmark.go -subs 1000 -events 1
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type CreateSubscriptionRequest struct {
	ID         string   `json:"id"`
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types"`
}

type CreateEventRequest struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Source string          `json:"source"`
	Data   json.RawMessage `json:"data"`
}

func main() {
	numSubs := flag.Int("subs", 1000, "Number of subscriptions")
	eventsPerSub := flag.Int("events", 1, "Events per subscription")
	apiURL := flag.String("api", "http://localhost:8080", "API URL")
	receiverURL := flag.String("receiver", "http://receiver:9999/webhook", "Webhook receiver URL")
	concurrency := flag.Int("concurrency", 100, "Concurrent HTTP requests")
	waitTime := flag.Int("wait", 30, "Seconds to wait for delivery")
	flag.Parse()

	totalEvents := *numSubs * *eventsPerSub

	fmt.Println("==============================================")
	fmt.Println("  Dispatch Throughput Benchmark")
	fmt.Println("==============================================")
	fmt.Printf("  Subscriptions: %d\n", *numSubs)
	fmt.Printf("  Events per subscription: %d\n", *eventsPerSub)
	fmt.Printf("  Total events: %d\n", totalEvents)
	fmt.Printf("  Concurrency: %d\n", *concurrency)
	fmt.Println("==============================================")
	fmt.Println()

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        *concurrency * 2,
			MaxIdleConnsPerHost: *concurrency * 2,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// Check API health
	fmt.Print("[1/4] Checking API health... ")
	resp, err := client.Get(*apiURL + "/health")
	if err != nil {
		log.Fatalf("API not reachable: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Fatalf("API unhealthy: %d", resp.StatusCode)
	}
	fmt.Println("OK")

	// Create subscriptions in parallel
	fmt.Printf("[2/4] Creating %d subscriptions... ", *numSubs)
	subStart := time.Now()
	createSubscriptions(client, *apiURL, *receiverURL, *numSubs, *concurrency)
	subDuration := time.Since(subStart)
	fmt.Printf("done (%.2fs, %.0f/s)\n", subDuration.Seconds(), float64(*numSubs)/subDuration.Seconds())

	// Send events in parallel
	fmt.Printf("[3/4] Sending %d events... ", totalEvents)
	eventStart := time.Now()
	successCount, failCount := sendEvents(client, *apiURL, *numSubs, *eventsPerSub, *concurrency)
	eventDuration := time.Since(eventStart)
	ingestRate := float64(successCount) / eventDuration.Seconds()
	fmt.Printf("done (%.2fs, %.0f events/s)\n", eventDuration.Seconds(), ingestRate)
	if failCount > 0 {
		fmt.Printf("  WARNING: %d events failed to send\n", failCount)
	}

	// Wait for delivery
	fmt.Printf("[4/4] Waiting %ds for delivery...\n", *waitTime)
	time.Sleep(time.Duration(*waitTime) * time.Second)

	endTime := time.Now()
	totalDuration := endTime.Sub(subStart)

	// Print results
	fmt.Println()
	fmt.Println("==============================================")
	fmt.Println("  BENCHMARK RESULTS")
	fmt.Println("==============================================")
	fmt.Println()
	fmt.Println("  Ingestion (API -> Kafka):")
	fmt.Printf("    Events sent: %d\n", successCount)
	fmt.Printf("    Duration: %.2fs\n", eventDuration.Seconds())
	fmt.Printf("    Throughput: %.0f events/s\n", ingestRate)
	fmt.Println()
	fmt.Println("  End-to-end:")
	fmt.Printf("    Total duration: %.2fs\n", totalDuration.Seconds())
	fmt.Printf("    Throughput: %.0f events/s\n", float64(successCount)/totalDuration.Seconds())
	fmt.Println()
	fmt.Println("==============================================")
}

func createSubscriptions(client *http.Client, apiURL, receiverURL string, numSubs, concurrency int) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	for i := 1; i <= numSubs; i++ {
		wg.Add(1)
		sem <- struct{}{}

		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			sub := CreateSubscriptionRequest{
				ID:         fmt.Sprintf("bench-sub-%d", idx),
				URL:        receiverURL,
				EventTypes: []string{fmt.Sprintf("bench.event.%d", idx)},
			}

			body, _ := json.Marshal(sub)
			req, _ := http.NewRequest("POST", apiURL+"/subscriptions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(i)
	}

	wg.Wait()
}

func sendEvents(client *http.Client, apiURL string, numSubs, eventsPerSub, concurrency int) (int64, int64) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	var successCount, failCount int64

	for subIdx := 1; subIdx <= numSubs; subIdx++ {
		for evtIdx := 1; evtIdx <= eventsPerSub; evtIdx++ {
			wg.Add(1)
			sem <- struct{}{}

			go func(s, e int) {
				defer wg.Done()
				defer func() { <-sem }()

				event := CreateEventRequest{
					ID:     fmt.Sprintf("bench-evt-%d-%d", s, e),
					Type:   fmt.Sprintf("bench.event.%d", s),
					Source: "benchmark",
					Data:   json.RawMessage(fmt.Sprintf(`{"sub":%d,"seq":%d}`, s, e)),
				}

				body, _ := json.Marshal(event)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				req, _ := http.NewRequestWithContext(ctx, "POST", apiURL+"/events", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")

				resp, err := client.Do(req)
				if err != nil {
					atomic.AddInt64(&failCount, 1)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()

				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					atomic.AddInt64(&successCount, 1)
				} else {
					atomic.AddInt64(&failCount, 1)
				}
			}(subIdx, evtIdx)
		}
	}

	wg.Wait()
	return successCount, failCount
}
