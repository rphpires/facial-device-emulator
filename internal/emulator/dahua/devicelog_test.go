package dahua

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/devicelog"
	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/gin-gonic/gin"
)

func newTestLog(t *testing.T) (*devicelog.Logger, string) {
	t.Helper()
	dir := t.TempDir()
	l := devicelog.New(devicelog.Options{Dir: dir, MaxSize: 1 << 20, MaxFiles: 8},
		devicelog.Info{ID: 9, Name: "dahua", Type: "Dahua", Port: 4009}, nil)
	if err := l.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	return l, filepath.Join(dir, "device-9.log")
}

// waitForLog waits (up to 2s) for every fragment to appear: the connection
// close reaches the ConnState hook asynchronously.
func waitForLog(t *testing.T, path string, fragments ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, _ := os.ReadFile(path)
		missing := ""
		for _, tr := range fragments {
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

func newTestDahuaEmulator(dlog *devicelog.Logger) *Emulator {
	return &Emulator{
		tracer:   trace.NewTracer(),
		stopChan: make(chan struct{}),
		configs:  newConfigStore("aa:bb:cc:dd:ee:ff"),
		eventLog: newDahuaEventLog(10),
		pushChan: make(chan []byte, 8),
		dlog:     dlog,
	}
}

func TestDahuaLogRecordsConnectionAndRequest(t *testing.T) {
	dlog, path := newTestLog(t)
	e := newTestDahuaEmulator(dlog)

	srv := httptest.NewUnstartedServer(e.newRouter())
	srv.Config.ConnState = e.dlog.ConnState
	srv.Start()

	resp, err := http.Get(srv.URL + "/cgi-bin/nonexistent.cgi?action=insert&name=AccessUserCard")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	srv.Close()

	waitForLog(t, path,
		" opened",
		"IN 127.0.0.1:",
		"GET /cgi-bin/nonexistent.cgi?action=insert&name=AccessUserCard -> 404 ",
		"| action=insert name=AccessUserCard",
		" closed (1 reqs, ")
}

func TestDahuaPostNotificationLogsOutbound(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	dlog, path := newTestLog(t)
	e := newTestDahuaEmulator(dlog)

	body := "--myboundary\r\nContent-Type: text/plain\r\nContent-Disposition: form-data; name=\"info\"\r\n\r\n" +
		`{"Events":[{"Code":"DoorStatus","Data":{"Status":"Open"}}]}` + "\r\n--myboundary--\r\n"
	if err := e.postNotification(target.URL+"/notification", []byte(body), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, path,
		"OUT -> POST "+target.URL+"/notification -> 200 ",
		"Code=DoorStatus Status=Open")
}

// A panicking handler becomes a 500 and must show up in the log: it is the
// error the operator most wants to see.
func TestDahuaLogRecordsPanickingRequest(t *testing.T) {
	dlog, path := newTestLog(t)
	e := newTestDahuaEmulator(dlog)

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
