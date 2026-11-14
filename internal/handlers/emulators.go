package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/emulator"
	"github.com/rphpires/facial-device-emulator/internal/models"
	"github.com/rphpires/facial-device-emulator/internal/utils"

	"github.com/gin-gonic/gin"
)

// statusForError maps a domain error to an HTTP status. It lives in one
// place so every CRUD handler answers the same way.
func statusForError(err error) int {
	switch {
	case errors.Is(err, emulator.ErrDeviceNotFound):
		return http.StatusNotFound
	case errors.Is(err, emulator.ErrDeviceRunning),
		errors.Is(err, emulator.ErrNotRunning),
		errors.Is(err, emulator.ErrDeviceDisabled),
		errors.Is(err, utils.ErrPortInUse):
		// State conflicts: already running/starting, not running, disabled,
		// or the port is held by something else.
		return http.StatusConflict
	case errors.Is(err, emulator.ErrInvalidSpec):
		// ConflictError matches here too: it is a payload error, not a
		// state error.
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// writeError writes the error body. A port conflict also carries the raw
// port lists, because the console highlights the offending ports.
func (h *Handler) writeError(c *gin.Context, err error) {
	status := statusForError(err)

	var conflict *emulator.ConflictError
	if errors.As(err, &conflict) {
		body := gin.H{"error": conflict.Error()}
		if len(conflict.Ports) > 0 {
			body["conflicts"] = conflict.Ports
		}
		if len(conflict.Reserved) > 0 {
			body["reserved"] = conflict.Reserved
		}
		c.JSON(status, body)
		return
	}

	if status == http.StatusInternalServerError {
		h.tracer.Error("Emulator CRUD failed: %v", err)
		c.JSON(status, gin.H{"error": "Internal error while processing the request"})
		return
	}

	c.JSON(status, gin.H{"error": err.Error()})
}

// apiListEmulators lists every emulator. An empty fleet is an empty array,
// never null.
func (h *Handler) apiListEmulators(c *gin.Context) {
	devices, err := h.manager.ListDevices()
	if err != nil {
		h.writeError(c, err)
		return
	}
	if devices == nil {
		devices = []models.Device{}
	}
	c.JSON(http.StatusOK, gin.H{"emulators": devices, "count": len(devices)})
}

// apiCreateEmulator creates one emulator.
func (h *Handler) apiCreateEmulator(c *gin.Context) {
	var spec emulator.DeviceSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dev, err := h.manager.CreateDevice(ctx, spec)
	if err != nil {
		h.writeError(c, err)
		return
	}

	if spec.AutoStart {
		if err := h.manager.Start(dev.ID); err != nil {
			// The device was created; only the start failed. 201 with the
			// warning inside is more honest than a 500 about a resource
			// that now exists.
			c.JSON(http.StatusCreated, gin.H{"emulator": dev, "start_error": err.Error()})
			return
		}
		// CreateDevice built dev before the start, with status "stopped".
		// Without this the response would say "started": true next to
		// "status": "stopped".
		dev.Status = "running"
		c.JSON(http.StatusCreated, gin.H{"emulator": dev, "started": true})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"emulator": dev})
}

// apiCreateEmulatorRange creates one emulator per port in the range.
func (h *Handler) apiCreateEmulatorRange(c *gin.Context) {
	var spec emulator.RangeSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	// A large range with auto_start brings up hundreds of HTTP servers; the
	// timeout has to accommodate that.
	ctx, cancel := context.WithTimeout(context.Background(), batchTimeout)
	defer cancel()

	devices, err := h.manager.CreateDeviceRange(ctx, spec)
	if err != nil {
		h.writeError(c, err)
		return
	}

	created := make([]gin.H, 0, len(devices))
	ids := make([]int, 0, len(devices))
	for _, d := range devices {
		created = append(created, gin.H{"id": d.ID, "name": d.Name, "port": d.Port})
		ids = append(ids, d.ID)
	}

	resp := gin.H{"count": len(devices), "created": created}

	if spec.AutoStart {
		// Same bounded concurrency as the batch endpoint.
		results := runBatch(ctx, ids, batchConcurrency, func(_ context.Context, id int) error {
			return h.manager.Start(id)
		})
		started := 0
		var failures []gin.H
		for _, r := range results {
			if r.OK {
				started++
				continue
			}
			failures = append(failures, gin.H{"id": r.ID, "error": r.Error})
		}
		resp["started"] = started
		if len(failures) > 0 {
			resp["start_errors"] = failures
		}
	}

	c.JSON(http.StatusCreated, resp)
}

// apiUpdateEmulator replaces the editable fields of a stopped emulator.
func (h *Handler) apiUpdateEmulator(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	var spec emulator.DeviceSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dev, err := h.manager.UpdateDevice(ctx, id, spec)
	if err != nil {
		h.writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"emulator": dev})
}

// apiDeleteEmulator removes an emulator and purges its data.
func (h *Handler) apiDeleteEmulator(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := h.manager.DeleteDevice(ctx, id); err != nil {
		h.writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"removed": id})
}
