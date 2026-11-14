package emulator

import (
	"context"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v4"
)

type fakeRow struct {
	values []interface{}
	err    error
}

func (l fakeRow) Scan(dest ...interface{}) error {
	if l.err != nil {
		return l.err
	}
	for i := range dest {
		if i >= len(l.values) {
			break
		}
		switch d := dest[i].(type) {
		case *bool:
			*d = l.values[i].(bool)
		case *int:
			*d = l.values[i].(int)
		case *string:
			*d = l.values[i].(string)
		}
	}
	return nil
}

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
	return fakeRow{values: r.rows[r.pos-1]}.Scan(dest...)
}

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
	// Recorded with the Execs: INSERT ... RETURNING goes through QueryRow.
	t.execs = append(t.execs, sql)
	t.execArgs = append(t.execArgs, args)
	if t.queryRowFn != nil {
		return t.queryRowFn(sql)
	}
	return fakeRow{}
}

type fakeDB struct {
	tx        *fakeTx
	execs     []string
	execArgs  [][]interface{}
	queryRows pgx.Rows
	row       pgx.Row
}

func (d *fakeDB) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	if d.queryRows == nil {
		return &fakeRows{}, nil
	}
	return d.queryRows, nil
}

func (d *fakeDB) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	if d.row == nil {
		return fakeRow{}
	}
	return d.row
}

func (d *fakeDB) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	d.execs = append(d.execs, sql)
	d.execArgs = append(d.execArgs, args)
	return pgconn.CommandTag{}, nil
}

func (d *fakeDB) Begin(ctx context.Context) (pgx.Tx, error) {
	if d.tx == nil {
		d.tx = &fakeTx{}
	}
	return d.tx, nil
}

func (d *fakeDB) Ping(ctx context.Context) error { return nil }
