package hikvision

import (
	"sync"
	"time"
)

// AcsEventRecord is one InfoList row in the POST /ISAPI/AccessControl/AcsEvent
// response. It only carries the fields an access-control client reads when
// collecting offline events.
type AcsEventRecord struct {
	Time         time.Time
	Major        int
	Minor        int
	CardNo       string
	Name         string
	EmployeeNo   string
	CardReaderNo int
	DoorNo       int
	SerialNo     int
}

// eventLog holds the events the device keeps "in memory" for offline queries.
// It is a ring buffer per emulator instance: restarting the emulator wipes the
// history, like a device that lost power.
type eventLog struct {
	mu   sync.Mutex
	cap  int
	recs []AcsEventRecord
}

func newEventLog(capacity int) *eventLog {
	if capacity <= 0 {
		capacity = 500
	}
	return &eventLog{cap: capacity, recs: make([]AcsEventRecord, 0, capacity)}
}

// Append adds an event, dropping the oldest one when the buffer is full.
func (l *eventLog) Append(rec AcsEventRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.recs) >= l.cap {
		l.recs = append(l.recs[:0], l.recs[len(l.recs)-l.cap+1:]...)
	}
	l.recs = append(l.recs, rec)
}

// Page returns a slice starting at offset (oldest to newest, which is the order
// clients page through) and the total number of stored events.
func (l *eventLog) Page(offset, max int) ([]AcsEventRecord, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	total := len(l.recs)
	if offset < 0 || offset >= total || max <= 0 {
		return nil, total
	}
	end := offset + max
	if end > total {
		end = total
	}
	out := make([]AcsEventRecord, end-offset)
	copy(out, l.recs[offset:end])
	return out, total
}

// PurgeBefore removes events older than t and returns how many were removed.
// Clients request this with StorageCfg {"mode":"time","checkTime":...} after
// collecting the offline events.
func (l *eventLog) PurgeBefore(t time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	kept := l.recs[:0]
	removed := 0
	for _, r := range l.recs {
		if r.Time.Before(t) {
			removed++
			continue
		}
		kept = append(kept, r)
	}
	l.recs = kept
	return removed
}

// Clear empties the log and returns how many events were dropped.
func (l *eventLog) Clear() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.recs)
	l.recs = l.recs[:0]
	return n
}
