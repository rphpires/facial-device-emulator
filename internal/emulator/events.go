package emulator

import (
	"sync"
	"sync/atomic"
)

// FleetEventKind tells the consumer what to re-read.
type FleetEventKind string

const (
	// FleetChanged: some device field changed (state, user count,
	// registration, log, mode) or the device was just created.
	FleetChanged FleetEventKind = "changed"
	// FleetRemoved: the device no longer exists.
	FleetRemoved FleetEventKind = "removed"
	// FleetResync: bulk change (e.g. a large range create); the consumer
	// should reread the whole fleet instead of device by device.
	FleetResync FleetEventKind = "resync"
)

// FleetEvent signals that something in the fleet changed. It does not carry
// the device: consumers re-read from the database, the single source of
// truth, so a burst of events costs a single read.
type FleetEvent struct {
	Kind     FleetEventKind `json:"kind"`
	DeviceID int            `json:"device_id"`
}

// fleetListenerBuffer holds a whole StartAll of a large fleet without
// dropping. If it still fills up, the listener is flagged and the consumer
// does a full re-read, so no event is ever lost silently.
const fleetListenerBuffer = 1024

// FleetListener is a subscription to fleet events.
type FleetListener struct {
	C        chan FleetEvent
	overflow atomic.Bool
}

// TakeOverflow reports whether any event was dropped since the last call,
// and clears the flag.
func (l *FleetListener) TakeOverflow() bool {
	return l.overflow.Swap(false)
}

// fleetHub fans events out to listeners. Sending is synchronous and
// non-blocking, so delivery order matches the order the changes happened
// (a goroutine per event could deliver "stopped" before the "running" that
// preceded it).
type fleetHub struct {
	mu        sync.RWMutex
	listeners []*FleetListener
}

func (h *fleetHub) add() *FleetListener {
	l := &FleetListener{C: make(chan FleetEvent, fleetListenerBuffer)}
	h.mu.Lock()
	h.listeners = append(h.listeners, l)
	h.mu.Unlock()
	return l
}

func (h *fleetHub) remove(l *FleetListener) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, cur := range h.listeners {
		if cur == l {
			close(l.C)
			h.listeners = append(h.listeners[:i], h.listeners[i+1:]...)
			return
		}
	}
}

func (h *fleetHub) publish(ev FleetEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, l := range h.listeners {
		select {
		case l.C <- ev:
		default:
			l.overflow.Store(true)
		}
	}
}

// AddFleetListener subscribes to fleet events. Subscribers must call
// RemoveFleetListener when done.
func (m *Manager) AddFleetListener() *FleetListener {
	return m.hub.add()
}

// RemoveFleetListener ends the subscription and closes the channel.
func (m *Manager) RemoveFleetListener(l *FleetListener) {
	m.hub.remove(l)
}

// NotifyChanged signals that the device changed. Exported for changes
// persisted outside the Manager (device mode, in the handler).
func (m *Manager) NotifyChanged(deviceID int) {
	m.hub.publish(FleetEvent{Kind: FleetChanged, DeviceID: deviceID})
}

func (m *Manager) notifyRemoved(deviceID int) {
	m.hub.publish(FleetEvent{Kind: FleetRemoved, DeviceID: deviceID})
}

func (m *Manager) notifyResync() {
	m.hub.publish(FleetEvent{Kind: FleetResync})
}
