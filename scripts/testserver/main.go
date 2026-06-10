package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// live-adjustable state stored as atomics so /control races are safe
var (
	atomicFailRate  uint64 // math.Float64bits of fail rate (0.0–1.0)
	atomicLatencyMs uint64 // latency in ms

	requestCount uint64
	successCount uint64
	failureCount uint64
)

func getFailRate() float64  { return math.Float64frombits(atomic.LoadUint64(&atomicFailRate)) }
func getLatencyMs() int     { return int(atomic.LoadUint64(&atomicLatencyMs)) }
func setFailRate(v float64) { atomic.StoreUint64(&atomicFailRate, math.Float64bits(v)) }
func setLatencyMs(v int)    { atomic.StoreUint64(&atomicLatencyMs, uint64(v)) }

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func main() {
	port := flag.Int("port", envInt("PORT", 9000), "port to listen on")
	fail := flag.Bool("fail", false, "always return 500 errors")
	failRate := flag.Float64("fail-rate", envFloat("FAIL_RATE", 0), "initial random failure rate (0.0-1.0)")
	latency := flag.Int("latency", envInt("LATENCY_MS", 50), "initial average response latency in ms")
	jitter := flag.Int("jitter", envInt("JITTER_MS", 20), "latency jitter in ms (+/-)")
	quiet := flag.Bool("quiet", false, "suppress per-request logging")
	flag.Parse()

	// Initialise atomics from flags/env
	setFailRate(*failRate)
	setLatencyMs(*latency)

	// Stats reporter
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		for range ticker.C {
			total := atomic.SwapUint64(&requestCount, 0)
			success := atomic.SwapUint64(&successCount, 0)
			failures := atomic.SwapUint64(&failureCount, 0)
			if total > 0 {
				fmt.Printf("[STATS] total=%d success=%d failures=%d rate=%.1f/s fail_rate=%.0f%% latency=%dms\n",
					total, success, failures, float64(total)/5.0,
					getFailRate()*100, getLatencyMs())
			}
		}
	}()

	// /control — live-adjust fail rate and latency without restart.
	// POST {"fail_rate": 0.7, "latency_ms": 100}
	http.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			FailRate  *float64 `json:"fail_rate"`
			LatencyMs *int     `json:"latency_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.FailRate != nil {
			setFailRate(*req.FailRate)
			fmt.Printf("[CONTROL] fail_rate=%.2f\n", *req.FailRate)
		}
		if req.LatencyMs != nil {
			setLatencyMs(*req.LatencyMs)
			fmt.Printf("[CONTROL] latency_ms=%d\n", *req.LatencyMs)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// / — accept webhooks on any path
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}

		atomic.AddUint64(&requestCount, 1)

		// Simulate latency
		lat := getLatencyMs()
		delay := time.Duration(lat) * time.Millisecond
		if *jitter > 0 {
			jitterMs := rand.Intn(*jitter*2) - *jitter
			delay += time.Duration(jitterMs) * time.Millisecond
		}
		time.Sleep(delay)

		body, _ := io.ReadAll(r.Body)

		shouldFail := *fail || (getFailRate() > 0 && rand.Float64() < getFailRate())

		if !*quiet {
			fmt.Printf("[REQ] event_id=%s type=%s latency=%v fail=%v\n",
				r.Header.Get("X-Event-ID"),
				r.Header.Get("X-Event-Type"),
				delay,
				shouldFail)
			if len(body) > 0 && len(body) < 200 {
				fmt.Printf("      body=%s\n", string(body))
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

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("receiver listening on %s  fail_rate=%.0f%%  latency=%dms (+/-%dms)\n",
		addr, getFailRate()*100, getLatencyMs(), *jitter)
	log.Fatal(http.ListenAndServe(addr, nil))
}
