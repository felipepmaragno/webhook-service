package resilience

import "testing"

func TestLocalSemaphoreManager_AcquireUsesProvidedLimit(t *testing.T) {
	manager := NewLocalSemaphoreManager(100)
	key := "sub-concurrency"

	if !manager.Acquire(key, 2) {
		t.Fatal("first acquire should succeed")
	}
	if !manager.Acquire(key, 2) {
		t.Fatal("second acquire should succeed")
	}
	if manager.Acquire(key, 2) {
		t.Fatal("third acquire should be rejected by provided limit")
	}

	manager.Release(key)
	if !manager.Acquire(key, 2) {
		t.Fatal("acquire after release should succeed")
	}
}
