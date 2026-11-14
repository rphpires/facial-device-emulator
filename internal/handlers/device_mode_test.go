package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/emulator"
	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgconn"
	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v4"
)

// fakeRow returns a fixed value (or an error) from Scan, to exercise
// getDeviceMode without a database.
type fakeRow struct {
	value string
	err   error
}

func (l fakeRow) Scan(dest ...any) error {
	if l.err != nil {
		return l.err
	}
	p, ok := dest[0].(*string)
	if !ok {
		return errors.New("destination is not *string")
	}
	*p = l.value
	return nil
}

// fakeDB implements database.DBInterface for the tests in this file.
// QueryRow, Exec and Query matter; Begin and Ping only satisfy the
// interface.
type fakeDB struct {
	row         fakeRow
	execCalled  bool
	execArgs    []interface{}
	execErr     error
	execTag     string // command tag Exec returns; "" means one row affected
	queryCalled bool
	queryArgs   []interface{}
	queryRows   pgx.Rows
	queryErr    error
}

func (d *fakeDB) QueryRow(ctx context.Context, query string, args ...interface{}) pgx.Row {
	return d.row
}

func (d *fakeDB) Exec(ctx context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	d.execCalled = true
	d.execArgs = args
	tag := d.execTag
	if tag == "" {
		tag = "INSERT 0 1"
	}
	return pgconn.CommandTag(tag), d.execErr
}

func (d *fakeDB) Query(ctx context.Context, query string, args ...interface{}) (pgx.Rows, error) {
	d.queryCalled = true
	d.queryArgs = args
	return d.queryRows, d.queryErr
}
func (d *fakeDB) Begin(ctx context.Context) (pgx.Tx, error) { return nil, nil }
func (d *fakeDB) Ping(ctx context.Context) error            { return nil }

// modeRow is one (device_id, value) row returned by the getDeviceModes
// query.
type modeRow struct {
	id    int
	value string
}

// fakeRows implements pgx.Rows to exercise getDeviceModes without a
// database.
type fakeRows struct {
	rows []modeRow
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
	id, ok := dest[0].(*int)
	if !ok {
		return errors.New("destination[0] is not *int")
	}
	value, ok := dest[1].(*string)
	if !ok {
		return errors.New("destination[1] is not *string")
	}
	*id = row.id
	*value = row.value
	return nil
}

// execArgsText joins the Exec arguments into one string, so a test can
// assert on the stored value without depending on its position.
func (d *fakeDB) execArgsText() string {
	parts := make([]string, 0, len(d.execArgs))
	for _, a := range d.execArgs {
		parts = append(parts, fmt.Sprint(a))
	}
	return strings.Join(parts, " ")
}

// TestGetDeviceMode translates the stored LocalAuthentication value into
// the console vocabulary: "0" and "1" mean nothing to an operator.
func TestGetDeviceMode(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"zero is online", "0", "online"},
		{"one is standalone", "1", "standalone"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Handler{serviceDB: &fakeDB{row: fakeRow{value: c.value}}}
			got, err := h.getDeviceMode(context.Background(), 1)
			if err != nil {
				t.Fatalf("getDeviceMode: %v", err)
			}
			if got != c.want {
				t.Errorf("mode = %q, want %q", got, c.want)
			}
		})
	}
}

// TestGetDeviceModeUnknownDevice: a missing device is a 404, not a mode.
func TestGetDeviceModeUnknownDevice(t *testing.T) {
	// The query joins service.devices: no row at all means no device (a
	// device without a mode row comes back as COALESCE '1').
	h := &Handler{serviceDB: &fakeDB{row: fakeRow{err: pgx.ErrNoRows}}}
	_, err := h.getDeviceMode(context.Background(), 1)
	if !errors.Is(err, emulator.ErrDeviceNotFound) {
		t.Fatalf("err = %v, want ErrDeviceNotFound", err)
	}
}

