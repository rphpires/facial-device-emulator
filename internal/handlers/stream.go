package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/emulator"
	"github.com/rphpires/facial-device-emulator/internal/models"

	"github.com/gin-gonic/gin"
)

// streamKeepalive is the interval of the comment frame that keeps the
// connection alive. Proxies tend to drop idle connections after 60s; 20s
// leaves room for three beats before that.
const streamKeepalive = 20 * time.Second

// streamRetry is how long the browser waits before reconnecting on its
// own, in milliseconds. Sent once, when the stream opens.
const streamRetry = 3000

// streamCoalesce is the window in which consecutive notifications collapse
// into one frame. "Start all" emits one notification per emulator; without
// the window each one would cost a full fleet read per open tab.
const streamCoalesce = 150 * time.Millisecond

// ServerWriteTimeout is the http.Server WriteTimeout. It must outlast the
// slowest regular handler, the batch and range endpoints, which give
// themselves batchTimeout (batch.go) to finish; the extra 30 s leaves room
// to write the response after that budget runs out. The /events stream
// clears its own write deadline, so this timeout does not cut it.
const ServerWriteTimeout = batchTimeout + 30*time.Second

// deviceView is the shape of a device on the SSE wire. It carries
// everything the table shows, so the client can draw a whole row (including
// a new one) without a second request.
type deviceView struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Model      string `json:"model"`
	IPAddress  string `json:"ip_address"`
	Port       int    `json:"port"`
	Status     string `json:"status"`
	Enabled    int    `json:"enabled"`
	LogEnabled int    `json:"log_enabled"`
	Interval   int    `json:"interval"`
	TotalUsers int    `json:"total_users"`
	Mode       string `json:"mode"`
	LastError  string `json:"last_error,omitempty"`
}

// newDeviceView applies the same rule as countFleet: Enabled == 0 wins
// over the stored Status, so the header and the table never disagree.
func newDeviceView(d models.Device) deviceView {
	status := d.Status
	if d.Enabled == 0 {
		status = "disabled"
	}

	return deviceView{
		ID:         d.ID,
		Name:       d.Name,
		Model:      d.Model,
		IPAddress:  d.IPAddress,
		Port:       d.Port,
		Status:     status,
		Enabled:    d.Enabled,
		LogEnabled: d.LogEnabled,
		Interval:   d.EventInterval,
		TotalUsers: d.TotalUsers,
		Mode:       modeStandalone,
	}
}

// writeSSE serializes one SSE frame. The JSON stays on a single line
// because a newline inside "data:" would end the frame halfway.
func writeSSE(w io.Writer, event string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal SSE payload: %w", err)
	}

	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}

// pendingChanges accumulates the notifications of one coalescing window.
type pendingChanges struct {
	changed map[int]bool
	removed map[int]bool
	resync  bool
}

func newPendingChanges() *pendingChanges {
	return &pendingChanges{changed: map[int]bool{}, removed: map[int]bool{}}
}

func (p *pendingChanges) empty() bool {
	return !p.resync && len(p.changed) == 0 && len(p.removed) == 0
}

// record folds in one notification. The latest event for a device wins:
// a removal after a change counts as removed, and vice versa.
func (p *pendingChanges) record(ev emulator.FleetEvent) {
	switch ev.Kind {
	case emulator.FleetResync:
		p.resync = true
	case emulator.FleetRemoved:
		delete(p.changed, ev.DeviceID)
		p.removed[ev.DeviceID] = true
	default:
		delete(p.removed, ev.DeviceID)
		p.changed[ev.DeviceID] = true
	}
}

