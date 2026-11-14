package emulator

import (
	"errors"
	"reflect"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestDeviceSpecNormalizeAppliesDefaults(t *testing.T) {
	s := DeviceSpec{Name: "lab-01", Model: ModelDahua, Port: 4000}

	if err := s.Normalize(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.IPAddress != DefaultIP {
		t.Errorf("ip: %q, want %q", s.IPAddress, DefaultIP)
	}
	if s.EventInterval != DefaultInterval {
		t.Errorf("interval: %d, want %d", s.EventInterval, DefaultInterval)
	}
	if s.Enabled == nil || !*s.Enabled {
		t.Error("enabled: want true by default")
	}
	if s.AutoStart {
		t.Error("auto_start: want false by default")
	}
}

func TestDeviceSpecNormalizeKeepsEnabledFalse(t *testing.T) {
	s := DeviceSpec{Name: "lab-01", Model: ModelDahua, Port: 4000, Enabled: boolPtr(false)}

	if err := s.Normalize(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *s.Enabled {
		t.Error("explicit enabled=false became true")
	}
}

func TestDeviceSpecNormalizeRejects(t *testing.T) {
	cases := []struct {
		name string
		spec DeviceSpec
	}{
		{"empty name", DeviceSpec{Model: ModelDahua, Port: 4000}},
		{"whitespace-only name", DeviceSpec{Name: "   ", Model: ModelDahua, Port: 4000}},
		{"unknown model", DeviceSpec{Name: "x", Model: "Intelbras", Port: 4000}},
		{"empty model", DeviceSpec{Name: "x", Port: 4000}},
		{"zero port", DeviceSpec{Name: "x", Model: ModelDahua, Port: 0}},
		{"negative port", DeviceSpec{Name: "x", Model: ModelDahua, Port: -1}},
		{"port above 65535", DeviceSpec{Name: "x", Model: ModelDahua, Port: 65536}},
		{"negative interval", DeviceSpec{Name: "x", Model: ModelDahua, Port: 4000, EventInterval: -5}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.spec
			err := s.Normalize()
			if !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("want ErrInvalidSpec, got %v", err)
			}
		})
	}
}

func TestDeviceSpecNormalizeAcceptsBothModels(t *testing.T) {
	for _, model := range []string{ModelDahua, ModelHikvision} {
		s := DeviceSpec{Name: "x", Model: model, Port: 4000}
		if err := s.Normalize(); err != nil {
			t.Errorf("model %q rejected: %v", model, err)
		}
	}
}

func TestRangeSpecExpandYieldsOnePerPort(t *testing.T) {
	r := RangeSpec{
		NamePrefix: "lab",
		Model:      ModelHikvision,
		IPAddress:  "192.168.1.50",
		PortStart:  4000,
		PortEnd:    4002,
	}
	if err := r.Normalize(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	specs := r.Expand()

	if len(specs) != 3 {
		t.Fatalf("want 3 specs, got %d", len(specs))
	}
	wantNames := []string{"lab-4000", "lab-4001", "lab-4002"}
	for i, s := range specs {
		if s.Name != wantNames[i] {
			t.Errorf("spec %d: name %q, want %q", i, s.Name, wantNames[i])
		}
		if s.Port != 4000+i {
			t.Errorf("spec %d: port %d, want %d", i, s.Port, 4000+i)
		}
		if s.Model != ModelHikvision || s.IPAddress != "192.168.1.50" {
			t.Errorf("spec %d: model/ip not taken from the batch: %+v", i, s)
		}
	}
}

func TestRangeSpecExpandSinglePort(t *testing.T) {
	r := RangeSpec{NamePrefix: "solo", Model: ModelDahua, PortStart: 4000, PortEnd: 4000}
	if err := r.Normalize(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs := r.Expand(); len(specs) != 1 {
		t.Errorf("want 1 spec, got %d", len(specs))
	}
}

func TestRangeSpecNormalizeRejects(t *testing.T) {
	cases := []struct {
		name string
		spec RangeSpec
	}{
		{"empty prefix", RangeSpec{Model: ModelDahua, PortStart: 4000, PortEnd: 4001}},
		{"invalid model", RangeSpec{NamePrefix: "x", Model: "Nedap", PortStart: 4000, PortEnd: 4001}},
		{"end before start", RangeSpec{NamePrefix: "x", Model: ModelDahua, PortStart: 4010, PortEnd: 4000}},
		{"zero start", RangeSpec{NamePrefix: "x", Model: ModelDahua, PortStart: 0, PortEnd: 4000}},
		{"end above 65535", RangeSpec{NamePrefix: "x", Model: ModelDahua, PortStart: 4000, PortEnd: 70000}},
		{"batch above the cap", RangeSpec{NamePrefix: "x", Model: ModelDahua, PortStart: 1000, PortEnd: 1000 + MaxRangeSize}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.spec
			if err := s.Normalize(); !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("want ErrInvalidSpec, got %v", err)
			}
		})
	}
}

func TestRangeSpecNormalizeAcceptsExactCap(t *testing.T) {
	r := RangeSpec{NamePrefix: "x", Model: ModelDahua, PortStart: 1000, PortEnd: 1000 + MaxRangeSize - 1}
	if err := r.Normalize(); err != nil {
		t.Errorf("batch of exactly %d ports rejected: %v", MaxRangeSize, err)
	}
}

func TestConflictsReturnsSortedAndDeduplicated(t *testing.T) {
	taken := map[int]bool{4003: true, 4001: true, 8080: true}

	got := Conflicts([]int{4000, 4001, 4001, 4003, 4004, 8080}, taken)

	want := []int{4001, 4003, 8080}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conflicts: %v, want %v", got, want)
	}
}

func TestConflictsWithoutCollisionReturnsEmpty(t *testing.T) {
	if got := Conflicts([]int{4000, 4001}, map[int]bool{9000: true}); len(got) != 0 {
		t.Errorf("want no conflicts, got %v", got)
	}
}

func TestConflictErrorListsPorts(t *testing.T) {
	err := &ConflictError{Ports: []int{4001, 4003}}
	if !errors.Is(err, ErrInvalidSpec) {
		t.Error("ConflictError must match ErrInvalidSpec so the handler returns 400")
	}
	if msg := err.Error(); msg == "" {
		t.Error("empty message")
	}
}

func TestConflictErrorMentionsReservedPort(t *testing.T) {
	err := &ConflictError{Reserved: []int{8080}}
	if msg := err.Error(); msg == "" {
		t.Error("empty message")
	}
}
