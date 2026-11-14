package database

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
)

// AdaptivePool is a pgxpool sized from the fleet, with usage statistics.
//
// The pool is created once with MaxConns set to its ceiling and never
// replaced. pgxpool opens connections lazily, on demand, and closes idle
// ones after MaxConnIdleTime, so the number of open connections follows the
// load between MinConns and MaxConns without recreating the pool. (The name
// is historical: an earlier version swapped the pool at runtime to "grow"
// it, which raced with every query.)
type AdaptivePool struct {
	pool    *pgxpool.Pool
	config  *AdaptiveConfig
	metrics poolMetrics

	stop      chan struct{}
	done      sync.WaitGroup
	closeOnce sync.Once
}

// poolMetrics collects usage statistics for the current period.
type poolMetrics struct {
	peakConnections int32
	queryCount      int64
	queryNanos      int64
	// Average of the last completed period, served by GetStats until the
	// current period has queries of its own.
	lastAvgQueryMs int64
}

// AdaptiveConfig configures an AdaptivePool.
type AdaptiveConfig struct {
	MinConns int32
	// MaxConns is the ceiling. Connections above MinConns are opened only
	// when queries need them.
	MaxConns int32
	// StatsInterval is the period of peak_connections and
	// avg_query_duration_ms. Zero means two minutes.
	StatsInterval time.Duration
}

// NewAdaptivePool creates a pool sized for emulatorCount devices.
func NewAdaptivePool(dbURL string, emulatorCount int) (*AdaptivePool, error) {
	return NewAdaptivePoolWithCustomConfig(dbURL, calculateInitialConfig(emulatorCount))
}

// NewAdaptivePoolWithCustomConfig creates a pool with a custom configuration.
func NewAdaptivePoolWithCustomConfig(dbURL string, config *AdaptiveConfig) (*AdaptivePool, error) {
	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, err
	}

	poolConfig.MinConns = config.MinConns
	poolConfig.MaxConns = config.MaxConns
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.ConnectConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, err
	}

	ap := &AdaptivePool{
		pool:   pool,
		config: config,
		stop:   make(chan struct{}),
	}

	interval := config.StatsInterval
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	ap.done.Add(1)
	go ap.rollStats(interval)

	return ap, nil
}

// calculateInitialConfig derives the configuration from the emulator count.
func calculateInitialConfig(emulatorCount int) *AdaptiveConfig {
	// Each emulator uses at most 2 concurrent connections, plus a few for
	// administrative operations. The ceiling keeps the 3x headroom the
	// earlier runtime expansion allowed.
	estimatedPeak := int32(emulatorCount*2 + 5)

	return &AdaptiveConfig{
		MinConns:      5,
		MaxConns:      estimatedPeak * 3,
		StatsInterval: 2 * time.Minute,
	}
}

// Query implements DBInterface.
func (ap *AdaptivePool) Query(ctx context.Context, query string, args ...interface{}) (pgx.Rows, error) {
	start := time.Now()
	rows, err := ap.pool.Query(ctx, query, args...)
	ap.recordQuery(time.Since(start))
	return rows, err
}

// QueryRow implements DBInterface.
func (ap *AdaptivePool) QueryRow(ctx context.Context, query string, args ...interface{}) pgx.Row {
	start := time.Now()
	row := ap.pool.QueryRow(ctx, query, args...)
	ap.recordQuery(time.Since(start))
	return row
}

// Exec implements DBInterface.
func (ap *AdaptivePool) Exec(ctx context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	start := time.Now()
	tag, err := ap.pool.Exec(ctx, query, args...)
	ap.recordQuery(time.Since(start))
	return tag, err
}

// Begin implements DBInterface.
func (ap *AdaptivePool) Begin(ctx context.Context) (pgx.Tx, error) {
	return ap.pool.Begin(ctx)
}

// Ping implements DBInterface.
func (ap *AdaptivePool) Ping(ctx context.Context) error {
	return ap.pool.Ping(ctx)
}

// Close stops the statistics goroutine and closes the pool. It is safe to
// call more than once.
func (ap *AdaptivePool) Close() {
	ap.closeOnce.Do(func() {
		close(ap.stop)
		ap.done.Wait()
		ap.pool.Close()
	})
}

// recordQuery records metrics for one query.
func (ap *AdaptivePool) recordQuery(duration time.Duration) {
	atomic.AddInt64(&ap.metrics.queryCount, 1)
	atomic.AddInt64(&ap.metrics.queryNanos, int64(duration))

	current := ap.pool.Stat().AcquiredConns()
	for {
		peak := atomic.LoadInt32(&ap.metrics.peakConnections)
		if current <= peak || atomic.CompareAndSwapInt32(&ap.metrics.peakConnections, peak, current) {
			return
		}
	}
}

// rollStats closes a statistics period every interval until Close.
func (ap *AdaptivePool) rollStats(interval time.Duration) {
	defer ap.done.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ap.stop:
			return
		case <-ticker.C:
			count := atomic.SwapInt64(&ap.metrics.queryCount, 0)
			nanos := atomic.SwapInt64(&ap.metrics.queryNanos, 0)
			if count > 0 {
				atomic.StoreInt64(&ap.metrics.lastAvgQueryMs, time.Duration(nanos/count).Milliseconds())
			}
			atomic.StoreInt32(&ap.metrics.peakConnections, 0)
		}
	}
}

// GetStats returns the current pool statistics. The monitoring health
// checker reads "acquired_conns" and "max_conns" (both int32).
func (ap *AdaptivePool) GetStats() map[string]interface{} {
	stats := ap.pool.Stat()

	avg := atomic.LoadInt64(&ap.metrics.lastAvgQueryMs)
	if count := atomic.LoadInt64(&ap.metrics.queryCount); count > 0 {
		avg = time.Duration(atomic.LoadInt64(&ap.metrics.queryNanos) / count).Milliseconds()
	}

	return map[string]interface{}{
		"max_conns":             stats.MaxConns(),
		"min_conns":             ap.config.MinConns,
		"total_conns":           stats.TotalConns(),
		"acquired_conns":        stats.AcquiredConns(),
		"idle_conns":            stats.IdleConns(),
		"constructing_conns":    stats.ConstructingConns(),
		"peak_connections":      atomic.LoadInt32(&ap.metrics.peakConnections),
		"avg_query_duration_ms": avg,
		// Acquires that found no idle connection and had to wait for one to
		// be opened or released. A count that keeps growing fast while
		// acquired_conns sits at max_conns means the pool is too small.
		"empty_acquire_count": stats.EmptyAcquireCount(),
	}
}
