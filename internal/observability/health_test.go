package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockDB struct {
	pingErr error
}

func (m *mockDB) Ping(ctx context.Context) error {
	return m.pingErr
}

func TestHealthHandler_Health(t *testing.T) {
	h := NewHealthHandler(ReadinessCheck{Name: "database", Checker: &mockDB{pingErr: errors.New("down")}})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	h.Health(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", resp.Status)
	}
}

func TestHealthHandler_Ready_AllHealthy(t *testing.T) {
	h := NewHealthHandler(
		ReadinessCheck{Name: "database", Checker: &mockDB{}},
		ReadinessCheck{Name: "kafka", Checker: &mockDB{}},
	)
	h.SetReady(true)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	h.Ready(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp ReadyResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", resp.Status)
	}

	if resp.Checks["app"] != "ok" {
		t.Errorf("expected app check 'ok', got %q", resp.Checks["app"])
	}

	if resp.Checks["database"] != "ok" {
		t.Errorf("expected database check 'ok', got %q", resp.Checks["database"])
	}
	if resp.Checks["kafka"] != "ok" {
		t.Errorf("expected kafka check 'ok', got %q", resp.Checks["kafka"])
	}
}

func TestHealthHandler_Ready_NotReady(t *testing.T) {
	h := NewHealthHandler(ReadinessCheck{Name: "database", Checker: &mockDB{}})
	h.SetReady(false)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	h.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}

	var resp ReadyResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "degraded" {
		t.Errorf("expected status 'degraded', got %q", resp.Status)
	}
}

func TestHealthHandler_Ready_DatabaseDown(t *testing.T) {
	h := NewHealthHandler(ReadinessCheck{Name: "database", Checker: &mockDB{pingErr: errors.New("postgres://secret@example")}})
	h.SetReady(true)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	h.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}

	var resp ReadyResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "degraded" {
		t.Errorf("expected status 'degraded', got %q", resp.Status)
	}

	if resp.Checks["database"] != "unavailable" {
		t.Errorf("expected database check error, got %q", resp.Checks["database"])
	}
}
