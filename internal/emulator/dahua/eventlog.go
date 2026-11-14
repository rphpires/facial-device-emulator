package dahua

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// CardRecEntry is one row of the AccessControlCardRec table — the access
// history the client scans to collect events after the device was offline.
type CardRecEntry struct {
	CreateTime time.Time
	CardNo     string
	CardName   string
	UserID     int
	ReaderID   int
	Method     int
	ErrorCode  int
	Status     int
}

// dahuaEventLog is the in-memory history, per emulator instance.
// Restarting the emulator clears it, like a device that lost power.
type dahuaEventLog struct {
	mu   sync.Mutex
	cap  int
	recs []CardRecEntry
}

func newDahuaEventLog(capacity int) *dahuaEventLog {
	if capacity <= 0 {
		capacity = 500
	}
	return &dahuaEventLog{cap: capacity, recs: make([]CardRecEntry, 0, capacity)}
}

func (l *dahuaEventLog) Append(e CardRecEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.recs) >= l.cap {
		l.recs = append(l.recs[:0], l.recs[len(l.recs)-l.cap+1:]...)
	}
	l.recs = append(l.recs, e)
}

// Between returns the records with CreateTime in [start, end], inclusive at
// both ends — the client sends StartTime/EndTime as epoch seconds.
func (l *dahuaEventLog) Between(start, end time.Time) []CardRecEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []CardRecEntry
	for _, r := range l.recs {
		if r.CreateTime.Before(start) || r.CreateTime.After(end) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Count returns how many records exist (used by getQuerySize).
func (l *dahuaEventLog) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recs)
}

// Render serializes in Dahua's table format: one line per field, prefixed
// with records[i]. The client splits each line on '=' and drops everything
// before the first dot.
func (l *dahuaEventLog) Render(recs []CardRecEntry) string {
	if len(recs) == 0 {
		return "found=0"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "found=%d\r\n", len(recs))
	for i, r := range recs {
		fmt.Fprintf(&b, "records[%d].CardName=%s\r\n", i, r.CardName)
		fmt.Fprintf(&b, "records[%d].CardNo=%s\r\n", i, r.CardNo)
		fmt.Fprintf(&b, "records[%d].CardType=0\r\n", i)
		fmt.Fprintf(&b, "records[%d].CreateTime=%d\r\n", i, r.CreateTime.Unix())
		fmt.Fprintf(&b, "records[%d].Door=0\r\n", i)
		fmt.Fprintf(&b, "records[%d].ErrorCode=%d\r\n", i, r.ErrorCode)
		fmt.Fprintf(&b, "records[%d].Method=%d\r\n", i, r.Method)
		fmt.Fprintf(&b, "records[%d].ReaderID=%d\r\n", i, r.ReaderID)
		fmt.Fprintf(&b, "records[%d].RecNo=%d\r\n", i, i+1)
		fmt.Fprintf(&b, "records[%d].Status=%d\r\n", i, r.Status)
		fmt.Fprintf(&b, "records[%d].UserID=%d\r\n", i, r.UserID)
		fmt.Fprintf(&b, "records[%d].UserType=0\r\n", i)
	}
	return b.String()
}
