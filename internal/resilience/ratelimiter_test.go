package resilience

import (
	"sync"
	"testing"
	"time"
)

func TestRateLimiterManager_Allow(t *testing.T) {
	config := RateLimiterConfig{
		RequestsPerSecond: 10,
		BurstSize:         2,
	}
	manager := NewRateLimiterManager(config)

	subID := "sub_test"

	if !manager.Allow(subID) {
		t.Error("first request should be allowed")
	}
	if !manager.Allow(subID) {
		t.Error("second request should be allowed (burst)")
	}

	if manager.Allow(subID) {
		t.Error("third request should be rate limited")
	}
}

func TestRateLimiterManager_ConcurrentAccess(t *testing.T) {
	config := DefaultRateLimiterConfig()
	manager := NewRateLimiterManager(config)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			subID := "sub_concurrent"
			manager.Allow(subID)
		}(i)
	}
	wg.Wait()
}

func TestRateLimiterManager_SetRate(t *testing.T) {
	config := DefaultRateLimiterConfig()
	manager := NewRateLimiterManager(config)

	subID := "sub_custom"
	manager.SetRate(subID, 1, 1)

	if !manager.Allow(subID) {
		t.Error("first request should be allowed")
	}
	if manager.Allow(subID) {
		t.Error("second request should be rate limited with rate=1")
	}
}

func TestRateLimiterManager_Remove(t *testing.T) {
	config := RateLimiterConfig{
		RequestsPerSecond: 1,
		BurstSize:         1,
	}
	manager := NewRateLimiterManager(config)

	subID := "sub_remove"

	manager.Allow(subID)
	if manager.Allow(subID) {
		t.Error("should be rate limited")
	}

	manager.Remove(subID)

	if !manager.Allow(subID) {
		t.Error("after remove, new limiter should allow")
	}
}

func TestRateLimiterManager_Wait(t *testing.T) {
	config := RateLimiterConfig{
		RequestsPerSecond: 10,
		BurstSize:         1,
	}
	manager := NewRateLimiterManager(config)

	subID := "sub_wait"

	manager.Allow(subID)

	delay := manager.Wait(subID)
	if delay == 0 {
		t.Error("should have a delay after burst exhausted")
	}
	if delay > 200*time.Millisecond {
		t.Errorf("delay too long: %v", delay)
	}
}