// TestGetDeviceModeDefaultIsStandalone is the key case of this file: almost
// no device has its own row in emulator.device_settings (the seed only
// writes the global device_id = 0 row). Repository.GetSetting treats a
// missing row as "1" (standalone), and the query's COALESCE returns the
// same, so the console says what the emulator does.
func TestGetDeviceModeDefaultIsStandalone(t *testing.T) {
	h := &Handler{serviceDB: &fakeDB{row: fakeRow{value: "1"}}}
	got, err := h.getDeviceMode(context.Background(), 1)
	if err != nil {
		t.Fatalf("getDeviceMode: %v", err)
	}
	if got != modeStandalone {
		t.Errorf("mode = %q, want %q", got, modeStandalone)
	}
}

// TestGetDeviceModeDatabaseError checks that a genuine database error (not
// pgx.ErrNoRows) is propagated, not masked as some mode.
func TestGetDeviceModeDatabaseError(t *testing.T) {
	dbErr := errors.New("connection lost")
	h := &Handler{serviceDB: &fakeDB{row: fakeRow{err: dbErr}}}

	_, err := h.getDeviceMode(context.Background(), 1)
	if err == nil {
		t.Fatal("getDeviceMode returned no error for a database failure")
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("err = %v, want it to wrap %v", err, dbErr)
	}
}

// TestGetDeviceModes covers the bulk read used by the device list; without
// it the page would run one query per device.
func TestGetDeviceModes(t *testing.T) {
	t.Run("empty list does not query the database", func(t *testing.T) {
		db := &fakeDB{}
		h := &Handler{serviceDB: db}

		modes, err := h.getDeviceModes(context.Background(), nil)
		if err != nil {
			t.Fatalf("getDeviceModes: %v", err)
		}
		if len(modes) != 0 {
			t.Errorf("modes = %v, want an empty map", modes)
		}
		if db.queryCalled {
			t.Error("the database was queried with an empty ID list")
		}
	})

	t.Run("two devices: 0 is online, 1 is standalone", func(t *testing.T) {
		db := &fakeDB{queryRows: &fakeRows{rows: []modeRow{
			{id: 1, value: "0"},
			{id: 2, value: "1"},
		}}}
		h := &Handler{serviceDB: db}

		modes, err := h.getDeviceModes(context.Background(), []int32{1, 2})
		if err != nil {
			t.Fatalf("getDeviceModes: %v", err)
		}
		if modes[1] != "online" {
			t.Errorf("modes[1] = %q, want %q", modes[1], "online")
		}
		if modes[2] != "standalone" {
			t.Errorf("modes[2] = %q, want %q", modes[2], "standalone")
		}
		if !db.queryCalled {
			t.Error("the database was not queried")
		}
	})

	t.Run("device without a row is absent from the map", func(t *testing.T) {
		// Only device 1 has a row; device 2 is requested but not returned.
		// Callers fall back to standalone, like getDeviceMode.
		db := &fakeDB{queryRows: &fakeRows{rows: []modeRow{
			{id: 1, value: "0"},
		}}}
		h := &Handler{serviceDB: db}

		modes, err := h.getDeviceModes(context.Background(), []int32{1, 2})
		if err != nil {
			t.Fatalf("getDeviceModes: %v", err)
		}
		if _, ok := modes[2]; ok {
			t.Errorf("modes = %v, device 2 should not be in the map", modes)
		}
		if len(modes) != 1 {
			t.Errorf("modes = %v, want only device 1", modes)
		}
	})
}

// TestSetDeviceModeRejectsInvalidValue: an unknown mode must never reach
// the database, or it would store garbage in LocalAuthentication.
func TestSetDeviceModeRejectsInvalidValue(t *testing.T) {
	db := &fakeDB{}
	h := &Handler{serviceDB: db}

	err := h.setDeviceMode(context.Background(), 1, "turbo")
	if err == nil {
		t.Fatal("setDeviceMode(\"turbo\") = nil, want an error")
	}
	if db.execCalled {
		t.Error("the database was called with an invalid mode")
	}
}

