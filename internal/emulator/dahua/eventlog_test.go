package dahua

import (
	"strings"
	"testing"
	"time"
)

func TestDahuaEventLog_BetweenFiltersByTime(t *testing.T) {
	l := newDahuaEventLog(10)
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		l.Append(CardRecEntry{CreateTime: base.Add(time.Duration(i) * time.Hour), CardNo: "AA", UserID: i})
	}

	got := l.Between(base.Add(time.Hour), base.Add(3*time.Hour))
	if len(got) != 3 {
		t.Fatalf("inclusive range 1h..3h: got %d, want 3", len(got))
	}
	if got[0].UserID != 1 || got[2].UserID != 3 {
		t.Errorf("wrong window: %+v", got)
	}
}

func TestDahuaEventLog_RingDropsOldest(t *testing.T) {
	l := newDahuaEventLog(3)
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		l.Append(CardRecEntry{CreateTime: base.Add(time.Duration(i) * time.Minute), UserID: i})
	}
	if n := l.Count(); n != 3 {
		t.Fatalf("capacity not enforced: got %d, want 3", n)
	}
	all := l.Between(base, base.Add(time.Hour))
	if all[0].UserID != 2 {
		t.Errorf("oldest retained: got UserID=%d, want 2", all[0].UserID)
	}
}

func TestDahuaEventLog_RenderMatchesDeviceFormat(t *testing.T) {
	l := newDahuaEventLog(10)
	when := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	recs := []CardRecEntry{{
		CreateTime: when, CardNo: "0A0B0C0D", CardName: "John Doe",
		UserID: 7, ReaderID: 1, Method: 15, ErrorCode: 0, Status: 1,
	}}

	out := l.Render(recs)
	if !strings.HasPrefix(out, "found=1") {
		t.Errorf("response must start with found=N:\n%s", out)
	}
	for _, required := range []string{
		"records[0].CardNo=0A0B0C0D",
		"records[0].UserID=7",
		"records[0].ReaderID=1",
		"records[0].Method=15",
		"records[0].ErrorCode=0",
	} {
		if !strings.Contains(out, required) {
			t.Errorf("missing %q:\n%s", required, out)
		}
	}
	// CreateTime is an integer epoch; the client parses it as int seconds.
	expected := "records[0].CreateTime=" + itoaTest(when.Unix())
	if !strings.Contains(out, expected) {
		t.Errorf("CreateTime must be epoch (%s):\n%s", expected, out)
	}
}

func TestDahuaEventLog_RenderEmpty(t *testing.T) {
	l := newDahuaEventLog(10)
	if got := l.Render(nil); got != "found=0" {
		t.Errorf("no records must return found=0; got %q", got)
	}
}

func itoaTest(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
