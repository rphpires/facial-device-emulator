package hikvision

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rphpires/facial-device-emulator/internal/devicetime"
)

// withDefaultZone sets the process-wide default device zone for one test.
func withDefaultZone(t *testing.T, loc *time.Location) {
	t.Helper()
	prev := devicetime.Default()
	devicetime.SetDefault(loc)
	t.Cleanup(func() { devicetime.SetDefault(prev) })
}

var isapiDateTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{2}:\d{2}$`)

func TestEventDateTime_UsesDefaultZone(t *testing.T) {
	withDefaultZone(t, time.UTC)
	e := newTestEmulator(t)
	got := e.buildDoorEvent(21).DateTime
	if !isapiDateTime.MatchString(got) || !strings.HasSuffix(got, "+00:00") {
		t.Fatalf("dateTime = %q, want ISAPI format with +00:00", got)
	}

	withDefaultZone(t, time.FixedZone("tokyo", 9*3600))
	got = e.buildDoorEvent(21).DateTime
	if !strings.HasSuffix(got, "+09:00") {
		t.Fatalf("dateTime = %q, want +09:00 suffix", got)
	}
}

func TestSetDateTime_TimeZoneDrivesEvents(t *testing.T) {
	withDefaultZone(t, time.UTC)
	e := newTestEmulator(t)
	r := gin.New()
	r.PUT("/ISAPI/System/time", e.handleSetDateTime)
	r.GET("/ISAPI/System/time", e.handleGetDateTime)

	body := `<?xml version="1.0" encoding="UTF-8"?>
<Time version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<timeMode>manual</timeMode>
<localTime>2026-01-01T10:00:00</localTime>
<timeZone>CST+3:00:00</timeZone>
</Time>`
	req := httptest.NewRequest(http.MethodPut, "/ISAPI/System/time", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status %d", w.Code)
	}

	before := time.Now().Truncate(time.Second)
	got := e.buildDoorEvent(21).DateTime
	if !strings.HasSuffix(got, "-03:00") {
		t.Fatalf("dateTime = %q, want -03:00 suffix", got)
	}
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatal(err)
	}
	if d := parsed.Sub(before); d < 0 || d > 5*time.Second {
		t.Fatalf("dateTime %q is not the current instant (off by %v)", got, d)
	}

	req = httptest.NewRequest(http.MethodGet, "/ISAPI/System/time", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "<timeZone>CST+3:00:00</timeZone>") {
		t.Fatalf("GET must report the configured zone; got:\n%s", w.Body.String())
	}
	if !regexp.MustCompile(`<localTime>[^<]+-03:00</localTime>`).MatchString(w.Body.String()) {
		t.Fatalf("GET localTime must carry -03:00; got:\n%s", w.Body.String())
	}
}

func TestGetDateTime_DefaultUTC(t *testing.T) {
	withDefaultZone(t, time.UTC)
	e := newTestEmulator(t)
	r := gin.New()
	r.GET("/ISAPI/System/time", e.handleGetDateTime)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ISAPI/System/time", nil))
	if !strings.Contains(w.Body.String(), "<timeZone>CST+0:00:00</timeZone>") {
		t.Fatalf("want CST+0:00:00 for UTC; got:\n%s", w.Body.String())
	}
}
