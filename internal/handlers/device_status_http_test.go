package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/emulator"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgconn"
	"github.com/jackc/pgx/v4"
)

// rowErrDB answers every QueryRow with the same Scan error, so the manager
// sees either a missing device (pgx.ErrNoRows) or a database failure.
type rowErrDB struct{ err error }

type errRow struct{ err error }

func (r errRow) Scan(...interface{}) error { return r.err }

func (d rowErrDB) QueryRow(context.Context, string, ...interface{}) pgx.Row { return errRow(d) }
func (d rowErrDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, d.err
}
func (d rowErrDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return nil, d.err
}
func (d rowErrDB) Begin(context.Context) (pgx.Tx, error) { return nil, d.err }
func (d rowErrDB) Ping(context.Context) error            { return d.err }

// TestDeviceRoutesStatusCodes: unknown IDs answer 404, state conflicts 409
// and database failures 500 - never 200 for a device that does not exist,
// and never 404 for a database timeout.
func TestDeviceRoutesStatusCodes(t *testing.T) {
	timeout := errors.New("timeout: context deadline exceeded")

	cases := []struct {
		name   string
		dbErr  error
		method string
		path   string
		body   string
		want   int
	}{
		{"get unknown", pgx.ErrNoRows, http.MethodGet, "/api/devices/9", "", http.StatusNotFound},
		{"get db timeout", timeout, http.MethodGet, "/api/devices/9", "", http.StatusInternalServerError},
		{"start unknown", pgx.ErrNoRows, http.MethodPost, "/api/devices/9/start", "", http.StatusNotFound},
		{"start db timeout", timeout, http.MethodPost, "/api/devices/9/start", "", http.StatusInternalServerError},
		{"stop unknown", pgx.ErrNoRows, http.MethodPost, "/api/devices/9/stop", "", http.StatusNotFound},
		{"settings unknown", pgx.ErrNoRows, http.MethodPut, "/api/devices/9/settings", `{"log_enabled":true}`, http.StatusNotFound},
		{"settings db failure", timeout, http.MethodPut, "/api/devices/9/settings", `{"log_enabled":true}`, http.StatusInternalServerError},
		{"users unknown", pgx.ErrNoRows, http.MethodGet, "/api/devices/9/users", "", http.StatusNotFound},
		{"mode get unknown", pgx.ErrNoRows, http.MethodGet, "/api/devices/9/mode", "", http.StatusNotFound},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tracer := testTracer(t)
			db := rowErrDB{c.dbErr}
			m := emulator.NewManager(db, db, tracer)
			h := &Handler{manager: m, batch: m, serviceDB: db, tracer: tracer}

			r := gin.New()
			r.GET("/api/devices/:id", h.getDevice)
			r.POST("/api/devices/:id/start", h.startSingleDevice)
			r.POST("/api/devices/:id/stop", h.stopSingleDevice)
			r.PUT("/api/devices/:id/settings", h.updateDeviceSettings)
			r.GET("/api/devices/:id/users", h.getDeviceUsers)
			r.GET("/api/devices/:id/mode", h.apiGetDeviceMode)

			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != c.want {
				t.Errorf("status %d, want %d - body: %s", w.Code, c.want, w.Body.String())
			}
		})
	}
}
