package dahua

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v4"
)

// This file implements an in-memory fake DBInterface, used by the
// repository/handler tests that exercise the card and face business rules
// (AddCard, CheckIfFaceExists, GetCardUserIDByRecNo, etc.) without a real
// Postgres. The Repository queries are constants inside the package, so the
// fake dispatches on fragments of the SQL text - the same pattern as
// internal/database/fakes_test.go, adapted to the dahua_cards/dahua_faces
// tables.

var errFakeNotFound = errors.New("fakestore: row not found")

// fakeRow implements pgx.Row, returning fixed values or a Scan error.
type fakeRow struct {
	values []interface{}
	err    error
}

func (r fakeRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	for i, d := range dest {
		if i >= len(r.values) {
			continue
		}
		switch dv := d.(type) {
		case *int:
			*dv = r.values[i].(int)
		case *string:
			*dv = r.values[i].(string)
		case *time.Time:
			*dv = r.values[i].(time.Time)
		default:
			return errors.New("fakestore: unsupported Scan destination type")
		}
	}
	return nil
}

// fakeRows implements pgx.Rows over a matrix of values.
type fakeRows struct {
	rows [][]interface{}
	pos  int
}

func (r *fakeRows) Close()                                         {}
func (r *fakeRows) Err() error                                     { return nil }
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

// fakeStore is the in-memory fake DBInterface. It keeps cards and faces in
// slices/maps and resolves each Query/QueryRow/Exec by matching the SQL
// text it receives.
type fakeStore struct {
	cards []Card
	faces map[int]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{faces: map[int]string{}}
}

func (f *fakeStore) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	switch {
	case strings.Contains(sql, "dahua_cards") && strings.Contains(sql, "LIMIT"):
		// GetCards(count, offset): args = [deviceID, count, offset].
		count := args[1].(int)
		offset := args[2].(int)

		sorted := append([]Card{}, f.cards...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].RecNo < sorted[j].RecNo })

		var rows [][]interface{}
		for i, c := range sorted {
			if i < offset {
				continue
			}
			if count > 0 && len(rows) >= count {
				break
			}
			rows = append(rows, []interface{}{c.RecNo, c.CardName, c.UserID, c.CardNo, c.ValidDateStart, c.ValidDateEnd})
		}
		return &fakeRows{rows: rows}, nil

	case strings.Contains(sql, "dahua_cards") && strings.Contains(sql, "user_id = $2"):
		// FindCard(userID): args = [deviceID, userID].
		userID := args[1].(int)
		var rows [][]interface{}
		for _, c := range f.cards {
			if c.UserID == userID {
				rows = append(rows, []interface{}{c.RecNo, c.CardName, c.UserID, c.CardNo, c.ValidDateStart, c.ValidDateEnd})
			}
		}
		return &fakeRows{rows: rows}, nil

	default:
		return &fakeRows{}, nil
	}
}

func (f *fakeStore) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	switch {
	case strings.Contains(sql, "dahua_faces") && strings.Contains(sql, "COUNT(*)"):
		// CheckIfFaceExists(userID): args = [deviceID, userID].
		userID := args[1].(int)
		n := 0
		if _, ok := f.faces[userID]; ok {
			n = 1
		}
		return fakeRow{values: []interface{}{n}}

	case strings.Contains(sql, "COALESCE(MIN"):
		// GetNextRecNo(): args = [deviceID].
		used := map[int]bool{}
		for _, c := range f.cards {
			used[c.RecNo] = true
		}
		next := 1
		for used[next] {
			next++
		}
		return fakeRow{values: []interface{}{next}}

	case strings.Contains(sql, "SELECT user_id FROM emulator.dahua_cards"):
		// GetCardUserIDByRecNo(recNo): args = [deviceID, recNo].
		recNo := args[1].(int)
		for _, c := range f.cards {
			if c.RecNo == recNo {
				return fakeRow{values: []interface{}{c.UserID}}
			}
		}
		return fakeRow{err: errFakeNotFound}

	case strings.Contains(sql, "card_no = $2") && strings.Contains(sql, "user_id <> $3"):
		// cardNoBelongsToOtherUser(cardNo, userID): args = [deviceID, cardNo, userID].
		cardNo := args[1].(string)
		userID := args[2].(int)
		n := 0
		for _, c := range f.cards {
			if c.CardNo == cardNo && c.UserID != userID {
				n++
			}
		}
		return fakeRow{values: []interface{}{n}}

	case strings.Contains(sql, "dahua_cards") && strings.Contains(sql, "COUNT(*)"):
		// cardExistsForUserID(userID): args = [deviceID, userID].
		userID := args[1].(int)
		n := 0
		for _, c := range f.cards {
			if c.UserID == userID {
				n++
			}
		}
		return fakeRow{values: []interface{}{n}}

	default:
		return fakeRow{values: []interface{}{0}}
	}
}

func (f *fakeStore) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	switch {
	case strings.Contains(sql, "INSERT INTO emulator.dahua_cards"):
		// args = [deviceID, recNo, cardName, userID, cardNo, validDateStart, validDateEnd].
		f.cards = append(f.cards, Card{
			RecNo:          args[1].(int),
			CardName:       args[2].(string),
			UserID:         args[3].(int),
			CardNo:         args[4].(string),
			ValidDateStart: args[5].(time.Time),
			ValidDateEnd:   args[6].(time.Time),
		})

	case strings.Contains(sql, "DELETE FROM emulator.dahua_cards"):
		// args = [deviceID, recNo].
		recNo := args[1].(int)
		var remaining []Card
		for _, c := range f.cards {
			if c.RecNo != recNo {
				remaining = append(remaining, c)
			}
		}
		f.cards = remaining

	case strings.Contains(sql, "INSERT INTO emulator.dahua_faces"):
		// args = [deviceID, userID, md5].
		f.faces[args[1].(int)] = args[2].(string)

	case strings.Contains(sql, "DELETE FROM emulator.dahua_faces"):
		// args = [deviceID, userID].
		delete(f.faces, args[1].(int))
	}

	return pgconn.CommandTag("OK"), nil
}

func (f *fakeStore) Begin(ctx context.Context) (pgx.Tx, error) { return nil, nil }
func (f *fakeStore) Ping(ctx context.Context) error            { return nil }
