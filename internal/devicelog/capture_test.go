package devicelog

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func enabledLogger(t *testing.T) (*Logger, Options) {
	t.Helper()
	o := testOpts(t)
	l := New(o, testInfo(), nil)
	if err := l.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	return l, o
}

func testRouter(l *Logger, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(l.Gin())
	r.Any("/*path", handler)
	return r
}

func TestGinLogsRequestWithBodyAndFields(t *testing.T) {
	l, o := enabledLogger(t)
	var received string
	r := testRouter(l, func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		received = string(b)
		c.JSON(200, gin.H{"statusCode": 1})
	})

	body := `{"UserInfo":{"employeeNo":"123","name":"Alice"}}`
	req := httptest.NewRequest("PUT", "/ISAPI/AccessControl/UserInfo/Modify?format=json", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", `Digest username="admin", realm="x"`)
	req.RemoteAddr = "10.0.0.5:51234"
	r.ServeHTTP(httptest.NewRecorder(), req)

	if received != body {
		t.Fatalf("handler received %q", received)
	}
	got := readFile(t, o.FilePath(1, 0))
	for _, want := range []string{
		"IN 10.0.0.5:51234 user=admin PUT /ISAPI/AccessControl/UserInfo/Modify?format=json -> 200 ",
		"| employeeNo=123 name=Alice",
		"\n    > " + body,
		"\n    < {\"statusCode\":1}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Digest") {
		t.Errorf("Authorization header leaked:\n%s", got)
	}
}

func TestGinLargeBodyReachesHandlerIntact(t *testing.T) {
	l, o := enabledLogger(t)
	large := bytes.Repeat([]byte("a"), maxCopy+10)
	var n int
	r := testRouter(l, func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		n = len(b)
		c.Status(200)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/face", bytes.NewReader(large)))

	if n != len(large) {
		t.Fatalf("handler received %d bytes, want %d", n, len(large))
	}
	got := readFile(t, o.FilePath(1, 0))
	if !strings.Contains(got, "> <body over 2.0MB not copied>") {
		t.Errorf("want only the size:\n%s", got)
	}
	if strings.Contains(got, "aaaa") {
		t.Errorf("large body was logged")
	}
}

func TestGinDisabledNeitherReadsNorWrites(t *testing.T) {
	o := testOpts(t)
	l := New(o, testInfo(), nil)
	r := testRouter(l, func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		c.String(200, string(b))
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/x", strings.NewReader("abc")))
	if rec.Body.String() != "abc" {
		t.Fatalf("response %q", rec.Body.String())
	}
	if fs := o.Files(1); len(fs) != 0 {
		t.Errorf("wrote while disabled: %v", fs)
	}
}

func TestGinMarkedStreamHasNoINLine(t *testing.T) {
	l, o := enabledLogger(t)
	r := testRouter(l, func(c *gin.Context) {
		MarkStream(c)
		c.Status(200)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/alertStream", nil))
	if strings.Contains(readFile(t, o.FilePath(1, 0)), " IN ") {
		t.Errorf("a stream must not produce an IN line")
	}
}

type fakeConn struct {
	net.Conn
	addr string
}

func (c fakeConn) RemoteAddr() net.Addr {
	a, _ := net.ResolveTCPAddr("tcp", c.addr)
	return a
}

func TestConnStateOpensCountsAndCloses(t *testing.T) {
	l, o := enabledLogger(t)
	c := fakeConn{addr: "10.0.0.5:51234"}
	l.ConnState(c, http.StateNew)
	l.ConnState(c, http.StateActive)
	l.ConnState(c, http.StateIdle)
	l.ConnState(c, http.StateActive)
	l.ConnState(c, http.StateClosed)

	got := readFile(t, o.FilePath(1, 0))
	if !strings.Contains(got, "CONN 10.0.0.5:51234 opened") {
		t.Errorf("missing opened:\n%s", got)
	}
	if !strings.Contains(got, "CONN 10.0.0.5:51234 closed (2 reqs, 0s)") {
		t.Errorf("missing closed:\n%s", got)
	}
}

func TestConnStateHijackedBecomesStream(t *testing.T) {
	l, o := enabledLogger(t)
	c := fakeConn{addr: "10.0.0.6:6000"}
	l.ConnState(c, http.StateNew)
	l.ConnState(c, http.StateActive)
	l.ConnState(c, http.StateHijacked)
	if !strings.Contains(readFile(t, o.FilePath(1, 0)), "CONN 10.0.0.6:6000 became stream (1 reqs, 0s)") {
		t.Errorf("missing became stream")
	}
}

func TestStreamOpenEventClose(t *testing.T) {
	l, o := enabledLogger(t)
	l.StreamOpen("10.0.0.5:6001", "/ISAPI/Event/notification/alertStream")
	l.StreamEvent("10.0.0.5:6001", "cardNo=999", 2048)
	l.StreamEvent("10.0.0.5:6001", "", 100)
	l.StreamClose("10.0.0.5:6001")

	got := readFile(t, o.FilePath(1, 0))
	for _, want := range []string{
		"CONNECTED 10.0.0.5:6001 /ISAPI/Event/notification/alertStream",
		"OUT -> 10.0.0.5:6001 stream 2KB | cardNo=999",
		"OUT -> 10.0.0.5:6001 stream 100B\n",
		"DISCONNECTED 10.0.0.5:6001 /ISAPI/Event/notification/alertStream after 0s, 2 events",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestDisablingWithOpenStreamStopsWriting(t *testing.T) {
	l, o := enabledLogger(t)
	l.StreamOpen("10.0.0.5:6001", "/alertStream")
	if err := l.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	l.StreamEvent("10.0.0.5:6001", "x", 10)
	l.StreamClose("10.0.0.5:6001")
	if fs := o.Files(1); len(fs) != 0 {
		t.Errorf("wrote after disabling: %v", fs)
	}
}

func TestOutLogsSuccessAndError(t *testing.T) {
	l, o := enabledLogger(t)
	l.Out("POST", "http://10.0.0.5:8000/events", "application/json",
		[]byte(`{"cardNo":"999"}`), 200, nil, 30*time.Millisecond)
	l.Out("GET", "http://10.0.0.5:8000/keepalive", "", nil, 0,
		errors.New("connection refused"), 2*time.Millisecond)

	got := readFile(t, o.FilePath(1, 0))
	for _, want := range []string{
		"OUT -> POST http://10.0.0.5:8000/events -> 200 30ms | cardNo=999\n    > {\"cardNo\":\"999\"}",
		"OUT -> GET http://10.0.0.5:8000/keepalive -> error: connection refused 2ms\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Dahua CGIs carry card/user passwords in the query string.
func TestGinMasksPasswordInQuery(t *testing.T) {
	l, o := enabledLogger(t)
	r := testRouter(l, func(c *gin.Context) { c.Status(200) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET",
		"/cgi-bin/recordUpdater.cgi?action=insert&CardNo=1&Password=s3cret&CardPassword=abc", nil))

	got := readFile(t, o.FilePath(1, 0))
	if strings.Contains(got, "s3cret") || strings.Contains(got, "abc") {
		t.Fatalf("password leaked:\n%s", got)
	}
	if !strings.Contains(got, "Password=***&CardPassword=***") {
		t.Errorf("query not masked:\n%s", got)
	}
}
