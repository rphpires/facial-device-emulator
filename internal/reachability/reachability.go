// Package reachability decides whether the configured device ports can be
// reached from outside in the environment the application is running in.
//
// The application honors each device's configured port and never
// overrides it: this package neither picks ports nor rejects devices, it
// only points out which ones will not work and why.
//
// Evidence is read in order of reliability. A failed bind counts in any
// environment. Without a bind error, the environment rule applies: in Docker
// and WSL the verdict does not depend on the emulator having been started,
// because warning that a port is not published BEFORE the user starts it is
// the most useful moment for the warning. In a native environment the only
// possible evidence is the bind attempt: not having started is not "something
// is wrong", it is the normal state of a freshly installed package, which is
// why it has its own status, StatusNotStarted, instead of being counted as
// StatusUnknown.
package reachability

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind identifies the kind of environment the application is running in.
type Kind int

const (
	KindLinux Kind = iota
	KindWSL
	KindDocker
	KindWindows
)

func (k Kind) String() string {
	switch k {
	case KindLinux:
		return "linux"
	case KindWSL:
		return "wsl"
	case KindDocker:
		return "docker"
	case KindWindows:
		return "windows"
	default:
		return "unknown"
	}
}

// MarshalJSON encodes the kind as its string name.
func (k Kind) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(k.String())), nil
}

// PortRange is an inclusive range of ports.
type PortRange struct {
	Start int
	End   int
}

// Contains reports whether port is inside the range.
func (r PortRange) Contains(port int) bool {
	return port >= r.Start && port <= r.End
}

func (r PortRange) String() string {
	if r.Start == r.End {
		return strconv.Itoa(r.Start)
	}
	return fmt.Sprintf("%d-%d", r.Start, r.End)
}

// Environment is a snapshot of the environment, taken once at startup.
type Environment struct {
	Kind            Kind        `json:"kind"`
	PublishedRanges []PortRange `json:"published_ranges"`
	// RangesKnown false means it was not possible to find out what the host
	// publishes. In that case no warning is made up.
	RangesKnown bool `json:"ranges_known"`
	HostNetwork bool `json:"host_network"`
	WSLMirrored bool `json:"wsl_mirrored"`
	// MaxOpenFiles is not used by any Analyze rule: it is diagnostic only,
	// exposed in /api/reachability for anyone investigating an environment
	// with many devices and a tight file descriptor limit.
	MaxOpenFiles uint64 `json:"max_open_files"`
}

// Status is the verdict for a device.
type Status int

const (
	StatusOK Status = iota
	StatusUnreachable
	StatusUnknown
	// StatusNotStarted only means the emulator is not running, not that
	// something is wrong and could not be verified. A freshly installed
	// package with every device stopped is the normal state and must not be
	// counted together with StatusUnknown.
	StatusNotStarted
)

// String returns the status as sent in the JSON API. The values are part of
// the /api/reachability contract consumed by the web UI.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusUnreachable:
		return "unreachable"
	case StatusNotStarted:
		return "not_started"
	default:
		return "unknown"
	}
}

// MarshalJSON encodes the status as its string value.
func (s Status) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(s.String())), nil
}

// DevicePort is the input of Analyze: the port configured for a device and
// what happened when the emulator tried to open it.
type DevicePort struct {
	DeviceID int
	Port     int
	// Started means the emulator was started at some point. Without it there
	// is no bind information, and the honest verdict is "not started", not
	// "unknown", which is reserved for when something may be wrong and could
	// not be confirmed.
	Started bool
	// BindError is the operating system error from opening the port, empty
	// when the bind succeeded.
	BindError string
}

// DeviceReachability is the per-device verdict.
type DeviceReachability struct {
	DeviceID int    `json:"device_id"`
	Port     int    `json:"port"`
	Status   Status `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

// Report is what the UI consumes.
type Report struct {
	Environment Environment          `json:"environment"`
	Devices     []DeviceReachability `json:"devices"`
	Unreachable int                  `json:"unreachable"`
	Unknown     int                  `json:"unknown"`
	NotStarted  int                  `json:"not_started"`
}

// ParseRanges parses the format written to PUBLISHED_PORT_RANGE by the
// compose file: comma-separated entries, each either "port" or "start-end".
func ParseRanges(s string) ([]PortRange, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty port range")
	}

	var ranges []PortRange
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("empty entry in %q", s)
		}

		start, end, hasDash := strings.Cut(entry, "-")
		if !hasDash {
			end = start
		}

		p1, err := parsePort(start)
		if err != nil {
			return nil, fmt.Errorf("in %q: %w", entry, err)
		}
		p2, err := parsePort(end)
		if err != nil {
			return nil, fmt.Errorf("in %q: %w", entry, err)
		}
		if p1 > p2 {
			return nil, fmt.Errorf("in %q: start greater than end", entry)
		}
		ranges = append(ranges, PortRange{Start: p1, End: p2})
	}
	return ranges, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%q is not a port number", s)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %d outside the 1-65535 range", n)
	}
	return n, nil
}

// Analyze is pure: the same arguments always give the same report. No I/O,
// no clock, no database.
func Analyze(ports []DevicePort, env Environment) Report {
	// Devices starts as an empty slice, not nil: a nil slice becomes "null"
	// in JSON, and the devices screen expects a list even when no device is
	// registered.
	r := Report{Environment: env, Devices: []DeviceReachability{}}

	for _, p := range ports {
		d := DeviceReachability{DeviceID: p.DeviceID, Port: p.Port}

		switch {
		// A failed bind is direct evidence and counts in any environment:
		// no environment rule may mask it.
		case p.BindError != "":
			d.Status = StatusUnreachable
			d.Reason = fmt.Sprintf("port %d could not be opened: %s", p.Port, p.BindError)
		case env.Kind == KindDocker && !env.HostNetwork:
			d.Status, d.Reason = dockerVerdict(p, env)
		case env.Kind == KindWSL && !env.WSLMirrored:
			d.Status = StatusUnreachable
			d.Reason = fmt.Sprintf("WSL without mirrored networking: only this machine can reach port %d", p.Port)
		default:
			d.Status, d.Reason = nativeVerdict(p)
		}

		switch d.Status {
		case StatusUnreachable:
			r.Unreachable++
		case StatusUnknown:
			r.Unknown++
		case StatusNotStarted:
			r.NotStarted++
		}
		r.Devices = append(r.Devices, d)
	}

	return r
}

func dockerVerdict(p DevicePort, env Environment) (Status, string) {
	if !env.RangesKnown {
		return StatusUnknown, "could not determine which ports this Docker environment publishes"
	}
	for _, pr := range env.PublishedRanges {
		if pr.Contains(p.Port) {
			return StatusOK, ""
		}
	}
	return StatusUnreachable, fmt.Sprintf("port %d is not published in this Docker environment", p.Port)
}

func nativeVerdict(p DevicePort) (Status, string) {
	if !p.Started {
		return StatusNotStarted, "the emulator has not been started yet"
	}
	return StatusOK, ""
}
