package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// emptyStats is a minimal getStatsFn for test checkers: the content does not
// matter for these tests, it only has to be non-nil.
func emptyStats() map[string]interface{} { return map[string]interface{}{} }

// fakeChecker implements HealthChecker with a fixed result, to test the
// HealthMonitor status aggregation without real databases.
type fakeChecker struct {
	name   string
	status HealthStatus
}

func (c *fakeChecker) Name() string { return c.name }
func (c *fakeChecker) Check(ctx context.Context) ComponentHealth {
	return ComponentHealth{Name: c.name, Status: c.status}
}

// TestCheckAll_OverallStatus covers the HealthMonitor aggregation rule: only
// an "unhealthy" component may bring the overall status down (and, through
// quickHealthCheck, the HTTP status to 503). "degraded" must not. This is the
// regression that would make every fresh install (optional dependency not
// configured) report a failure from the startup scripts forever.
func TestCheckAll_OverallStatus(t *testing.T) {
	cases := []struct {
		name     string
		register func(hm *HealthMonitor)
		want     HealthStatus
	}{
		{
			name: "only healthy checkers reports healthy",
			register: func(hm *HealthMonitor) {
				hm.RegisterChecker(&fakeChecker{name: "a", status: HealthStatusHealthy})
				hm.RegisterChecker(&fakeChecker{name: "b", status: HealthStatusHealthy})
			},
			want: HealthStatusHealthy,
		},
		{
			// Mirrors the real optional_db checker
			// (NewDatabaseHealthCheckerWithFailureStatus with
			// HealthStatusDegraded), built the same way as in
			// internal/handlers/handlers.go.
			name: "optional_db with failed ping reports degraded, not unhealthy",
			register: func(hm *HealthMonitor) {
				hm.RegisterChecker(&fakeChecker{name: "service_db", status: HealthStatusHealthy})
				hm.RegisterChecker(NewDatabaseHealthCheckerWithFailureStatus(
					"optional_db",
					func(ctx context.Context) error { return errors.New("connection refused") },
					emptyStats,
					HealthStatusDegraded,
				))
			},
			want: HealthStatusDegraded,
		},
		{
			// Mirrors service_db/emulator_db (NewDatabaseHealthChecker with
			// the default failureStatus): a required dependency whose failed
			// ping must still bring the overall status down.
			name: "required database with failed ping reports unhealthy",
			register: func(hm *HealthMonitor) {
				hm.RegisterChecker(NewDatabaseHealthChecker(
					"service_db",
					func(ctx context.Context) error { return errors.New("connection refused") },
					emptyStats,
				))
			},
			want: HealthStatusUnhealthy,
		},
		{
			// Mixed: a degraded optional dependency must not mask a required
			// dependency that is actually down.
			name: "required database down wins over degraded optional_db",
			register: func(hm *HealthMonitor) {
				hm.RegisterChecker(NewDatabaseHealthChecker(
					"service_db",
					func(ctx context.Context) error { return errors.New("connection refused") },
					emptyStats,
				))
				hm.RegisterChecker(NewDatabaseHealthCheckerWithFailureStatus(
					"optional_db",
					func(ctx context.Context) error { return errors.New("connection refused") },
					emptyStats,
					HealthStatusDegraded,
				))
			},
			want: HealthStatusUnhealthy,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hm := NewHealthMonitor()
			c.register(hm)

			result := hm.CheckAll(context.Background())
			status, ok := result["status"].(HealthStatus)
			if !ok {
				t.Fatalf("result[\"status\"] is not a HealthStatus: %#v", result["status"])
			}
			if status != c.want {
				t.Errorf("overall status = %q, want %q", status, c.want)
			}
		})
	}
}

