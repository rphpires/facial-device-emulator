package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/emulator"
	"github.com/rphpires/facial-device-emulator/internal/utils"

	"github.com/gin-gonic/gin"
)

// errTestDB stands for any infrastructure failure.
var errTestDB = errors.New("connection refused")

// newJSONRequest builds a Gin context with a JSON body and route params.
func newJSONRequest(t *testing.T, method, target, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	return c, rec
}

func TestApiCreateEmulatorRejectsMalformedJSON(t *testing.T) {
	h := &Handler{tracer: testTracer(t)}

	c, rec := newJSONRequest(t, http.MethodPost, "/api/emulators", `{"name":`, nil)
	h.apiCreateEmulator(c)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

func TestApiUpdateEmulatorRejectsNonNumericID(t *testing.T) {
	h := &Handler{tracer: testTracer(t)}

	c, rec := newJSONRequest(t, http.MethodPut, "/api/emulators/abc", `{}`,
		gin.Params{{Key: "id", Value: "abc"}})
	h.apiUpdateEmulator(c)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

func TestApiDeleteEmulatorRejectsNonNumericID(t *testing.T) {
	h := &Handler{tracer: testTracer(t)}

	c, rec := newJSONRequest(t, http.MethodDelete, "/api/emulators/xyz", "",
		gin.Params{{Key: "id", Value: "xyz"}})
	h.apiDeleteEmulator(c)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

// The domain-error to HTTP-status mapping is the contract the console and
// any integration rely on, so every branch is tested.
func TestStatusForDomainError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"invalid spec", emulator.ErrInvalidSpec, http.StatusBadRequest},
		{"port conflict", &emulator.ConflictError{Ports: []int{4000}}, http.StatusBadRequest},
		{"not found", emulator.ErrDeviceNotFound, http.StatusNotFound},
		{"emulator running", emulator.ErrDeviceRunning, http.StatusConflict},
		{"not running", emulator.ErrNotRunning, http.StatusConflict},
		{"disabled", emulator.ErrDeviceDisabled, http.StatusConflict},
		{"port in use", utils.ErrPortInUse, http.StatusConflict},
		{"database failure", errTestDB, http.StatusInternalServerError},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := statusForError(c.err); got != c.want {
				t.Errorf("status %d, want %d", got, c.want)
			}
		})
	}
}

// The port-conflict error body must list the ports: the console builds its
// message from that list.
func TestConflictErrorBodyListsPorts(t *testing.T) {
	h := &Handler{tracer: testTracer(t)}

	c, rec := newJSONRequest(t, http.MethodPost, "/api/emulators", "", nil)
	h.writeError(c, &emulator.ConflictError{Ports: []int{4001, 4003}})

	var body struct {
		Error     string `json:"error"`
		Conflicts []int  `json:"conflicts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v - %s", err, rec.Body.String())
	}
	if len(body.Conflicts) != 2 || body.Conflicts[0] != 4001 {
		t.Errorf("conflicts: %v, want [4001 4003]", body.Conflicts)
	}
	if body.Error == "" {
		t.Error("want a readable message in error")
	}
}

// Internal errors must not leak driver text to the client.
func TestInternalErrorBodyIsGeneric(t *testing.T) {
	h := &Handler{tracer: testTracer(t)}

	c, rec := newJSONRequest(t, http.MethodPost, "/api/emulators", "", nil)
	h.writeError(c, errTestDB)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("driver error leaked: %s", rec.Body.String())
	}
}

// The fleet routes (/start, /stop, /batch, /range) share the group with
// PUT|DELETE /:id. Route registration panics at startup, not at compile
// time: without this test a regression would only show when the service
// fails to boot.
func TestEmulatorRoutesCoexist(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{tracer: testTracer(t)}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route registration panicked: %v", r)
		}
	}()

	router := gin.New()
	h.setupAPIRoutes(router)
	h.setupWebRoutes(router)
}

// Fleet control must not be reachable with GET: a crawler, a link preview
// or a prefetch would start or stop the whole fleet.
func TestFleetControlRequiresPost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{tracer: testTracer(t)}
	router := gin.New()
	h.setupAPIRoutes(router)

	for _, path := range []string{"/api/emulators/start", "/api/emulators/stop", "/api/emulators/batch"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s answered %d, want it unrouted", path, rec.Code)
		}
	}
}
