package hikvision

import (
	"testing"
	"time"
)

func TestEventLog_AppendAndPage(t *testing.T) {
	l := newEventLog(10)
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		l.Append(AcsEventRecord{Time: base.Add(time.Duration(i) * time.Minute), Major: 5, Minor: MinorFaceVerifyPass})
	}

	recs, total := l.Page(0, 3)
	if total != 5 {
		t.Errorf("total: got %d, want 5", total)
	}
	if len(recs) != 3 {
		t.Fatalf("len(recs): got %d, want 3", len(recs))
	}
	if !recs[0].Time.Equal(base) {
		t.Errorf("first page must start at the oldest event; got %v", recs[0].Time)
	}

	recs, _ = l.Page(3, 3)
	if len(recs) != 2 {
		t.Errorf("second page: got %d, want 2", len(recs))
	}

	recs, _ = l.Page(99, 3)
	if len(recs) != 0 {
		t.Errorf("offset past the end must return empty; got %d", len(recs))
	}
}

func TestEventLog_RingDropsOldest(t *testing.T) {
	l := newEventLog(3)
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		l.Append(AcsEventRecord{Time: base.Add(time.Duration(i) * time.Minute), SerialNo: i})
	}
	recs, total := l.Page(0, 10)
	if total != 3 || len(recs) != 3 {
		t.Fatalf("capacity not honored: total=%d len=%d", total, len(recs))
	}
	if recs[0].SerialNo != 2 {
		t.Errorf("oldest retained: got serialNo=%d, want 2", recs[0].SerialNo)
	}
}

func TestEventLog_PurgeBefore(t *testing.T) {
	l := newEventLog(10)
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		l.Append(AcsEventRecord{Time: base.Add(time.Duration(i) * time.Minute)})
	}
	n := l.PurgeBefore(base.Add(3 * time.Minute))
	if n != 3 {
		t.Errorf("purged: got %d, want 3", n)
	}
	_, total := l.Page(0, 10)
	if total != 2 {
		t.Errorf("remaining: got %d, want 2", total)
	}
}

func TestEventLog_Clear(t *testing.T) {
	l := newEventLog(10)
	l.Append(AcsEventRecord{Time: time.Now()})
	l.Append(AcsEventRecord{Time: time.Now()})

	if n := l.Clear(); n != 2 {
		t.Errorf("Clear: got %d, want 2", n)
	}
	if _, total := l.Page(0, 10); total != 0 {
		t.Errorf("log must be empty after Clear; got %d", total)
	}
}
