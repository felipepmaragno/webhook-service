package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync/atomic"
	"time"
)

var (
	requestCount uint64
	successCount uint64
	failureCount uint64
)

func main() {
	port := flag.Int("port", 9999, "port to listen on")
	fail := flag.Bool("fail", false, "return 500 errors")
	failRate := flag.Float64("fail-rate", 0, "random failure rate (0.0-1.0)")
	latency := flag.Int("latency", 100, "average response latency in ms")
	jitter := flag.Int("jitter", 20, "latency jitter in ms (+/-)")
	quiet := flag.Bool("quiet", false, "suppress per-request logging")
	flag.Parse()

	// Stats reporter
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		for range ticker.C {
			total := atomic.LoadUint64(&requestCount)
			success := atomic.LoadUint64(&successCount)
			failures := atomic.LoadUint64(&failureCount)
			if total > 0 {
				fmt.Printf("[STATS] Total: %d | Success: %d | Failures: %d | Rate: %.1f req/s\n",
					total, success, failures, float64(total)/5.0)
				atomic.StoreUint64(&requestCount, 0)
				atomic.StoreUint64(&successCount, 0)
				atomic.StoreUint64(&failureCount, 0)
			}
		}
	}()

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(&requestCount, 1)

		// Simulate realistic latency (~100ms average)
		delay := time.Duration(*latency) * time.Millisecond
		if *jitter > 0 {
			jitterMs := rand.Intn(*jitter*2) - *jitter
			delay += time.Duration(jitterMs) * time.Millisecond
		}
		time.Sleep(delay)

		body, _ := io.ReadAll(r.Body)

		// Determine if this request should fail
		shouldFail := *fail || (*failRate > 0 && rand.Float64() < *failRate)

		if !*quiet {
			fmt.Printf("[REQ] Event-ID: %s | Type: %s | Latency: %v | Fail: %v\n",
				r.Header.Get("X-Dispatch-Event-ID"),
				r.Header.Get("X-Dispatch-Event-Type"),
				delay,
				shouldFail)
			if len(body) > 0 && len(body) < 200 {
				fmt.Printf("      Body: %s\n", string(body))
			}
		}

		if shouldFail {
			atomic.AddUint64(&failureCount, 1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("simulated failure"))
		} else {
			atomic.AddUint64(&successCount, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		}
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Webhook receiver listening on %s\n", addr)
	fmt.Printf("  Latency: %dms (+/- %dms)\n", *latency, *jitter)
	fmt.Printf("  Fail mode: %v | Fail rate: %.1f%%\n", *fail, *failRate*100)
	fmt.Printf("  Quiet: %v\n", *quiet)
	log.Fatal(http.ListenAndServe(addr, nil))
}
