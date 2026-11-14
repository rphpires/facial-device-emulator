package devicetime

import (
	"testing"
	"time"
)

func TestParseHikvisionTimeZone(t *testing.T) {
	cases := []struct {
		in     string
		offset int
	}{
		{"CST+3:00:00", -3 * 3600},
		{"CST-8:00:00", 8 * 3600},
		{"CST-5:30:00", 5*3600 + 30*60},
		{"CST+0:00:00", 0},
		{"CST-1:00:00DST01:00:00,M3.5.0/02:00:00,M10.5.0/03:00:00", 3600},
	}
	for _, tc := range cases {
		loc, err := ParseHikvisionTimeZone(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if _, off := time.Now().In(loc).Zone(); off != tc.offset {
			t.Errorf("%q: offset %d, want %d", tc.in, off, tc.offset)
		}
	}
	for _, bad := range []string{"", "CST", "CST+x", "CST+99:00:00"} {
		if _, err := ParseHikvisionTimeZone(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestFormatHikvisionTimeZone_RoundTrip(t *testing.T) {
	for _, s := range []string{"CST+3:00:00", "CST-8:00:00", "CST-5:30:00", "CST+0:00:00"} {
		loc, err := ParseHikvisionTimeZone(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatHikvisionTimeZone(time.Now().In(loc)); got != s {
			t.Errorf("round trip %q -> %q", s, got)
		}
	}
}

func TestSetDefaultName(t *testing.T) {
	t.Cleanup(func() { SetDefault(time.UTC) })
	if Default() != time.UTC {
		t.Fatalf("default must start as UTC, got %v", Default())
	}
	if err := SetDefaultName("Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	if _, off := time.Now().In(Default()).Zone(); off != 9*3600 {
		t.Fatalf("Asia/Tokyo offset %d", off)
	}
	if err := SetDefaultName("Not/AZone"); err == nil {
		t.Fatal("expected an error for an unknown zone")
	}
	if err := SetDefaultName(""); err != nil || Default() != time.UTC {
		t.Fatalf("empty name must reset to UTC")
	}
}
