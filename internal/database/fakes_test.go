package database

import (
	"context"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v4"
)

// fakeRow returns fixed values (or an error) from Scan.
type fakeRow struct {
	values []interface{}
	err    error
}

func (r fakeRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		if i >= len(r.values) {
			break
		}
		switch d := dest[i].(type) {
		case *bool:
			*d = r.values[i].(bool)
		case *int:
			*d = r.values[i].(int)
		case *string:
			*d = r.values[i].(string)
		}
	}
	return nil
}

// fakeRows implements pgx.Rows over a matrix of values.
type fakeRows struct {
	rows [][]interface{}
	pos  int
	err  error
}

func (r *fakeRows) Close()                                         {}
func (r *fakeRows) Err() error                                     { return r.err }
func (r *fakeRows) CommandTag() pgconn.CommandTag                  { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgproto3.FieldDescription { return nil }
func (r *fakeRows) Values() ([]interface{}, error)                 { return nil, nil }
func (r *fakeRows) RawValues() [][]byte                            { return nil }

func (r *fakeRows) Next() bool {
	if r.pos >= len(r.rows) {
		return false
	}
	r.pos++
	return true
}

func (r *fakeRows) Scan(dest ...interface{}) error {
	row := r.rows[r.pos-1]
	return fakeRow{values: row}.Scan(dest...)
}

// fakeTx implements pgx.Tx and records what was executed.
type fakeTx struct {
	execs      []string
	execArgs   [][]interface{}
	committed  bool
	rolledBack bool
	execErr    error
	queryRows  pgx.Rows
	queryRowFn func(sql string) pgx.Row
}

func (t *fakeTx) Begin(ctx context.Context) (pgx.Tx, error)                 { return t, nil }
func (t *fakeTx) BeginFunc(ctx context.Context, f func(pgx.Tx) error) error { return f(t) }
func (t *fakeTx) Commit(ctx context.Context) error                          { t.committed = true; return nil }
func (t *fakeTx) Rollback(ctx context.Context) error                        { t.rolledBack = true; return nil }
func (t *fakeTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (t *fakeTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { return nil }
func (t *fakeTx) LargeObjects() pgx.LargeObjects                               { return pgx.LargeObjects{} }
func (t *fakeTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (t *fakeTx) Conn() *pgx.Conn { return nil }
func (t *fakeTx) QueryFunc(ctx context.Context, sql string, args []interface{}, scans []interface{}, f func(pgx.QueryFuncRow) error) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (t *fakeTx) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	t.execs = append(t.execs, sql)
	t.execArgs = append(t.execArgs, args)
	return pgconn.CommandTag{}, t.execErr
}

func (t *fakeTx) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	if t.queryRows == nil {
		return &fakeRows{}, nil
	}
	return t.queryRows, nil
}

func (t *fakeTx) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	if t.queryRowFn != nil {
		return t.queryRowFn(sql)
	}
	return fakeRow{}
}

// fakeDB implements DBInterface and always returns the same fakeTx.
type fakeDB struct {
	tx        *fakeTx
	execs     []string
	queryRows pgx.Rows
	row       pgx.Row

	// queryRowSQLs collects the SQL of every QueryRow call, in order —
	// for tests that need to confirm a specific clause (e.g.
	// "WHERE host <> ''") is actually in the emitted query, since
	// fakeRow/fakeRows don't filter anything.
	queryRowSQLs []string
}

func (d *fakeDB) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	if d.queryRows == nil {
		return &fakeRows{}, nil
	}
	return d.queryRows, nil
}

func (d *fakeDB) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	d.queryRowSQLs = append(d.queryRowSQLs, sql)
	if d.row == nil {
		return fakeRow{}
	}
	return d.row
}

func (d *fakeDB) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	d.execs = append(d.execs, sql)
	return pgconn.CommandTag{}, nil
}

func (d *fakeDB) Begin(ctx context.Context) (pgx.Tx, error) {
	if d.tx == nil {
		d.tx = &fakeTx{}
	}
	return d.tx, nil
}

func (d *fakeDB) Ping(ctx context.Context) error { return nil }
