package hikvision

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/devicelog"

	"github.com/gin-gonic/gin"
)

func newTestLog(t *testing.T) (*devicelog.Logger, string) {
	t.Helper()
	dir := t.TempDir()
	l := devicelog.New(devicelog.Options{Dir: dir, MaxSize: 1 << 20, MaxFiles: 8},
		devicelog.Info{ID: 7, Name: "hik", Type: "Hikvision", Port: 4007}, nil)
	if err := l.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	return l, filepath.Join(dir, "device-7.log")
}

// waitForLog waits (up to 2s) for all snippets to appear: the connection close
// reaches the ConnState hook asynchronously.
func waitForLog(t *testing.T, path string, snippets ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, _ := os.ReadFile(path)
		missing := ""
		for _, tr := range snippets {
			if !strings.Contains(string(b), tr) {
				missing = tr
				break
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing %q in log:\n%s", missing, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHikvisionLogRecordsConnectionAndRequest(t *testing.T) {
	e := newTestEmulator(t)
	dlog, path := newTestLog(t)
	e.dlog = dlog

	srv := httptest.NewUnstartedServer(e.newRouter())
	srv.Config.ConnState = e.dlog.ConnState
	srv.Start()

	resp, err := http.Get(srv.URL + "/missing-route?employeeNo=55")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	srv.Close()

	waitForLog(t, path,
		" opened",
		"IN 127.0.0.1:",
		"GET /missing-route?employeeNo=55 -> 404 ",
		"| employeeNo=55",
		" closed (1 reqs, ")
}

func TestHikvisionPostEventRecordsOutgoing(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	e := newTestEmulator(t)
	dlog, path := newTestLog(t)
	e.dlog = dlog

	event := &Event{EventType: "AccessControllerEvent"}
	event.AccessControllerEvent.CardNo = "999"
	body := buildPushEventMultipart(event, []byte{0xff, 0xd8, 0xff, 0x00}, "MIME_boundary")

	if err := e.postEvent(target.URL+"/events", "multipart/form-data; boundary=MIME_boundary", body, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, path,
		"OUT -> POST "+target.URL+"/events -> 200 ",
		"cardNo=999",
		"[Picture] <image/jpeg 4B>")
}

func TestHikvisionWithoutLoggerDoesNotBreak(t *testing.T) {
	e := newTestEmulator(t) // dlog nil
	srv := httptest.NewServer(e.newRouter())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/missing-route")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d", resp.StatusCode)
	}
}

// A panicking handler becomes a 500 and must show up in the log: it is the
// error an operator most wants to see.
func TestHikvisionLogRecordsPanickedRequest(t *testing.T) {
	e := newTestEmulator(t)
	dlog, path := newTestLog(t)
	e.dlog = dlog

	r := e.newRouter()
	r.GET("/panic", func(c *gin.Context) { panic("test") })
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/panic")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitForLog(t, path, "GET /panic -> 500 ")
}
