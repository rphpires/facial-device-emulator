package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/emulator"
	"github.com/rphpires/facial-device-emulator/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v4"
)

// batchBackend is the part of *emulator.Manager the batch endpoint uses.
// Keeping it an interface lets the handler tests drive mixed outcomes
// without a database.
type batchBackend interface {
	GetDevice(id int) (models.Device, error)
	Start(id int) error
	Stop(id int) error
	DeleteDevice(ctx context.Context, id int) error
	UpdateDevice(ctx context.Context, id int, spec emulator.DeviceSpec) (models.Device, error)
	UpdateDeviceSettings(id int, logEnabled bool) error
}

// Batch actions accepted by POST /api/emulators/batch.
const (
	batchStart      = "start"
	batchStop       = "stop"
	batchDelete     = "delete"
	batchEnable     = "enable"
	batchDisable    = "disable"
	batchEnableLog  = "enable_log"
	batchDisableLog = "disable_log"
)

var batchActions = map[string]bool{
	batchStart: true, batchStop: true, batchDelete: true,
	batchEnable: true, batchDisable: true,
	batchEnableLog: true, batchDisableLog: true,
}

const (
	// maxBatchSize matches the largest range the API can create, so any
	// range can be managed in one call.
	maxBatchSize = emulator.MaxRangeSize

	// batchConcurrency bounds how many items run at once. A start opens a
	// listener and can take up to the manager's start timeout; running a
	// 1000-device batch serially would take minutes, while running it all at
	// once would stampede the database pool and the scheduler.
	batchConcurrency = 32

	// batchTimeout caps the whole request. Items not reached in time are
	// reported as failed rather than left running after the response.
	batchTimeout = 5 * time.Minute
)

// batchRequest is the body of POST /api/emulators/batch.
type batchRequest struct {
	IDs    []int  `json:"ids"`
	Action string `json:"action"`
}