// TestSetDeviceModeStoresTheRightValue checks the reverse translation and
// that the device ID reaches the query.
func TestSetDeviceModeStoresTheRightValue(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"online", "0"},
		{"standalone", "1"},
	}

	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			db := &fakeDB{}
			h := &Handler{serviceDB: db}

			if err := h.setDeviceMode(context.Background(), 7, c.mode); err != nil {
				t.Fatalf("setDeviceMode: %v", err)
			}
			if !db.execCalled {
				t.Fatal("the database was not called")
			}
			if !strings.Contains(db.execArgsText(), c.want) {
				t.Errorf("args = %s, want them to contain %q", db.execArgsText(), c.want)
			}
			if !strings.Contains(db.execArgsText(), "7") {
				t.Errorf("args = %s, want the device ID (7) among them", db.execArgsText())
			}
		})
	}
}

// testTracer returns a *trace.Tracer for this package's tests. The tracer
// only writes to stdout until it is configured, so nothing touches the disk.
func testTracer(t *testing.T) *trace.Tracer {
	t.Helper()
	return trace.NewTracer()
}

// TestApiSetDeviceModeHTTP drives the real handler through a gin engine:
// an invalid mode must not leak a driver error or become a 500, an invalid
// ID stays 400, and a valid request stores the mode and answers 200.
func TestApiSetDeviceModeHTTP(t *testing.T) {
	post := func(t *testing.T, db *fakeDB, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		h := &Handler{serviceDB: db, tracer: testTracer(t)}
		r := gin.New()
		r.POST("/api/devices/:id/mode", h.apiSetDeviceMode)

		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("invalid mode answers 400 without touching the database", func(t *testing.T) {
		db := &fakeDB{}
		w := post(t, db, "/api/devices/1/mode", `{"mode":"turbo"}`)

		if w.Code != http.StatusBadRequest {
			t.Errorf("HTTP status = %d, want %d - body: %s", w.Code, http.StatusBadRequest, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "Invalid mode") {
			t.Errorf("body = %s, want the invalid-mode message", body)
		}
		if strings.Contains(body, `"turbo"`) {
			t.Errorf("body = %s, should not echo the raw client value", body)
		}
		if db.execCalled {
			t.Error("the database was called with an invalid mode")
		}
	})

	t.Run("non-numeric ID answers 400", func(t *testing.T) {
		db := &fakeDB{}
		w := post(t, db, "/api/devices/abc/mode", `{"mode":"online"}`)

		if w.Code != http.StatusBadRequest {
			t.Errorf("HTTP status = %d, want %d - body: %s", w.Code, http.StatusBadRequest, w.Body.String())
		}
		if db.execCalled {
			t.Error("the database was called with an invalid ID")
		}
	})

	t.Run("valid mode is stored and answers 200", func(t *testing.T) {
		db := &fakeDB{}
		w := post(t, db, "/api/devices/1/mode", `{"mode":"standalone"}`)

		if w.Code != http.StatusOK {
			t.Fatalf("HTTP status = %d, want %d - body: %s", w.Code, http.StatusOK, w.Body.String())
		}
		if !db.execCalled {
			t.Error("the database was not called for a valid mode")
		}
		if body := w.Body.String(); !strings.Contains(body, `"mode":"standalone"`) {
			t.Errorf("body = %s, want {\"mode\":\"standalone\"}", body)
		}
	})

	t.Run("unknown device answers 404", func(t *testing.T) {
		db := &fakeDB{execTag: "INSERT 0 0"}
		w := post(t, db, "/api/devices/999/mode", `{"mode":"online"}`)

		if w.Code != http.StatusNotFound {
			t.Errorf("HTTP status = %d, want %d - body: %s", w.Code, http.StatusNotFound, w.Body.String())
		}
	})

	t.Run("database failure answers a generic 500 without leaking the driver error", func(t *testing.T) {
		db := &fakeDB{execErr: errors.New("pq: connection refused at 10.0.0.5:5432")}
		w := post(t, db, "/api/devices/1/mode", `{"mode":"online"}`)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("HTTP status = %d, want %d - body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
		}
		body := w.Body.String()
		if strings.Contains(body, "connection refused") || strings.Contains(body, "pq:") {
			t.Errorf("body = %s, should not leak the raw driver error", body)
		}
		if !strings.Contains(body, "Could not change the device mode") {
			t.Errorf("body = %s, want the generic message", body)
		}
	})
}
