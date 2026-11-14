package database

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

// pendingMigrations is the part of the runner that can be tested without a
// database: given the embedded files and the applied set, which ones are
// missing and in what order.
func TestPendingMigrationsSortsAndSkipsApplied(t *testing.T) {
	fsys := fstest.MapFS{
		"V003_third.sql":    {Data: []byte("SELECT 3;")},
		"V001_baseline.sql": {Data: []byte("SELECT 1;")},
		"V002_second.sql":   {Data: []byte("SELECT 2;")},
		"readme.txt":        {Data: []byte("not a migration")},
	}

	pend, err := pendingMigrations(fsys, map[string]bool{"V001": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pend) != 2 {
		t.Fatalf("want 2 pending, got %d: %+v", len(pend), pend)
	}
	if pend[0].Version != "V002" || pend[1].Version != "V003" {
		t.Errorf("wrong order: %s, %s", pend[0].Version, pend[1].Version)
	}
	if pend[0].SQL != "SELECT 2;" {
		t.Errorf("V002 SQL: %q", pend[0].SQL)
	}
}

// Nothing pending is the normal case on every boot after the first.
func TestPendingMigrationsAllApplied(t *testing.T) {
	fsys := fstest.MapFS{"V001_baseline.sql": {Data: []byte("SELECT 1;")}}

	pend, err := pendingMigrations(fsys, map[string]bool{"V001": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pend) != 0 {
		t.Errorf("want none pending, got %d", len(pend))
	}
}

// A file outside the VNNN_ pattern is not a migration and must not become one.
func TestPendingMigrationsIgnoresNonMatchingNames(t *testing.T) {
	fsys := fstest.MapFS{
		"init.sql":       {Data: []byte("SELECT 0;")},
		"V010_tenth.sql": {Data: []byte("SELECT 10;")},
	}

	pend, err := pendingMigrations(fsys, map[string]bool{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pend) != 1 || pend[0].Version != "V010" {
		t.Errorf("want only V010, got %+v", pend)
	}
}

// On a database that already has its structure but no rows in
// schema_migrations, V001 is recorded as the baseline (the validator has
// already run it) and only V002 actually runs.
func TestApplyMigrationsRecordsBaselineAndAppliesRest(t *testing.T) {
	fsys := fstest.MapFS{
		"V001_baseline.sql": {Data: []byte("CREATE TABLE baseline();")},
		"V002_new.sql":      {Data: []byte("ALTER TABLE x ADD COLUMN y INT;")},
	}

	db := &fakeDB{tx: &fakeTx{}}
	// No version applied yet: the version query returns nothing, and the
	// baseline INSERT is what marks V001.
	db.queryRows = &fakeRows{}

	if err := ApplyMigrations(context.Background(), db, fsys); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	joined := strings.Join(db.execs, "\n")
	if !strings.Contains(joined, "service.schema_migrations") {
		t.Error("want the tracking table to be created")
	}
	if !strings.Contains(joined, "'V001'") {
		t.Error("want V001 recorded as the baseline")
	}

	txJoined := strings.Join(db.tx.execs, "\n")
	if !strings.Contains(txJoined, "ALTER TABLE x ADD COLUMN y INT;") {
		t.Errorf("want V002 executed in the transaction, got: %s", txJoined)
	}
	if strings.Contains(txJoined, "CREATE TABLE baseline();") {
		t.Error("V001 must not be executed by the runner — it belongs to the validator")
	}
	if !db.tx.committed {
		t.Error("want the transaction committed")
	}
}

// Second boot: everything already applied, no transaction is opened.
func TestApplyMigrationsIdempotent(t *testing.T) {
	fsys := fstest.MapFS{"V002_new.sql": {Data: []byte("ALTER TABLE x ADD COLUMN y INT;")}}

	db := &fakeDB{tx: &fakeTx{}}
	db.queryRows = &fakeRows{rows: [][]interface{}{{"V001"}, {"V002"}}}

	if err := ApplyMigrations(context.Background(), db, fsys); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(db.tx.execs) != 0 {
		t.Errorf("want no migration executed, got: %v", db.tx.execs)
	}
}
