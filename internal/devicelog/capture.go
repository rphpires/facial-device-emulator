package devicelog

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// maxCapture is how much of the response is kept for Describe: enough for
// sanitizing to recognize a long base64 blob before truncating to maxBody.
const maxCapture = 64 << 10

const streamKey = "devicelog.stream"

type connInfo struct {
	opened time.Time
	reqs   int
}

type streamInfo struct {
	opened time.Time
	path   string
	events int
}

// MarkStream tells the middleware that the handler took over the connection
// as a stream. The handler itself logs CONNECTED/DISCONNECTED.
func MarkStream(c *gin.Context) {
	c.Set(streamKey, true)
}

type bodyCapture struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (w *bodyCapture) keep(b []byte) {
	if room := maxCapture - w.buf.Len(); room > 0 {
		if len(b) > room {
			b = b[:room]
		}
		w.buf.Write(b)
	}
}

func (w *bodyCapture) Write(b []byte) (int, error) {
	w.keep(b)
	return w.ResponseWriter.Write(b)
}

func (w *bodyCapture) WriteString(s string) (int, error) {
	w.keep([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

// Unwrap lets http.NewResponseController reach the underlying writer.
// gin.ResponseWriter does not declare Unwrap, so embedding it does not
// promote the method.
func (w *bodyCapture) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// restoredBody hands the handler what the middleware already read, followed
// by the rest of the original body.
type restoredBody struct {
	io.Reader
	io.Closer
}

// Gin logs every incoming request. When disabled it reads nothing.
func (l *Logger) Gin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !l.Enabled() {
			c.Next()
			return
		}
		start := time.Now()
		req := c.Request

		var reqBody []byte
		read, tooLarge := 0, false
		if req.Body != nil && req.Body != http.NoBody {
			data, _ := io.ReadAll(io.LimitReader(req.Body, maxCopy+1))
			read, tooLarge = len(data), len(data) > maxCopy
			req.Body = restoredBody{io.MultiReader(bytes.NewReader(data), req.Body), req.Body}
			if !tooLarge {
				reqBody = data
			}
		}

		w := &bodyCapture{ResponseWriter: c.Writer}
		c.Writer = w
		c.Next()

		if c.GetBool(streamKey) {
			return
		}

		respSize := w.Size()
		if respSize < 0 {
			respSize = 0
		}
		reqSize := humanSize(read)
		if tooLarge {
			reqSize = ">" + humanSize(maxCopy)
		}

		var b strings.Builder
		b.WriteString(req.RemoteAddr)
		if u := UserFromAuth(req.Header.Get("Authorization")); u != "" {
			b.WriteString(" user=" + u)
		}
		fmt.Fprintf(&b, " %s %s -> %d %dms (%s/%s)", req.Method, maskURI(req.URL.RequestURI()),
			w.Status(), time.Since(start).Milliseconds(), reqSize, humanSize(respSize))
		ct := req.Header.Get("Content-Type")
		if f := KeyFields(ct, reqBody, req.URL.Query()); f != "" {
			b.WriteString(" | " + f)
		}
		switch {
		case tooLarge:
			fmt.Fprintf(&b, "\n    > <body over %s not copied>", humanSize(maxCopy))
		case len(reqBody) > 0:
			if d := Describe(ct, reqBody); d != "" {
				b.WriteString("\n    > " + d)
			}
		}
		if d := Describe(w.Header().Get("Content-Type"), w.buf.Bytes()); d != "" {
			b.WriteString("\n    < " + d)
		}
		l.Printf("IN", "%s", b.String())
	}
}

func (ci *connInfo) summary(now time.Time) string {
	if ci == nil {
		return "? reqs"
	}
	s := fmt.Sprintf("%d reqs", ci.reqs)
	if !ci.opened.IsZero() {
		s += ", " + now.Sub(ci.opened).Round(time.Second).String()
	}
	return s
}

// ConnState is the http.Server hook: one line when a TCP connection opens
// and another when it closes. Two management clients talking to the same
// emulator show up here as two alternating addresses.
func (l *Logger) ConnState(conn net.Conn, state http.ConnState) {
	if !l.Enabled() {
		return
	}
	addr := conn.RemoteAddr().String()
	l.mu.Lock()
	defer l.mu.Unlock()

	ci := l.conns[addr]
	switch state {
	case http.StateNew:
		l.conns[addr] = &connInfo{opened: l.now()}
		l.writeLocked("CONN", addr+" opened")
	case http.StateActive:
		if ci == nil {
			ci = &connInfo{}
			l.conns[addr] = ci
		}
		ci.reqs++
	case http.StateHijacked:
		delete(l.conns, addr)
		l.writeLocked("CONN", fmt.Sprintf("%s became stream (%s)", addr, ci.summary(l.now())))
	case http.StateClosed:
		delete(l.conns, addr)
		l.writeLocked("CONN", fmt.Sprintf("%s closed (%s)", addr, ci.summary(l.now())))
	}
}

// StreamOpen records the opening of an event stream (alertStream,
// eventManager, snapManager).
func (l *Logger) StreamOpen(remote, path string) {
	if !l.Enabled() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.streams[remote] = &streamInfo{opened: l.now(), path: path}
	l.writeLocked("CONNECTED", remote+" "+path)
}

// StreamEvent records an event written to the stream (heartbeats are not logged).
func (l *Logger) StreamEvent(remote, summary string, size int) {
	if !l.Enabled() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.streams[remote]; s != nil {
		s.events++
	}
	msg := fmt.Sprintf("-> %s stream %s", remote, humanSize(size))
	if summary != "" {
		msg += " | " + summary
	}
	l.writeLocked("OUT", msg)
}

// StreamClose records the end of a stream with its duration and event count.
func (l *Logger) StreamClose(remote string) {
	if !l.Enabled() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.streams[remote]
	delete(l.streams, remote)
	if s == nil {
		l.writeLocked("DISCONNECTED", remote)
		return
	}
	l.writeLocked("DISCONNECTED", fmt.Sprintf("%s %s after %s, %d events",
		remote, s.path, l.now().Sub(s.opened).Round(time.Second), s.events))
}

// Out records a request sent by the emulator to the management client
// (event push, keepalive).
func (l *Logger) Out(method, url, contentType string, body []byte, status int, err error, d time.Duration) {
	if !l.Enabled() {
		return
	}
	res := fmt.Sprint(status)
	if err != nil {
		res = "error: " + err.Error()
	}
	msg := fmt.Sprintf("-> %s %s -> %s %dms", method, url, res, d.Milliseconds())
	if f := KeyFields(contentType, body, nil); f != "" {
		msg += " | " + f
	}
	if desc := Describe(contentType, body); desc != "" {
		msg += "\n    > " + desc
	}
	l.Printf("OUT", "%s", msg)
}
