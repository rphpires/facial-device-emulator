package database

import (
	"fmt"
	"math"
	"time"
)

// DynamicPoolConfig holds pool settings tuned to the number of emulators.
type DynamicPoolConfig struct {
	EmulatorCount int

	// Read Pool Configuration
	ReadPoolMin int32
	ReadPoolMax int32

	// Write Pool Configuration
	WritePoolMin   int32
	WritePoolMax   int32
	WriteWorkers   int
	WriteQueueSize int
	WriteBatchSize int
	WriteFlushMs   int

	// Connection Settings
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration

	// Timeouts
	QueryTimeout       time.Duration
	TransactionTimeout time.Duration
}

// CalculateDynamicConfig computes the pool configuration for a given number of emulators.
func CalculateDynamicConfig(emulatorCount int) *DynamicPoolConfig {
	if emulatorCount <= 0 {
		emulatorCount = 10 // Reasonable minimum
	}

	config := &DynamicPoolConfig{
		EmulatorCount: emulatorCount,
	}

	// ============ READ POOL ============
	// Formula: each emulator may run ~2-3 concurrent reads during sync,
	// plus a 20% buffer for administrative operations and the watchdog
	estimatedReadConcurrency := float64(emulatorCount) * 2.5
	readPoolEstimate := int32(math.Ceil(estimatedReadConcurrency * 1.2))

	config.ReadPoolMin = maxInt32(5, readPoolEstimate/10) // 10% as the minimum
	config.ReadPoolMax = maxInt32(20, readPoolEstimate)   // Computed maximum

	// ============ WRITE POOL ============
	// The write pool is MUCH smaller because it sits behind a central queue
	// Formula: sqrt(emulators) workers, clamped to 5..50
	writersFloat := math.Sqrt(float64(emulatorCount))
	config.WriteWorkers = clampInt(int(math.Ceil(writersFloat)), 5, 50)

	// Connections = workers + 20% buffer
	config.WritePoolMin = int32(config.WriteWorkers)
	config.WritePoolMax = int32(math.Ceil(float64(config.WriteWorkers) * 1.2))

	// ============ WRITE QUEUE ============
	// Formula: each emulator may have ~5 pending operations during a mass sync
	// Queue = emulators * 5, clamped to 1000..50000
	queueSizeFloat := float64(emulatorCount) * 5
	config.WriteQueueSize = clampInt(int(queueSizeFloat), 1000, 50000)

	// Batch size grows logarithmically with the number of emulators
	// Formula: 10 * log10(emulators), clamped to 10..500
	batchSizeFloat := 10 * math.Log10(float64(emulatorCount))
	config.WriteBatchSize = clampInt(int(math.Ceil(batchSizeFloat)), 10, 500)

	// Flush interval shrinks as emulators grow (more activity = faster flush)
	// Formula: 5000ms / log10(emulators), clamped to 100ms..5000ms
	flushMsFloat := 5000 / math.Max(1, math.Log10(float64(emulatorCount)))
	config.WriteFlushMs = clampInt(int(math.Ceil(flushMsFloat)), 100, 5000)

	// ============ CONNECTION SETTINGS ============
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 10 * time.Minute
	config.HealthCheckPeriod = 1 * time.Minute

	// ============ TIMEOUTS ============
	// Timeouts grow with the number of emulators (more contention = more time)
	baseQueryTimeout := 5.0 // seconds
	timeoutMultiplier := 1.0 + (math.Log10(float64(emulatorCount)) / 10)
	queryTimeoutSeconds := baseQueryTimeout * timeoutMultiplier

	config.QueryTimeout = time.Duration(queryTimeoutSeconds * float64(time.Second))
	config.TransactionTimeout = config.QueryTimeout * 2 // Transactions may run longer

	return config
}

// String returns a one-line summary of the configuration, for the log.
func (c *DynamicPoolConfig) String() string {
	return fmt.Sprintf(
		"emulators=%d read_pool=%d..%d write_pool=%d..%d write_workers=%d "+
			"queue_size=%d batch_size=%d flush_ms=%d query_timeout=%v tx_timeout=%v",
		c.EmulatorCount,
		c.ReadPoolMin, c.ReadPoolMax,
		c.WritePoolMin, c.WritePoolMax, c.WriteWorkers,
		c.WriteQueueSize, c.WriteBatchSize, c.WriteFlushMs,
		c.QueryTimeout, c.TransactionTimeout,
	)
}

// GetRecommendedPostgresMaxConnections returns the recommended PostgreSQL max_connections.
func (c *DynamicPoolConfig) GetRecommendedPostgresMaxConnections() int {
	// Total connections + buffer for admin/superuser and other applications
	totalAppConns := c.ReadPoolMax + c.WritePoolMax
	recommended := int(math.Ceil(float64(totalAppConns) * 1.3))

	// PostgreSQL defaults to 100; never recommend less than that
	if recommended < 100 {
		recommended = 100
	}

	return recommended
}

// Utility functions
func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// GetScalingFactors returns scaling factors for analysis.
func (c *DynamicPoolConfig) GetScalingFactors() map[string]float64 {
	return map[string]float64{
		"read_pool_per_emulator":    float64(c.ReadPoolMax) / float64(c.EmulatorCount),
		"write_workers_per_100_emu": (float64(c.WriteWorkers) / float64(c.EmulatorCount)) * 100,
		"queue_depth_per_emulator":  float64(c.WriteQueueSize) / float64(c.EmulatorCount),
		"total_conns_per_emulator":  float64(c.ReadPoolMax+c.WritePoolMax) / float64(c.EmulatorCount),
	}
}