// batchResult is the outcome for one device. Skipped is set when the device
// was already in the requested state: the item counts as a success, and the
// reason tells the caller nothing was done.
type batchResult struct {
	ID      int    `json:"id"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Skipped string `json:"skipped,omitempty"`
}

// batchResponse aggregates the per-device results. Succeeded includes the
// skipped items.
type batchResponse struct {
	Action    string        `json:"action"`
	Requested int           `json:"requested"`
	Succeeded int           `json:"succeeded"`
	Failed    int           `json:"failed"`
	Skipped   int           `json:"skipped"`
	Results   []batchResult `json:"results"`
}

// errAlreadyInState marks an item that needed no work.
type errAlreadyInState string

func (e errAlreadyInState) Error() string { return string(e) }

// validateBatch checks the request and returns the de-duplicated IDs in
// their original order.
func validateBatch(req batchRequest) ([]int, error) {
	if !batchActions[req.Action] {
		return nil, fmt.Errorf("unknown action %q; use start, stop, delete, enable, disable, enable_log or disable_log", req.Action)
	}
	if len(req.IDs) == 0 {
		return nil, errors.New("ids must contain at least one device ID")
	}

	seen := make(map[int]bool, len(req.IDs))
	ids := make([]int, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 {
			return nil, fmt.Errorf("invalid device ID %d", id)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) > maxBatchSize {
		return nil, fmt.Errorf("batch too large: %d devices, the limit is %d", len(ids), maxBatchSize)
	}
	return ids, nil
}

// apiBatchEmulators applies one action to a list of devices.
//
//	POST /api/emulators/batch
//	{"ids": [1, 2, 3], "action": "start"}
//
// The response is 200 whenever the request itself is valid, even if some
// items failed: each result carries its own ok/error. Only a malformed
// request answers 400.
func (h *Handler) apiBatchEmulators(c *gin.Context) {
	var req batchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: " + err.Error()})
		return
	}

	ids, err := validateBatch(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), batchTimeout)
	defer cancel()

	h.tracer.Info("Batch %s on %d device(s)", req.Action, len(ids))
	results := runBatch(ctx, ids, batchConcurrency, func(ctx context.Context, id int) error {
		return h.applyBatchAction(ctx, req.Action, id)
	})

	resp := summarizeBatch(req.Action, results)
	if resp.Failed > 0 {
		h.tracer.Warning("Batch %s: %d of %d failed", req.Action, resp.Failed, resp.Requested)
	}
	c.JSON(http.StatusOK, resp)
}

// runBatch runs fn for every ID with at most limit calls in flight and
// returns the results in the order of ids. Once ctx is done, the remaining
// items fail without being attempted.
func runBatch(ctx context.Context, ids []int, limit int, fn func(context.Context, int) error) []batchResult {
	results := make([]batchResult, len(ids))
	if limit < 1 {
		limit = 1
	}

	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, id := range ids {
		results[i].ID = id
		if ctx.Err() != nil {
			results[i].Error = "not attempted: batch timed out"
			continue
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			results[i].Error = "not attempted: batch timed out"
			continue
		}

		wg.Add(1)
		go func(i, id int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = resultOf(id, fn(ctx, id))
		}(i, id)
	}
	wg.Wait()
	return results
}

func resultOf(id int, err error) batchResult {
	var already errAlreadyInState
	switch {
	case err == nil:
		return batchResult{ID: id, OK: true}
	case errors.As(err, &already):
		return batchResult{ID: id, OK: true, Skipped: already.Error()}
	default:
		return batchResult{ID: id, Error: err.Error()}
	}
}

func summarizeBatch(action string, results []batchResult) batchResponse {
	resp := batchResponse{Action: action, Requested: len(results), Results: results}
	for _, r := range results {
		switch {
		case !r.OK:
			resp.Failed++
		case r.Skipped != "":
			resp.Succeeded++
			resp.Skipped++
		default:
			resp.Succeeded++
		}
	}
	return resp
}

// applyBatchAction performs one action on one device using the existing
// manager operations. Start, stop, enable and disable read the device first
// so a device already in the requested state is reported as skipped
// instead of failed.
func (h *Handler) applyBatchAction(ctx context.Context, action string, id int) error {
	b := h.batch

	switch action {
	case batchDelete:
		return b.DeleteDevice(ctx, id)
	case batchEnableLog, batchDisableLog:
		if _, err := b.GetDevice(id); err != nil {
			return lookupError(id, err)
		}
		return b.UpdateDeviceSettings(id, action == batchEnableLog)
	}

	dev, err := b.GetDevice(id)
	if err != nil {
		return lookupError(id, err)
	}
	running := dev.Status == "running"

	switch action {
	case batchStart:
		if dev.Enabled == 0 {
			return errors.New("device is disabled")
		}
		if running {
			return errAlreadyInState("already running")
		}
		return b.Start(id)

	case batchStop:
		if !running {
			return errAlreadyInState("not running")
		}
		return b.Stop(id)

	case batchEnable, batchDisable:
		want := action == batchEnable
		if (dev.Enabled != 0) == want {
			return errAlreadyInState("already " + action + "d")
		}
		// A disabled device cannot run, and the manager only edits
		// stopped devices.
		if running {
			if err := b.Stop(id); err != nil {
				return fmt.Errorf("stopping before %s: %w", action, err)
			}
		}
		_, err := b.UpdateDevice(ctx, id, specFromDevice(dev, want))
		return err
	}

	return fmt.Errorf("unknown action %q", action)
}

// specFromDevice rebuilds the editable fields of a device so an update can
// change only Enabled.
func specFromDevice(d models.Device, enabled bool) emulator.DeviceSpec {
	return emulator.DeviceSpec{
		Name:          d.Name,
		Model:         d.Model,
		IPAddress:     d.IPAddress,
		Port:          d.Port,
		EventInterval: d.EventInterval,
		Enabled:       &enabled,
	}
}

// lookupError turns a GetDevice failure into a short per-item message.
func lookupError(id int, err error) error {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, emulator.ErrDeviceNotFound) {
		return fmt.Errorf("device %d not found", id)
	}
	return fmt.Errorf("reading device %d: %w", id, err)
}
