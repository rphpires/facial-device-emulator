package emulator

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Models createEmulator() knows how to instantiate. Any other value would
// register a device that could never start.
const (
	ModelHikvision = "Hikvision"
	ModelDahua     = "Dahua"
)

const (
	// MaxRangeSize is the width of the port range published in
	// docker-compose.yml (4000-4999). A larger batch would register emulators
	// that are unreachable under Docker.
	MaxRangeSize = 1000

	// DefaultIP and DefaultInterval apply when the payload omits the fields.
	DefaultIP       = "127.0.0.1"
	DefaultInterval = 10
)

var (
	// ErrInvalidSpec is the root of every client payload error; the handler
	// maps it to 400. ConflictError matches it too.
	ErrInvalidSpec = errors.New("invalid data")

	// ErrDeviceRunning: the emulator keeps an in-memory copy of
	// models.Device; editing it live would make the ISAPI/CGI responses lie.
	ErrDeviceRunning = errors.New("emulator is running")

	ErrDeviceNotFound = errors.New("device not found")

	// ErrNotRunning and ErrDeviceDisabled are state conflicts of start and
	// stop (409), like ErrDeviceRunning.
	ErrNotRunning     = errors.New("emulator is not running")
	ErrDeviceDisabled = errors.New("device is disabled")
)

// DeviceSpec is the body of POST /api/emulators and PUT /api/emulators/:id.
// Enabled is a pointer to tell "omitted" (defaults to true) from "explicitly
// false".
type DeviceSpec struct {
	Name          string `json:"name"`
	Model         string `json:"model"`
	IPAddress     string `json:"ip_address"`
	Port          int    `json:"port"`
	EventInterval int    `json:"event_interval"`
	Enabled       *bool  `json:"enabled"`
	AutoStart     bool   `json:"auto_start"`
}

// RangeSpec is the body of POST /api/emulators/range. Only the port varies
// between items: the bind is 0.0.0.0, so a range is N ports on one host.
type RangeSpec struct {
	NamePrefix    string `json:"name_prefix"`
	Model         string `json:"model"`
	IPAddress     string `json:"ip_address"`
	PortStart     int    `json:"port_start"`
	PortEnd       int    `json:"port_end"`
	EventInterval int    `json:"event_interval"`
	Enabled       *bool  `json:"enabled"`
	AutoStart     bool   `json:"auto_start"`
}

// ConflictError lists the ports that blocked creation. Ports are taken by
// other registered devices; Reserved are the service's own ports, which get
// a different message because the cause is different.
type ConflictError struct {
	Ports    []int
	Reserved []int
}

func (e *ConflictError) Error() string {
	var parts []string
	if len(e.Ports) > 0 {
		parts = append(parts, fmt.Sprintf("ports already used by other emulators: %v", e.Ports))
	}
	if len(e.Reserved) > 0 {
		parts = append(parts,
			fmt.Sprintf("ports reserved by the service itself: %v", e.Reserved))
	}
	if len(parts) == 0 {
		return "port conflict"
	}
	return strings.Join(parts, "; ")
}

// Is makes ConflictError match ErrInvalidSpec, so the handler maps every
// payload error to a single 400.
func (e *ConflictError) Is(target error) bool { return target == ErrInvalidSpec }

func validModel(model string) bool {
	return model == ModelHikvision || model == ModelDahua
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

// Normalize fills in defaults and validates. Call it before any use.
func (s *DeviceSpec) Normalize() error {
	s.Name = strings.TrimSpace(s.Name)
	s.IPAddress = strings.TrimSpace(s.IPAddress)

	if s.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidSpec)
	}
	if !validModel(s.Model) {
		return fmt.Errorf("%w: model must be %q or %q", ErrInvalidSpec, ModelHikvision, ModelDahua)
	}
	if !validPort(s.Port) {
		return fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidSpec)
	}
	if s.EventInterval < 0 {
		return fmt.Errorf("%w: event interval cannot be negative", ErrInvalidSpec)
	}

	if s.IPAddress == "" {
		s.IPAddress = DefaultIP
	}
	if s.EventInterval == 0 {
		s.EventInterval = DefaultInterval
	}
	if s.Enabled == nil {
		enabled := true
		s.Enabled = &enabled
	}
	return nil
}

// Normalize fills in the batch defaults and validates the range bounds.
func (s *RangeSpec) Normalize() error {
	s.NamePrefix = strings.TrimSpace(s.NamePrefix)
	s.IPAddress = strings.TrimSpace(s.IPAddress)

	if s.NamePrefix == "" {
		return fmt.Errorf("%w: name prefix is required", ErrInvalidSpec)
	}
	if !validModel(s.Model) {
		return fmt.Errorf("%w: model must be %q or %q", ErrInvalidSpec, ModelHikvision, ModelDahua)
	}
	if !validPort(s.PortStart) || !validPort(s.PortEnd) {
		return fmt.Errorf("%w: ports must be between 1 and 65535", ErrInvalidSpec)
	}
	if s.PortEnd < s.PortStart {
		return fmt.Errorf("%w: end port cannot be lower than start port", ErrInvalidSpec)
	}
	if size := s.PortEnd - s.PortStart + 1; size > MaxRangeSize {
		return fmt.Errorf("%w: batch of %d ports exceeds the maximum of %d",
			ErrInvalidSpec, size, MaxRangeSize)
	}
	if s.EventInterval < 0 {
		return fmt.Errorf("%w: event interval cannot be negative", ErrInvalidSpec)
	}

	if s.IPAddress == "" {
		s.IPAddress = DefaultIP
	}
	if s.EventInterval == 0 {
		s.EventInterval = DefaultInterval
	}
	if s.Enabled == nil {
		enabled := true
		s.Enabled = &enabled
	}
	return nil
}

// Expand turns the batch into one DeviceSpec per port. The name carries the
// port, not an index: the port is what the operator looks for when something
// fails. Call only after Normalize.
func (s RangeSpec) Expand() []DeviceSpec {
	specs := make([]DeviceSpec, 0, s.PortEnd-s.PortStart+1)
	for port := s.PortStart; port <= s.PortEnd; port++ {
		specs = append(specs, DeviceSpec{
			Name:          fmt.Sprintf("%s-%d", s.NamePrefix, port),
			Model:         s.Model,
			IPAddress:     s.IPAddress,
			Port:          port,
			EventInterval: s.EventInterval,
			Enabled:       s.Enabled,
			AutoStart:     s.AutoStart,
		})
	}
	return specs
}

// Conflicts returns the wanted ports that are already taken, sorted and
// deduplicated.
func Conflicts(wanted []int, taken map[int]bool) []int {
	seen := map[int]bool{}
	var conflicts []int
	for _, p := range wanted {
		if taken[p] && !seen[p] {
			seen[p] = true
			conflicts = append(conflicts, p)
		}
	}
	sort.Ints(conflicts)
	return conflicts
}

// internalDuplicates returns the ports that appear more than once in wanted,
// sorted and deduplicated: a batch asking for the same port twice, which
// Conflicts (which only compares against what is already stored) cannot see.
func internalDuplicates(wanted []int) []int {
	count := map[int]int{}
	for _, p := range wanted {
		count[p]++
	}

	seen := map[int]bool{}
	var dups []int
	for _, p := range wanted {
		if count[p] > 1 && !seen[p] {
			seen[p] = true
			dups = append(dups, p)
		}
	}
	sort.Ints(dups)
	return dups
}