// TestCheckAll_OptionalDbComponentShowsDegraded checks the individual
// component, not just the aggregate: the payload must include optional_db
// with status "degraded" when the ping fails, so anyone looking at
// /monitoring/health can see why without guessing from the overall status.
func TestCheckAll_OptionalDbComponentShowsDegraded(t *testing.T) {
	hm := NewHealthMonitor()
	hm.RegisterChecker(NewDatabaseHealthCheckerWithFailureStatus(
		"optional_db",
		func(ctx context.Context) error { return errors.New("dial tcp: no such host") },
		emptyStats,
		HealthStatusDegraded,
	))

	result := hm.CheckAll(context.Background())
	components, ok := result["components"].(map[string]ComponentHealth)
	if !ok {
		t.Fatalf("result[\"components\"] is not a map[string]ComponentHealth: %#v", result["components"])
	}

	opt, exists := components["optional_db"]
	if !exists {
		t.Fatalf("optional_db component missing from the payload")
	}
	if opt.Status != HealthStatusDegraded {
		t.Errorf("optional_db.status = %q, want %q", opt.Status, HealthStatusDegraded)
	}
	if opt.Message == "" {
		t.Errorf("optional_db.message is empty; it should explain why the ping failed")
	}
}

// TestNewDatabaseHealthChecker_DefaultsToUnhealthy ensures the original
// constructor (used by service_db and emulator_db) still reports "unhealthy"
// on a failed ping by default, i.e. giving optional_db a custom status did
// not soften the required dependencies.
func TestNewDatabaseHealthChecker_DefaultsToUnhealthy(t *testing.T) {
	checker := NewDatabaseHealthChecker(
		"service_db",
		func(ctx context.Context) error { return errors.New("connection refused") },
		emptyStats,
	)

	health := checker.Check(context.Background())
	if health.Status != HealthStatusUnhealthy {
		t.Errorf("status = %q, want %q: NewDatabaseHealthChecker must not soften required dependencies", health.Status, HealthStatusUnhealthy)
	}
}

// TestDatabaseHealthChecker_PoolUsage verifies the checker reads the keys the
// pools actually report (acquired_conns/max_conns, flat or nested per pool)
// and always sets a status on a successful ping.
func TestDatabaseHealthChecker_PoolUsage(t *testing.T) {
	ping := func(ctx context.Context) error { return nil }
	cases := []struct {
		name  string
		stats map[string]interface{}
		want  HealthStatus
	}{
		{"single pool low usage", map[string]interface{}{"acquired_conns": int32(1), "max_conns": int32(10)}, HealthStatusHealthy},
		{"single pool high usage", map[string]interface{}{"acquired_conns": int32(10), "max_conns": int32(10)}, HealthStatusDegraded},
		{"dual pool busiest wins", map[string]interface{}{
			"read_pool":  map[string]interface{}{"acquired_conns": int32(1), "max_conns": int32(10)},
			"write_pool": map[string]interface{}{"acquired_conns": int32(19), "max_conns": int32(20)},
		}, HealthStatusDegraded},
		{"dual pool healthy", map[string]interface{}{
			"read_pool":  map[string]interface{}{"acquired_conns": int32(1), "max_conns": int32(10)},
			"write_pool": map[string]interface{}{"acquired_conns": int32(6), "max_conns": int32(10)},
		}, HealthStatusHealthy},
		{"unknown stats shape", map[string]interface{}{"status": "unknown"}, HealthStatusHealthy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stats := tc.stats
			c := NewDatabaseHealthChecker("db", ping, func() map[string]interface{} { return stats })
			got := c.Check(context.Background())
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q (message %q)", got.Status, tc.want, got.Message)
			}
		})
	}
}

// TestComponentHealth_DurationMs verifies duration_ms is serialized in
// milliseconds, not as time.Duration's nanoseconds.
func TestComponentHealth_DurationMs(t *testing.T) {
	b, err := json.Marshal(ComponentHealth{Name: "x", Status: HealthStatusHealthy, Duration: 1500 * time.Microsecond})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if got := out["duration_ms"]; got != 1.5 {
		t.Fatalf("duration_ms = %v, want 1.5 (json %s)", got, b)
	}
	if out["name"] != "x" || out["status"] != "healthy" {
		t.Fatalf("other fields lost: %s", b)
	}
}