// handleStream serves /events.
//
// Protocol:
//
//	snapshot -> { devices: [...], counts }            full state
//	delta    -> { devices: [...], removed: [ids], counts }
//
// The snapshot is sent on connect, after bulk changes (e.g. a large range
// create) and whenever this client's channel overflowed: a notification was
// lost, and only a full reread guarantees the screen is not wrong.
func (h *Handler) handleStream(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// Turn off nginx buffering: with it on, frames get stuck in the proxy
	// and the stream arrives in bursts or not at all.
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		h.tracer.Error("SSE: ResponseWriter does not support Flush")
		c.Status(http.StatusInternalServerError)
		return
	}

	// The stream lives for as long as the tab is open; without this the
	// server's WriteTimeout would cut it every few minutes. gin's writer
	// implements Unwrap, so the controller reaches the net/http one. The
	// stream still ends on server shutdown: the request context derives
	// from the server's BaseContext, which main cancels on shutdown.
	if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{}); err != nil {
		h.tracer.Warning("SSE: could not clear the write deadline: %v", err)
	}

	if _, err := fmt.Fprintf(c.Writer, "retry: %d\n\n", streamRetry); err != nil {
		return
	}

	// Subscribe before the snapshot: a notification arriving in between
	// lands in the next window instead of being lost.
	listener := h.manager.AddFleetListener()
	defer h.manager.RemoveFleetListener(listener)

	if err := h.writeSnapshot(c.Request.Context(), c.Writer); err != nil {
		h.tracer.Error("SSE: failed to send snapshot: %v", err)
		return
	}
	flusher.Flush()

	keepalive := time.NewTicker(streamKeepalive)
	defer keepalive.Stop()

	// The window timer stays stopped until the first notification.
	window := time.NewTimer(time.Hour)
	window.Stop()
	pending := newPendingChanges()

	clientGone := c.Request.Context().Done()

	for {
		select {
		case ev, ok := <-listener.C:
			if !ok {
				return
			}
			if pending.empty() {
				window.Reset(streamCoalesce)
			}
			pending.record(ev)

		case <-window.C:
			var err error
			if pending.resync || listener.TakeOverflow() {
				err = h.writeSnapshot(c.Request.Context(), c.Writer)
			} else {
				err = h.writeDelta(c.Request.Context(), c.Writer, pending)
			}
			pending = newPendingChanges()
			if err != nil {
				h.tracer.Error("SSE: failed to send update: %v", err)
				return
			}
			flusher.Flush()

		case <-keepalive.C:
			// An SSE comment keeps the connection warm and is ignored by
			// EventSource. The beat also recovers an overflow that was not
			// followed by any later notification.
			if listener.TakeOverflow() {
				if err := h.writeSnapshot(c.Request.Context(), c.Writer); err != nil {
					return
				}
			} else if _, err := fmt.Fprint(c.Writer, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()

		case <-clientGone:
			return
		}
	}
}

// fleetViews reads the fleet and builds the views with mode and last
// start error.
func (h *Handler) fleetViews(ctx context.Context) ([]deviceView, FleetCounts, error) {
	devices, err := h.manager.ListDevices()
	if err != nil {
		return nil, FleetCounts{}, err
	}

	ids := make([]int32, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, int32(d.ID))
	}
	modes, err := h.getDeviceModes(ctx, ids)
	if err != nil {
		// An inaccurate mode column beats a dead stream.
		h.tracer.Error("SSE: failed to read device modes: %v", err)
		modes = map[int]string{}
	}

	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		v := newDeviceView(d)
		if mode, ok := modes[d.ID]; ok {
			v.Mode = mode
		}
		v.LastError = h.manager.LastStartError(d.ID)
		views = append(views, v)
	}
	return views, countFleet(devices), nil
}

// writeSnapshot sends the whole fleet, so the client starts from a known
// state instead of trusting what came in the HTML.
func (h *Handler) writeSnapshot(ctx context.Context, w io.Writer) error {
	views, counts, err := h.fleetViews(ctx)
	if err != nil {
		return err
	}

	return writeSSE(w, "snapshot", gin.H{
		"devices": views,
		"counts":  counts,
	})
}

// writeDelta sends only the devices that changed in the window. A changed
// ID that is no longer in the fleet is reported as removed.
func (h *Handler) writeDelta(ctx context.Context, w io.Writer, p *pendingChanges) error {
	views, counts, err := h.fleetViews(ctx)
	if err != nil {
		return err
	}

	changed := make([]deviceView, 0, len(p.changed))
	present := make(map[int]bool, len(views))
	for _, v := range views {
		present[v.ID] = true
		if p.changed[v.ID] {
			changed = append(changed, v)
		}
	}

	removed := make([]int, 0, len(p.removed))
	for id := range p.removed {
		if !present[id] {
			removed = append(removed, id)
		}
	}
	for id := range p.changed {
		if !present[id] {
			removed = append(removed, id)
		}
	}

	return writeSSE(w, "delta", gin.H{
		"devices": changed,
		"removed": removed,
		"counts":  counts,
	})
}
