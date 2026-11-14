package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/monitoring"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// fixedChecker is a monitoring.HealthChecker with a fixed result, used to
// build degraded/unhealthy scenarios without real checkers or a full
// Handler: quickHealthCheck and advancedHealthCheck only read
// h.healthMonitor, so *Handler{healthMonitor: hm} is enough.
type fixedChecker struct {
	name   string
	status monitoring.HealthStatus
}

func (c *fixedChecker) Name() string { return c.name }
func (c *fixedChecker) Check(ctx context.Context) monitoring.ComponentHealth {
	return monitoring.ComponentHealth{Name: c.name, Status: c.status}
}

// TestQuickHealthCheck_HTTPContract and TestAdvancedHealthCheck_HTTPContract
// test the real HTTP contract that start scripts and container probes rely
// on ("curl -sf http://localhost:7070/monitoring/health/quick"): degraded
// must answer 200 (a degraded optional component must not make the start
// scripts report failure) and unhealthy must answer 503 (otherwise a truly
// broken service would never be caught).
func TestQuickHealthCheck_HTTPContract(t *testing.T) {
	cases := []struct {
		name     string
		status   monitoring.HealthStatus
		wantHTTP int
	}{
		{"degraded answers 200", monitoring.HealthStatusDegraded, http.StatusOK},
		{"unhealthy answers 503", monitoring.HealthStatusUnhealthy, http.StatusServiceUnavailable},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hm := monitoring.NewHealthMonitor()
			hm.RegisterChecker(&fixedChecker{name: "component", status: c.status})

			h := &Handler{healthMonitor: hm}
			r := gin.New()
			r.GET("/monitoring/health/quick", h.quickHealthCheck)

			req := httptest.NewRequest(http.MethodGet, "/monitoring/health/quick", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != c.wantHTTP {
				t.Errorf("HTTP status = %d, want %d - body: %s", w.Code, c.wantHTTP, w.Body.String())
			}
		})
	}
}

func TestAdvancedHealthCheck_HTTPContract(t *testing.T) {
	cases := []struct {
		name     string
		status   monitoring.HealthStatus
		wantHTTP int
	}{
		{"degraded answers 200", monitoring.HealthStatusDegraded, http.StatusOK},
		{"unhealthy answers 503", monitoring.HealthStatusUnhealthy, http.StatusServiceUnavailable},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hm := monitoring.NewHealthMonitor()
			hm.RegisterChecker(&fixedChecker{name: "component", status: c.status})

			h := &Handler{healthMonitor: hm}
			r := gin.New()
			r.GET("/monitoring/health", h.advancedHealthCheck)

			req := httptest.NewRequest(http.MethodGet, "/monitoring/health", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != c.wantHTTP {
				t.Errorf("HTTP status = %d, want %d - body: %s", w.Code, c.wantHTTP, w.Body.String())
			}
		})
	}
}

// TestQuickHealthCheck_UsesCache confirms the second call really came from
// the cached path (getCachedResponse) and not from a new CheckAll, by
// checking the "cached": true marker only getCachedResponse writes. Most
// production responses come from the 5s cache, so a regression in the
// cached copy alone must not go unnoticed.
func TestQuickHealthCheck_UsesCache(t *testing.T) {
	hm := monitoring.NewHealthMonitor()
	hm.RegisterChecker(&fixedChecker{name: "optional_component", status: monitoring.HealthStatusDegraded})

	h := &Handler{healthMonitor: hm}
	r := gin.New()
	r.GET("/monitoring/health/quick", h.quickHealthCheck)

	// First call: the cache is empty, so QuickCheck falls through to
	// CheckAll, which fills the cache via updateCache.
	req1 := httptest.NewRequest(http.MethodGet, "/monitoring/health/quick", nil)
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: HTTP status = %d, want 200 - body: %s", w1.Code, w1.Body.String())
	}

	// Second call, inside the 5s cache window: must come from
	// getCachedResponse.
	req2 := httptest.NewRequest(http.MethodGet, "/monitoring/health/quick", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second call (cache): HTTP status = %d, want 200 - body: %s", w2.Code, w2.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal: %v - body: %s", err, w2.Body.String())
	}
	if cached, _ := body["cached"].(bool); !cached {
		t.Errorf("second call did not come from the cache (\"cached\" missing or false) - body: %s", w2.Body.String())
	}
	if status, _ := body["status"].(string); status != string(monitoring.HealthStatusDegraded) {
		t.Errorf("status in cached payload = %q, want %q", status, monitoring.HealthStatusDegraded)
	}
}
