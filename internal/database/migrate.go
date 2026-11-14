package database

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"sort"

	"github.com/rphpires/facial-device-emulator/internal/trace"
)

// migration is a VNNN_*.sql file embedded in the binary.
type migration struct {
	Version string // "V002"
	Name    string // "V002_add_something.sql"
	SQL     string
}

// migrationFileName matches VNNN_anything.sql. Any other file in the
// folder is ignored — the folder also holds the baseline and may one day
// hold a README.
var migrationFileName = regexp.MustCompile(`^(V\d{3})_.+\.sql$`)

// pendingMigrations returns, in version order, the migrations in the FS
// that are not yet in applied.
func pendingMigrations(fsys fs.FS, applied map[string]bool) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded migrations: %w", err)
	}

	var pend []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFileName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		version := m[1]
		if applied[version] {
			continue
		}
		content, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", e.Name(), err)
		}
		pend = append(pend, migration{Version: version, Name: e.Name(), SQL: string(content)})
	}

	sort.Slice(pend, func(i, j int) bool { return pend[i].Version < pend[j].Version })
	return pend, nil
}

// baselineVersion is the migration the validator runs on its own
// (assets/migrations/V001_create_emulator_schema.sql). The runner never
// executes it; it only records it, so it never tries to recreate a schema
// that already exists.
const baselineVersion = "V001"

// ApplyMigrations applies, in order, every embedded migration not yet
// recorded in service.schema_migrations. Each one runs in its own
// transaction: a half-migrated database is worse than a migration that
// failed as a whole.
//
// It runs after ValidateDatabaseOnStartup, which creates the baseline on an
// empty database. If the validator was told to reset a mismatched database,
// service.schema_migrations goes with the dropped schemas and everything is
// reapplied from scratch, which is why migrations are written to be
// idempotent.
func ApplyMigrations(ctx context.Context, db DBInterface, fsys fs.FS) error {
	_, err := db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS service.schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
		)`)
	if err != nil {
		return fmt.Errorf("failed to create service.schema_migrations: %w", err)
	}

	_, err = db.Exec(ctx,
		`INSERT INTO service.schema_migrations (version) VALUES ('`+baselineVersion+`')
		 ON CONFLICT (version) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("failed to record baseline: %w", err)
	}

	applied := map[string]bool{baselineVersion: true}
	rows, err := db.Query(ctx, "SELECT version FROM service.schema_migrations")
	if err != nil {
		return fmt.Errorf("failed to read applied migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("failed to read applied version: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate applied migrations: %w", err)
	}

	pend, err := pendingMigrations(fsys, applied)
	if err != nil {
		return err
	}

	tracer := trace.NewTracer()
	for _, mig := range pend {
		tracer.Info("Applying migration %s", mig.Name)

		tx, err := db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("failed to begin transaction for %s: %w", mig.Name, err)
		}

		if _, err := tx.Exec(ctx, mig.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("failed to apply %s: %w", mig.Name, err)
		}

		if _, err := tx.Exec(ctx,
			"INSERT INTO service.schema_migrations (version) VALUES ($1)", mig.Version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("failed to record %s: %w", mig.Name, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("failed to commit %s: %w", mig.Name, err)
		}

		tracer.Info("Migration %s applied", mig.Version)
	}

	return nil
}
