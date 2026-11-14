package handlers

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/devicelog"

	"github.com/gin-gonic/gin"
)

// logRouter registers the routes as in production, next to a sibling route
// with a parameter, to catch gin route conflicts.
func logRouter(t *testing.T) (*gin.Engine, *devicelog.Registry) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	reg := devicelog.NewRegistry(devicelog.Options{Dir: t.TempDir(), MaxSize: 1 << 20, MaxFiles: 8}, nil)
	h := &Handler{tracer: testTracer(t), logs: reg}
	r := gin.New()
	g := r.Group("/api/devices")
	g.GET("/:id/settings", func(c *gin.Context) { c.Status(204) })
	g.GET("/:id/log", h.getDeviceLog)
	g.GET("/:id/log.zip", h.getDeviceLogZip)
	return r, reg
}

func get(r *gin.Engine, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestGetDeviceLogServesActiveFileInline(t *testing.T) {
	r, reg := logRouter(t)
	l := reg.For(devicelog.Info{ID: 3, Name: "x", Type: "Dahua", Port: 4003})
	_ = l.SetEnabled(true)
	l.Printf("IN", "test line")

	rec := get(r, "/api/devices/3/log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") {
		t.Errorf("Content-Disposition %q", cd)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("missing no-store")
	}
	if !strings.Contains(rec.Body.String(), "IN test line") {
		t.Errorf("body: %s", rec.Body.String())
	}
	if get(r, "/api/devices/3/settings").Code != 204 {
		t.Error("sibling route broke")
	}
}

func TestGetDeviceLogZip(t *testing.T) {
	r, reg := logRouter(t)
	_ = reg.SetEnabled(devicelog.Info{ID: 3}, true)

	rec := get(r, "/api/devices/3/log.zip")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="device-3-logs-`) {
		t.Errorf("Content-Disposition %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "device-3.log" {
		t.Errorf("zip: %+v", zr.File)
	}
}

func TestGetDeviceLogWithoutFile404(t *testing.T) {
	r, _ := logRouter(t)
	for _, target := range []string{"/api/devices/9/log", "/api/devices/9/log.zip"} {
		if rec := get(r, target); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, rec.Code)
		}
	}
}

func TestGetDeviceLogInvalidID400(t *testing.T) {
	r, _ := logRouter(t)
	for _, target := range []string{"/api/devices/abc/log", "/api/devices/0/log.zip", "/api/devices/-1/log"} {
		if rec := get(r, target); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", target, rec.Code)
		}
	}
}
