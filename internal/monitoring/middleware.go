package monitoring

import (
	"bytes"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/gin-gonic/gin"
)

// responseWriter captures the status code and, for errors, the body.
type responseWriter struct {
	gin.ResponseWriter
	statusCode int
	body       *bytes.Buffer
}

func (w *responseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	// Capture the body only for errors
	if w.statusCode >= 400 {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *responseWriter) WriteString(s string) (int, error) {
	if w.statusCode >= 400 {
		w.body.WriteString(s)
	}
	return w.ResponseWriter.WriteString(s)
}

// Unwrap lets http.NewResponseController reach the underlying writer (the
// /events stream clears its write deadline through it). gin.ResponseWriter
// does not declare Unwrap, so embedding it does not promote the method.
func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// MetricsMiddleware records request metrics and logs errors and slow requests.
func MetricsMiddleware(metrics *Metrics, tracer *trace.Tracer) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		// Wrap the writer to capture the response
		writer := &responseWriter{
			ResponseWriter: c.Writer,
			statusCode:     200, // Default
			body:           bytes.NewBuffer(nil),
		}
		c.Writer = writer

		// Handle the request
		c.Next()

		// Compute duration
		duration := time.Since(start)
		statusCode := writer.statusCode

		// Record metrics
		var err error
		if statusCode >= 500 {
			err = fmt.Errorf("HTTP %d", statusCode)
		}
		metrics.RecordRequest(path, statusCode, duration, err)

		// Detailed log for 5xx errors
		if statusCode >= 500 {
			tracer.Error("[HTTP ERROR] %s %s - Status: %d - Duration: %dms - Body: %s",
				c.Request.Method,
				path,
				statusCode,
				duration.Milliseconds(),
				writer.body.String())

			// Request details for debugging
			tracer.Error("[HTTP ERROR DETAILS] IP: %s, User-Agent: %s, Content-Type: %s",
				c.ClientIP(),
				c.Request.UserAgent(),
				c.ContentType())
		} else if statusCode >= 400 {
			// Warning for 4xx
			tracer.Warning("[HTTP WARN] %s %s - Status: %d - Duration: %dms",
				c.Request.Method,
				path,
				statusCode,
				duration.Milliseconds())
		}

		// Slow request (> 1s)
		if duration > time.Second {
			tracer.Warning("[HTTP SLOW] %s %s - Duration: %dms",
				c.Request.Method,
				path,
				duration.Milliseconds())
		}
	}
}

// RecoveryMiddleware recovers from panics, logs them and returns a 500.
func RecoveryMiddleware(metrics *Metrics, tracer *trace.Tracer) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Log the panic
				tracer.Error("[PANIC] %s %s - Error: %v",
					c.Request.Method,
					c.Request.URL.Path,
					err)

				// Stack trace
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				tracer.Error("[PANIC STACK] %s", string(buf[:n]))

				// Record the metric
				metrics.RecordRequest(c.Request.URL.Path, 500, 0, fmt.Errorf("panic: %v", err))

				// Return a generic error
				c.JSON(500, gin.H{
					"error":   "Internal server error",
					"message": "An unexpected error occurred",
				})

				c.Abort()
			}
		}()

		c.Next()
	}
}
