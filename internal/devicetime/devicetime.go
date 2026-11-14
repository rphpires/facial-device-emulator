// Package devicetime holds the time zone the emulated devices use to stamp
// events and report their local time.
//
// Real terminals report time in their configured time zone. Each emulator can
// override the zone (Hikvision: PUT /ISAPI/System/time); when it has none,
// the process-wide default set from the emulator.default_timezone config key
// (UTC unless configured) applies. The zone never depends on the host's
// local time zone.
package devicetime

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata" // embed the zone database: container images may lack it
)

var defaultLocation atomic.Pointer[time.Location]

func init() {
	defaultLocation.Store(time.UTC)
}

// Default returns the default device time zone.
func Default() *time.Location {
	return defaultLocation.Load()
}

// SetDefault replaces the default device time zone. A nil loc resets it to UTC.
func SetDefault(loc *time.Location) {
	if loc == nil {
		loc = time.UTC
	}
	defaultLocation.Store(loc)
}

// SetDefaultName loads an IANA time zone name (e.g. "UTC",
// "America/Sao_Paulo") and makes it the default. An empty name means UTC.
func SetDefaultName(name string) error {
	if name == "" {
		SetDefault(time.UTC)
		return nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return fmt.Errorf("invalid time zone %q: %w", name, err)
	}
	SetDefault(loc)
	return nil
}

// ParseHikvisionTimeZone parses the ISAPI <timeZone> value, a POSIX-style
// string such as "CST-8:00:00" (UTC+08:00) or "CST+3:00:00" (UTC-03:00).
// As in POSIX TZ strings, the sign is inverted relative to the UTC offset.
// Any DST rule following the base offset is ignored: the result is a fixed
// zone.
func ParseHikvisionTimeZone(s string) (*time.Location, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, "+-")
	if i < 0 {
		return nil, fmt.Errorf("invalid ISAPI time zone %q: missing offset", s)
	}
	sign := 1
	if s[i] == '+' {
		sign = -1 // POSIX: "+3" means three hours west of UTC
	}
	rest := s[i+1:]
	end := 0
	for end < len(rest) && (rest[end] == ':' || (rest[end] >= '0' && rest[end] <= '9')) {
		end++
	}
	parts := strings.Split(rest[:end], ":")
	if len(parts) == 0 || len(parts) > 3 || parts[0] == "" {
		return nil, fmt.Errorf("invalid ISAPI time zone %q", s)
	}
	limits := []int{14, 59, 59}
	units := []int{3600, 60, 1}
	seconds := 0
	for k, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > limits[k] {
			return nil, fmt.Errorf("invalid ISAPI time zone %q", s)
		}
		seconds += n * units[k]
	}
	offset := sign * seconds
	return time.FixedZone(FormatOffset(offset), offset), nil
}

// FormatHikvisionTimeZone renders the zone in effect at t in the ISAPI
// <timeZone> format (e.g. UTC-03:00 -> "CST+3:00:00").
func FormatHikvisionTimeZone(t time.Time) string {
	_, offset := t.Zone()
	sign := "+"
	if offset > 0 {
		sign = "-"
	} else {
		offset = -offset
	}
	return fmt.Sprintf("CST%s%d:%02d:%02d", sign, offset/3600, offset%3600/60, offset%60)
}

// FormatOffset renders an offset in seconds east of UTC as "+hh:mm".
func FormatOffset(offset int) string {
	sign := '+'
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	return fmt.Sprintf("%c%02d:%02d", sign, offset/3600, offset%3600/60)
}
