package database

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/config"
	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/jackc/pgx/v4/pgxpool"
)

var (
	serviceDBInterface  DBInterface
	emulatorDBInterface DBInterface

	serviceOnce  sync.Once
	emulatorOnce sync.Once

	// Whether to use DualPoolManager (set via environment variable)
	useDualPoolManager = os.Getenv("USE_DUAL_POOL_MANAGER") == "true"
)

// GetServiceDB returns the process-wide connection for the service schema.
func GetServiceDB(cfg config.DatabaseConfig) (DBInterface, error) {
	var err error
	serviceOnce.Do(func() {
		serviceDBInterface, err = openDB(cfg, "ServiceDB")
	})
	return serviceDBInterface, err
}

// GetEmulatorDB returns the process-wide connection for the emulator schema.
func GetEmulatorDB(cfg config.DatabaseConfig) (DBInterface, error) {
	var err error
	emulatorOnce.Do(func() {
		emulatorDBInterface, err = openDB(cfg, "EmulatorDB")
	})
	return emulatorDBInterface, err
}

// openDB opens an AdaptivePool, or a DualPoolManager when
// USE_DUAL_POOL_MANAGER=true, sized from the enabled device count.
func openDB(cfg config.DatabaseConfig, name string) (DBInterface, error) {
	tracer := trace.NewTracer()

	dbURL, err := cfg.PostgresURL()
	if err != nil {
		return nil, err
	}
	emulatorCount := getEstimatedEmulatorCount(dbURL)

	if useDualPoolManager {
		tracer.Info("Creating DualPoolManager for %s with %d emulators", name, emulatorCount)
		dpm, err := NewDualPoolManager(dbURL, emulatorCount, tracer)
		if err != nil {
			tracer.Error("Failed to create DualPoolManager for %s: %v", name, err)
			return nil, err
		}
		return dpm, nil
	}

	tracer.Info("Creating connection pool for %s with %d emulators", name, emulatorCount)
	pool, err := NewAdaptivePool(dbURL, emulatorCount)
	if err != nil {
		tracer.Error("Failed to create connection pool for %s: %v", name, err)
		return nil, err
	}
	return pool, nil
}

// CloseAll closes both process-wide connections.
func CloseAll() {
	for _, db := range []DBInterface{serviceDBInterface, emulatorDBInterface} {
		switch p := db.(type) {
		case *DualPoolManager:
			p.Close()
		case *AdaptivePool:
			p.Close()
		}
	}
}

func getEstimatedEmulatorCount(dbURL string) int {
	// Try to count the existing devices in the database;
	// fall back to a conservative estimate on failure.
	tracer := trace.NewTracer()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tempPool, err := pgxpool.Connect(ctx, dbURL)
	if err != nil {
		tracer.Warning("Could not connect to count devices, using default estimate: %v", err)
		return 50 // Conservative estimate
	}
	defer tempPool.Close()

	var count int
	err = tempPool.QueryRow(ctx, "SELECT COUNT(*) FROM service.devices WHERE enabled = 1").Scan(&count)
	if err != nil {
		tracer.Warning("Could not count devices, using default estimate: %v", err)
		return 50 // Conservative estimate
	}

	if count == 0 {
		count = 20 // Development minimum
	}

	tracer.Info("Found %d enabled devices in database", count)
	return count
}
