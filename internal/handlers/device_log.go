package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/devicelog"

	"github.com/gin-gonic/gin"
)

// logDeviceID reads :id. The log file path is derived from this number
// only, never from request text.
func logDeviceID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return 0, false
	}
	return id, true
}

// getDeviceLog serves the device's most recent log file for viewing in the
// browser ("View log" in the drawer).
func (h *Handler) getDeviceLog(c *gin.Context) {
	id, ok := logDeviceID(c)
	if !ok {
		return
	}
	f, err := os.Open(h.logs.ActivePath(id))
	if errors.Is(err, os.ErrNotExist) {
		c.String(http.StatusNotFound, "Logging is off or the log is still empty for device %d.", id)
		return
	}
	if err != nil {
		h.tracer.Error("Opening log of device %d: %v", id, err)
		c.String(http.StatusInternalServerError, "Could not read the log.")
		return
	}
	defer f.Close()

	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`inline; filename="device-%d.log"`, id))
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, f); err != nil {
		h.tracer.Warning("Sending log of device %d: %v", id, err)
	}
}

// getDeviceLogZip returns a zip with every log file of the device.
func (h *Handler) getDeviceLogZip(c *gin.Context) {
	id, ok := logDeviceID(c)
	if !ok {
		return
	}
	var buf bytes.Buffer
	err := h.logs.WriteZip(id, &buf)
	if errors.Is(err, devicelog.ErrNoLogs) {
		c.String(http.StatusNotFound, "No logs for device %d.", id)
		return
	}
	if err != nil {
		h.tracer.Error("Building log zip of device %d: %v", id, err)
		c.String(http.StatusInternalServerError, "Could not build the zip.")
		return
	}

	name := fmt.Sprintf("device-%d-logs-%s.zip", id, time.Now().Format("20060102-1504"))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/zip", buf.Bytes())
}
