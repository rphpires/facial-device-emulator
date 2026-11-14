package handlers

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/monitoring"
	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/gin-gonic/gin"
)

// handleStream clears the server's write deadline through
// http.NewResponseController(c.Writer). That only works while gin's writer
// and every middleware wrapper in the router (MetricsMiddleware) expose
// Unwrap; this test pins it with a real server whose WriteTimeout is
// shorter than the stream.
func TestStreamOutlivesWriteTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(monitoring.MetricsMiddleware(monitoring.NewMetrics(), trace.NewTracer()))
	r.GET("/s", func(c *gin.Context) {
		if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{}); err != nil {
			t.Errorf("SetWriteDeadline through gin writer: %v", err)
			return
		}
		c.Header("Content-Type", "text/event-stream")
		for i := 0; i < 3; i++ {
			time.Sleep(150 * time.Millisecond)
			_, _ = c.Writer.WriteString("data: tick\n\n")
			c.Writer.Flush()
		}
	})

	srv := httptest.NewUnstartedServer(r)
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/s")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	ticks := 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: tick") {
			ticks++
		}
	}
	if ticks != 3 {
		t.Errorf("got %d ticks, want 3 (stream cut by WriteTimeout?)", ticks)
	}
}

func TestServerWriteTimeoutOutlastsBatch(t *testing.T) {
	if ServerWriteTimeout <= batchTimeout {
		t.Errorf("ServerWriteTimeout %v must exceed batchTimeout %v", ServerWriteTimeout, batchTimeout)
	}
}
