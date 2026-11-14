package database

import (
	"context"
	"fmt"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgx/v4"
)

// DualPoolManager manages separate pools for reads and writes.
type DualPoolManager struct {
	readPool   *AdaptivePool
	writePool  *AdaptivePool
	writeQueue *OrderedWriteQueue

	config *DynamicPoolConfig
	tracer *trace.Tracer

	// The current emulator is carried in context.Context (see WithEmulatorID)
	// instead of thread-local state.
}

// NewDualPoolManager creates a manager with separate read and write pools.
func NewDualPoolManager(dbURL string, emulatorCount int, tracer *trace.Tracer) (*DualPoolManager, error) {
	// Compute the dynamic configuration
	config := CalculateDynamicConfig(emulatorCount)

	tracer.Info("[DualPoolManager] Calculated configuration for %d emulators:", emulatorCount)
	tracer.Info("%s", config.String())
	tracer.Info("[DualPoolManager] Recommended PostgreSQL max_connections: %d",
		config.GetRecommendedPostgresMaxConnections())

	// Read pool (large, for many concurrent queries)
	readPool, err := createPoolWithConfig(dbURL, config, true, tracer)
	if err != nil {
		return nil, fmt.Errorf("failed to create read pool: %w", err)
	}

	// Write pool (small, used only by the queue)
	writePool, err := createPoolWithConfig(dbURL, config, false, tracer)
	if err != nil {
		readPool.Close()
		return nil, fmt.Errorf("failed to create write pool: %w", err)
	}

	// Write queue
	writeQueue := NewOrderedWriteQueue(writePool, config, tracer)

	dpm := &DualPoolManager{
		readPool:   readPool,
		writePool:  writePool,
		writeQueue: writeQueue,
		config:     config,
		tracer:     tracer,
	}

	tracer.Info("[DualPoolManager] Initialization complete")
	return dpm, nil
}

// createPoolWithConfig creates a read or write pool from the dynamic config.
func createPoolWithConfig(dbURL string, config *DynamicPoolConfig, isReadPool bool, tracer *trace.Tracer) (*AdaptivePool, error) {
	var poolType string
	var adaptiveConfig *AdaptiveConfig

	if isReadPool {
		poolType = "READ"
		adaptiveConfig = &AdaptiveConfig{
			MinConns:      config.ReadPoolMin,
			MaxConns:      config.ReadPoolMax,
			StatsInterval: 2 * time.Minute,
		}
	} else {
		poolType = "WRITE"
		adaptiveConfig = &AdaptiveConfig{
			MinConns:      config.WritePoolMin,
			MaxConns:      config.WritePoolMax,
			StatsInterval: 5 * time.Minute,
		}
	}

	tracer.Info("[DualPoolManager] Creating %s pool (min: %d, max: %d)",
		poolType, adaptiveConfig.MinConns, adaptiveConfig.MaxConns)

	pool, err := NewAdaptivePoolWithConfig(dbURL, adaptiveConfig)
	if err != nil {
		return nil, err
	}

	return pool, nil
}

// ============ DBInterface Implementation ============

// Query runs a read query on the read pool.
func (dpm *DualPoolManager) Query(ctx context.Context, query string, args ...interface{}) (pgx.Rows, error) {
	return dpm.readPool.Query(ctx, query, args...)
}

// QueryRow runs a single-row read query on the read pool.
func (dpm *DualPoolManager) QueryRow(ctx context.Context, query string, args ...interface{}) pgx.Row {
	return dpm.readPool.QueryRow(ctx, query, args...)
}

// Exec runs a write through the write queue.
func (dpm *DualPoolManager) Exec(ctx context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	// Read the emulatorID from the context
	emulatorID := GetEmulatorIDFromContext(ctx)
	if emulatorID == 0 {
		// No emulatorID: run directly (administrative operations)
		return dpm.writePool.Exec(ctx, query, args...)
	}

	// Run through the ordered queue
	return dpm.writeQueue.Exec(ctx, emulatorID, query, args...)
}

// Begin starts a transaction on the write pool.
func (dpm *DualPoolManager) Begin(ctx context.Context) (pgx.Tx, error) {
	// Transactions need direct control and cannot go through the queue,
	// so they use the write pool directly
	return dpm.writePool.Begin(ctx)
}

// Ping checks the health of both pools.
func (dpm *DualPoolManager) Ping(ctx context.Context) error {
	if err := dpm.readPool.Ping(ctx); err != nil {
		return fmt.Errorf("read pool ping failed: %w", err)
	}
	if err := dpm.writePool.Ping(ctx); err != nil {
		return fmt.Errorf("write pool ping failed: %w", err)
	}
	return nil
}

// Close shuts down both pools and the queue.
func (dpm *DualPoolManager) Close() {
	dpm.tracer.Info("[DualPoolManager] Shutting down...")

	// Stop the write queue first (flushes pending writes)
	dpm.writeQueue.Stop()

	// Close pools
	dpm.writePool.Close()
	dpm.readPool.Close()

	dpm.tracer.Info("[DualPoolManager] Shutdown complete")
}

// ============ Specific Methods ============

// ExecDirect writes directly to the pool, bypassing the queue.
// Used for administrative operations that don't need ordering.
func (dpm *DualPoolManager) ExecDirect(ctx context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	return dpm.writePool.Exec(ctx, query, args...)
}

// GetStats returns statistics for both pools and the queue.
func (dpm *DualPoolManager) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"read_pool":   dpm.readPool.GetStats(),
		"write_pool":  dpm.writePool.GetStats(),
		"write_queue": dpm.writeQueue.GetMetrics(),
		"config":      dpm.getConfigSummary(),
		"timestamp":   time.Now(),
	}
}

// getConfigSummary returns a configuration summary.
func (dpm *DualPoolManager) getConfigSummary() map[string]interface{} {
	return map[string]interface{}{
		"emulator_count":   dpm.config.EmulatorCount,
		"read_pool_max":    dpm.config.ReadPoolMax,
		"write_pool_max":   dpm.config.WritePoolMax,
		"write_workers":    dpm.config.WriteWorkers,
		"write_queue_size": dpm.config.WriteQueueSize,
		"write_batch_size": dpm.config.WriteBatchSize,
		"query_timeout":    dpm.config.QueryTimeout.String(),
		"scaling_factors":  dpm.config.GetScalingFactors(),
	}
}

// ============ Context Helpers ============

type contextKey int

const emulatorIDKey contextKey = 1

// WithEmulatorID stores emulatorID in the context.
func WithEmulatorID(ctx context.Context, emulatorID int) context.Context {
	return context.WithValue(ctx, emulatorIDKey, emulatorID)
}

// GetEmulatorIDFromContext reads emulatorID from the context.
func GetEmulatorIDFromContext(ctx context.Context) int {
	if id, ok := ctx.Value(emulatorIDKey).(int); ok {
		return id
	}
	return 0
}

// ============ Adaptive Pool Helper ============

// NewAdaptivePoolWithConfig creates an AdaptivePool with a custom configuration.
// It delegates to NewAdaptivePoolWithCustomConfig in adaptive_pool.go.
func NewAdaptivePoolWithConfig(dbURL string, config *AdaptiveConfig) (*AdaptivePool, error) {
	return NewAdaptivePoolWithCustomConfig(dbURL, config)
}
