package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rphpires/facial-device-emulator/assets"
	"github.com/rphpires/facial-device-emulator/internal/config"
	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
)

// ownedSchemas are the schemas the service creates and, only when asked
// to, drops.
var ownedSchemas = []string{"emulator", "service"}

// expectedTables are the tables created by the V001 baseline.
var expectedTables = map[string][]string{
	"service": {
		"devices",
	},
	"emulator": {
		"device_settings",
		"dahua_cards",
		"dahua_faces",
		"hikvision_users",
		"hikvision_cards",
		"hikvision_faces",
		"hikvision_fingers",
	},
}

// criticalColumns are columns whose absence means the tables come from
// something other than this service.
var criticalColumns = map[string]map[string][]string{
	"service": {
		"devices": {"id", "name", "ip_address", "port", "model", "status"},
	},
	"emulator": {
		"device_settings":   {"device_id", "cfg_id", "value"},
		"dahua_cards":       {"device_id", "rec_no", "user_id", "card_no"},
		"hikvision_users":   {"device_id", "employee_no", "name"},
		"hikvision_fingers": {"device_id", "employee_no", "data_index"},
	},
}

// ErrStructureMismatch is returned when the database holds part of the
// expected structure, or tables with missing columns, and resetting was
// not allowed.
var ErrStructureMismatch = errors.New("database structure does not match")

// structureState is the outcome of inspecting the database.
type structureState int

const (
	structureEmpty    structureState = iota // none of the expected tables exist
	structureComplete                       // every table and critical column exists
	structurePartial                        // anything in between
)

// classify turns the inspection counts into a state.
func classify(found, total int, problems []string) structureState {
	switch {
	case found == 0:
		return structureEmpty
	case found == total && len(problems) == 0:
		return structureComplete
	default:
		return structurePartial
	}
}

// DatabaseValidator checks the database structure on startup.
type DatabaseValidator struct {
	pool   *pgxpool.Pool
	tracer *trace.Tracer
}

// NewDatabaseValidator connects with the same URL the pools use.
func NewDatabaseValidator(cfg config.DatabaseConfig) (*DatabaseValidator, error) {
	dbURL, err := cfg.PostgresURL()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.Connect(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect for validation: %w", err)
	}

	return &DatabaseValidator{pool: pool, tracer: trace.NewTracer()}, nil
}

// Close closes the validator connection.
func (v *DatabaseValidator) Close() {
	if v.pool != nil {
		v.pool.Close()
	}
}

// Validate creates the baseline structure on an empty database, accepts a
// complete one, and refuses a partial or mismatched one. With reset set it
// drops the service and emulator schemas (all their data) and recreates
// them instead of refusing.
func (v *DatabaseValidator) Validate(reset bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	v.tracer.Info("Validating database structure...")

	state, problems, err := v.inspect(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect structure: %w", err)
	}

	switch state {
	case structureComplete:
		v.tracer.Info("Database structure is correct")
		return nil

	case structureEmpty:
		v.tracer.Info("Database is empty, creating the service and emulator schemas")
		if err := v.createBaseline(ctx); err != nil {
			return fmt.Errorf("failed to create database structure: %w", err)
		}
		v.tracer.Info("Database structure created")
		return nil
	}

	for _, p := range problems {
		v.tracer.Warning("Database structure: %s", p)
	}

	if !reset {
		return mismatchError(problems)
	}

	v.tracer.Warning("Reset requested: dropping the service and emulator schemas and all their data")
	if err := v.recreate(ctx); err != nil {
		return fmt.Errorf("failed to recreate database: %w", err)
	}
	v.tracer.Info("Database recreated")
	return nil
}

func mismatchError(problems []string) error {
	return fmt.Errorf("%w: %s. Point the service at an empty database, fix the schema by hand, "+
		"or start it with -reset-db (or service_db.reset_on_mismatch: true) to DROP the "+
		"service and emulator schemas with ALL their data and recreate them",
		ErrStructureMismatch, strings.Join(problems, "; "))
}

// inspect classifies the current structure and lists what is missing.
func (v *DatabaseValidator) inspect(ctx context.Context) (structureState, []string, error) {
	var problems []string
	found, total := 0, 0

	for _, schema := range ownedSchemas {
		for _, table := range expectedTables[schema] {
			total++
			exists, err := v.tableExists(ctx, schema, table)
			if err != nil {
				return 0, nil, err
			}
			if !exists {
				problems = append(problems, fmt.Sprintf("table %s.%s is missing", schema, table))
				continue
			}
			found++
			for _, column := range criticalColumns[schema][table] {
				ok, err := v.columnExists(ctx, schema, table, column)
				if err != nil {
					return 0, nil, err
				}
				if !ok {
					problems = append(problems, fmt.Sprintf("column %s.%s.%s is missing", schema, table, column))
				}
			}
		}
	}

	state := classify(found, total, problems)
	if state != structurePartial {
		problems = nil
	}
	return state, problems, nil
}

// tableExists reports whether a table exists.
func (v *DatabaseValidator) tableExists(ctx context.Context, schemaName, tableName string) (bool, error) {
	var exists bool
	err := v.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)",
		schemaName, tableName).Scan(&exists)
	return exists, err
}

// columnExists reports whether a column exists.
func (v *DatabaseValidator) columnExists(ctx context.Context, schemaName, tableName, columnName string) (bool, error) {
	var exists bool
	err := v.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3)",
		schemaName, tableName, columnName).Scan(&exists)
	return exists, err
}

// createBaseline runs the embedded V001 script in one transaction, so a
// failure leaves the database as it was.
func (v *DatabaseValidator) createBaseline(ctx context.Context) error {
	return v.inTx(ctx, func(tx pgx.Tx) error {
		return execBaseline(ctx, tx)
	})
}

// recreate drops the owned schemas and runs the baseline, all in one
// transaction (PostgreSQL DDL is transactional).
func (v *DatabaseValidator) recreate(ctx context.Context) error {
	return v.inTx(ctx, func(tx pgx.Tx) error {
		for _, schema := range ownedSchemas {
			if _, err := tx.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
				return fmt.Errorf("failed to drop schema %s: %w", schema, err)
			}
			v.tracer.Info("Dropped schema %s", schema)
		}
		return execBaseline(ctx, tx)
	})
}

func execBaseline(ctx context.Context, tx pgx.Tx) error {
	script, err := assets.MigrationSQL()
	if err != nil {
		return fmt.Errorf("failed to read embedded migration: %w", err)
	}
	if _, err := tx.Exec(ctx, string(script)); err != nil {
		return fmt.Errorf("failed to run baseline migration: %w", err)
	}
	return nil
}

func (v *DatabaseValidator) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := v.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// ValidateDatabaseOnStartup is the entry point called at startup. reset
// allows dropping and recreating a mismatched structure; main passes the
// -reset-db flag, and service_db.reset_on_mismatch enables it too.
func ValidateDatabaseOnStartup(cfg config.DatabaseConfig, reset bool) error {
	validator, err := NewDatabaseValidator(cfg)
	if err != nil {
		return fmt.Errorf("failed to create validator: %w", err)
	}
	defer validator.Close()

	return validator.Validate(reset || cfg.ResetOnMismatch)
}
