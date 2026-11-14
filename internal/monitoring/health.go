package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// HealthStatus is the health status of a component.
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusDegraded  HealthStatus = "degraded"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

// ComponentHealth is the health of a single component.
type ComponentHealth struct {
	Name       string                 `json:"name"`
	Status     HealthStatus           `json:"status"`
	Message    string                 `json:"message,omitempty"`
	LastCheck  time.Time              `json:"last_check"`
	Duration   time.Duration          `json:"duration_ms"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	ErrorCount int64                  `json:"error_count"`
}

// MarshalJSON renders Duration as milliseconds (with sub-millisecond
// precision), as the `duration_ms` key promises. Without it, time.Duration
// would be serialized as an integer number of nanoseconds.
func (c ComponentHealth) MarshalJSON() ([]byte, error) {
	type alias ComponentHealth
	return json.Marshal(struct {
		alias
		Duration float64 `json:"duration_ms"`
	}{
		alias:    alias(c),
		Duration: durationMs(c.Duration),
	})
}

// durationMs converts d to milliseconds as a float.
func durationMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// HealthChecker is implemented by components that can be health-checked.
type HealthChecker interface {
	Name() string
	Check(ctx context.Context) ComponentHealth
}

// HealthMonitor runs the health checks of all registered components.
type HealthMonitor struct {
	checkers []HealthChecker
	mu       sync.RWMutex

	// Global metrics
	totalChecks   atomic.Int64
	totalFailures atomic.Int64
	lastCheckTime atomic.Value // time.Time

	// Result cache
	cachedResults     map[string]ComponentHealth
	cacheExpiry       time.Time
	cacheDuration     time.Duration
	cacheResultsMutex sync.RWMutex
}

// NewHealthMonitor creates a health monitor.
func NewHealthMonitor() *HealthMonitor {
	hm := &HealthMonitor{
		checkers:      make([]HealthChecker, 0),
		cachedResults: make(map[string]ComponentHealth),
		cacheDuration: 5 * time.Second, // Cache results for 5 seconds
	}
	hm.lastCheckTime.Store(time.Time{})
	return hm
}

// RegisterChecker adds a checker to the monitor.
func (hm *HealthMonitor) RegisterChecker(checker HealthChecker) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.checkers = append(hm.checkers, checker)
}

// aggregateOverallStatus combines each component's status into a single
// overall status: any "unhealthy" component makes the whole result unhealthy
// (which quickHealthCheck turns into HTTP 503); "degraded" raises the overall
// status to degraded only if nothing is unhealthy; otherwise it stays
// healthy. This is the ONLY definition of the aggregation rule: CheckAll and
// getCachedResponse (the path QuickCheck serves most of the time, thanks to
// the 5s cache) both call it instead of each reimplementing the loop, so a
// regression cannot slip into just one of the two copies and go unnoticed
// whenever the response comes from the cache.
func aggregateOverallStatus(components map[string]ComponentHealth) (HealthStatus, int) {
	overallStatus := HealthStatusHealthy
	unhealthyCount := 0

	for _, result := range components {
		if result.Status == HealthStatusUnhealthy {
			unhealthyCount++
			overallStatus = HealthStatusUnhealthy
		} else if result.Status == HealthStatusDegraded && overallStatus == HealthStatusHealthy {
			overallStatus = HealthStatusDegraded
		}
	}

	return overallStatus, unhealthyCount
}

// CheckAll checks the health of every component.
func (hm *HealthMonitor) CheckAll(ctx context.Context) map[string]interface{} {
	// Serve from cache when possible
	if hm.isCacheValid() {
		return hm.getCachedResponse()
	}

	start := time.Now()
	hm.totalChecks.Add(1)

	// Run the checks in parallel
	resultsChan := make(chan ComponentHealth, len(hm.checkers))
	var wg sync.WaitGroup

	hm.mu.RLock()
	checkers := make([]HealthChecker, len(hm.checkers))
	copy(checkers, hm.checkers)
	hm.mu.RUnlock()

	for _, checker := range checkers {
		wg.Add(1)
		go func(c HealthChecker) {
			defer wg.Done()

			// Per-check timeout
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			result := c.Check(checkCtx)
			resultsChan <- result
		}(checker)
	}

	// Wait for every check
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	components := make(map[string]ComponentHealth)
	for result := range resultsChan {
		components[result.Name] = result
	}

	overallStatus, unhealthyCount := aggregateOverallStatus(components)
	if unhealthyCount > 0 {
		hm.totalFailures.Add(int64(unhealthyCount))
	}

	// System metrics
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	response := map[string]interface{}{
		"status":     overallStatus,
		"timestamp":  time.Now().UTC(),
		"duration":   time.Since(start).Milliseconds(),
		"components": components,
		"metrics": map[string]interface{}{
			"total_checks":    hm.totalChecks.Load(),
			"total_failures":  hm.totalFailures.Load(),
			"unhealthy_count": unhealthyCount,
			"goroutines":      runtime.NumGoroutine(),
			"memory_alloc_mb": memStats.Alloc / 1024 / 1024,
			"memory_sys_mb":   memStats.Sys / 1024 / 1024,
			"gc_pause_ms":     float64(memStats.PauseNs[(memStats.NumGC+255)%256]) / 1000000,
			"num_gc":          memStats.NumGC,
		},
	}

	// Update the cache
	hm.updateCache(components)
	hm.lastCheckTime.Store(time.Now())

	return response
}

// QuickCheck returns a fast status, served from cache when available.
func (hm *HealthMonitor) QuickCheck() map[string]interface{} {
	if hm.isCacheValid() {
		return hm.getCachedResponse()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hm.CheckAll(ctx)
}

// isCacheValid reports whether the cache is still fresh.
func (hm *HealthMonitor) isCacheValid() bool {
	hm.cacheResultsMutex.RLock()
	defer hm.cacheResultsMutex.RUnlock()
	return time.Now().Before(hm.cacheExpiry) && len(hm.cachedResults) > 0
}

// getCachedResponse builds the response from cached results.
func (hm *HealthMonitor) getCachedResponse() map[string]interface{} {
	hm.cacheResultsMutex.RLock()
	defer hm.cacheResultsMutex.RUnlock()

	overallStatus, unhealthyCount := aggregateOverallStatus(hm.cachedResults)

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return map[string]interface{}{
		"status":     overallStatus,
		"timestamp":  time.Now().UTC(),
		"cached":     true,
		"components": hm.cachedResults,
		"metrics": map[string]interface{}{
			"total_checks":    hm.totalChecks.Load(),
			"total_failures":  hm.totalFailures.Load(),
			"unhealthy_count": unhealthyCount,
			"goroutines":      runtime.NumGoroutine(),
			"memory_alloc_mb": memStats.Alloc / 1024 / 1024,
		},
	}
}

// updateCache replaces the cached results.
func (hm *HealthMonitor) updateCache(results map[string]ComponentHealth) {
	hm.cacheResultsMutex.Lock()
	defer hm.cacheResultsMutex.Unlock()

	hm.cachedResults = results
	hm.cacheExpiry = time.Now().Add(hm.cacheDuration)
}

// DatabaseHealthChecker checks the health of a database connection.
type DatabaseHealthChecker struct {
	name          string
	pingFn        func(ctx context.Context) error
	getStatsFn    func() map[string]interface{}
	failureStatus HealthStatus
}

// NewDatabaseHealthChecker creates a database checker. A failed ping reports
// HealthStatusUnhealthy: use it for required dependencies (e.g. service_db,
// emulator_db) whose unavailability actually stops the application from
// working.
func NewDatabaseHealthChecker(name string, pingFn func(ctx context.Context) error, getStatsFn func() map[string]interface{}) *DatabaseHealthChecker {
	return NewDatabaseHealthCheckerWithFailureStatus(name, pingFn, getStatsFn, HealthStatusUnhealthy)
}

// NewDatabaseHealthCheckerWithFailureStatus is like NewDatabaseHealthChecker
// but lets the caller choose the status reported when the ping fails. Use
// HealthStatusDegraded for optional dependencies (e.g. optional_db) whose
// unavailability must not turn the overall status unhealthy (and, as a
// consequence, /monitoring/health/quick into HTTP 503): the application keeps
// working, only the features that depend on it are degraded.
func NewDatabaseHealthCheckerWithFailureStatus(name string, pingFn func(ctx context.Context) error, getStatsFn func() map[string]interface{}, failureStatus HealthStatus) *DatabaseHealthChecker {
	return &DatabaseHealthChecker{
		name:          name,
		pingFn:        pingFn,
		getStatsFn:    getStatsFn,
		failureStatus: failureStatus,
	}
}

// Name returns the component name.
func (d *DatabaseHealthChecker) Name() string {
	return d.name
}

// Check pings the database and rates pool usage and latency.
func (d *DatabaseHealthChecker) Check(ctx context.Context) ComponentHealth {
	start := time.Now()
	health := ComponentHealth{
		Name:      d.name,
		LastCheck: start,
	}

	// Ping
	err := d.pingFn(ctx)
	duration := time.Since(start)
	health.Duration = duration

	if err != nil {
		health.Status = d.failureStatus
		health.Message = fmt.Sprintf("Database ping failed: %v", err)
		health.ErrorCount = 1
		return health
	}

	// Pool statistics
	stats := d.getStatsFn()
	health.Metadata = stats

	// Rate status from pool usage
	health.Status = HealthStatusHealthy
	health.Message = "Optimal"
	if usage, ok := poolUsage(stats); ok {
		if usage > 90 {
			health.Status = HealthStatusDegraded
			health.Message = fmt.Sprintf("High connection usage: %.1f%%", usage)
		} else if usage > 50 {
			health.Message = fmt.Sprintf("Normal operation: %.1f%% usage", usage)
		}
	}

	// Rate latency
	if duration > 100*time.Millisecond {
		health.Status = HealthStatusDegraded
		health.Message = fmt.Sprintf("High latency: %dms", duration.Milliseconds())
	}

	return health
}

// poolUsage returns the connection usage percentage reported by pool stats.
// It understands both shapes produced by the database package: a single pool
// (AdaptivePool.GetStats: top-level "acquired_conns"/"max_conns") and a dual
// pool (DualPoolManager.GetStats: the same keys nested under "read_pool" and
// "write_pool"), in which case the busiest pool wins. ok is false when no
// usable figures are present.
func poolUsage(stats map[string]interface{}) (usage float64, ok bool) {
	acquired, okA := stats["acquired_conns"].(int32)
	maxConns, okM := stats["max_conns"].(int32)
	if okA && okM && maxConns > 0 {
		return float64(acquired) / float64(maxConns) * 100, true
	}
	for _, key := range []string{"read_pool", "write_pool"} {
		sub, isMap := stats[key].(map[string]interface{})
		if !isMap {
			continue
		}
		if u, subOK := poolUsage(sub); subOK && (!ok || u > usage) {
			usage, ok = u, true
		}
	}
	return usage, ok
}

// EmulatorHealthChecker checks the health of the emulators.
type EmulatorHealthChecker struct {
	getDevicesFn func() ([]map[string]interface{}, error)
}

// NewEmulatorHealthChecker creates an emulator checker.
func NewEmulatorHealthChecker(getDevicesFn func() ([]map[string]interface{}, error)) *EmulatorHealthChecker {
	return &EmulatorHealthChecker{
		getDevicesFn: getDevicesFn,
	}
}

// Name returns the component name.
func (e *EmulatorHealthChecker) Name() string {
	return "emulators"
}

// Check counts emulators by status.
func (e *EmulatorHealthChecker) Check(ctx context.Context) ComponentHealth {
	start := time.Now()
	health := ComponentHealth{
		Name:      "emulators",
		LastCheck: start,
		Metadata:  make(map[string]interface{}),
	}

	devices, err := e.getDevicesFn()
	duration := time.Since(start)
	health.Duration = duration

	if err != nil {
		health.Status = HealthStatusUnhealthy
		health.Message = fmt.Sprintf("Failed to get devices: %v", err)
		health.ErrorCount = 1
		return health
	}

	// Count by status
	totalDevices := len(devices)
	runningCount := 0
	stoppedCount := 0
	errorCount := 0

	for _, device := range devices {
		if status, ok := device["status"].(string); ok {
			switch status {
			case "running":
				runningCount++
			case "stopped":
				stoppedCount++
			case "error":
				errorCount++
			}
		}
	}

	health.Metadata["total_devices"] = totalDevices
	health.Metadata["running"] = runningCount
	health.Metadata["stopped"] = stoppedCount
	health.Metadata["errors"] = errorCount

	// Rate status
	if errorCount > totalDevices/10 { // More than 10% in error
		health.Status = HealthStatusDegraded
		health.Message = fmt.Sprintf("%d devices with errors", errorCount)
		health.ErrorCount = int64(errorCount)
	} else if runningCount == 0 && totalDevices > 0 {
		health.Status = HealthStatusDegraded
		health.Message = "No emulators running"
	} else {
		health.Status = HealthStatusHealthy
		health.Message = fmt.Sprintf("%d/%d emulators running", runningCount, totalDevices)
	}

	return health
}
