package reachability

import (
	"encoding/json"
	"testing"
)

// TestParseRanges covers the format the compose file writes to
// PUBLISHED_PORT_RANGE. Malformed input must become an error, not a silently
// empty range: otherwise the application would flag every device as
// unreachable because of a typo in the compose file.
func TestParseRanges(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    []PortRange
		wantErr bool
	}{
		{"simple range", "4000-4499", []PortRange{{4000, 4499}}, false},
		{"single port", "7070", []PortRange{{7070, 7070}}, false},
		{"two entries", "7070,4000-4099", []PortRange{{7070, 7070}, {4000, 4099}}, false},
		{"surrounding spaces", " 4000-4099 , 7070 ", []PortRange{{4000, 4099}, {7070, 7070}}, false},
		{"empty", "", nil, true},
		{"reversed", "4499-4000", nil, true},
		{"not numeric", "abc", nil, true},
		{"outside port range", "0-70000", nil, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseRanges(c.input)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseRanges(%q) = %v, want error", c.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRanges(%q): %v", c.input, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("ParseRanges(%q) = %v, want %v", c.input, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("range %d = %v, want %v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestKindMarshalJSON ensures Kind serializes as a string, like Status.
// Otherwise /api/reachability would emit "kind":2, a number no consumer
// knows how to interpret, in the most specific field of the environment.
func TestKindMarshalJSON(t *testing.T) {
	b, err := json.Marshal(KindDocker)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(b) != `"docker"` {
		t.Errorf("KindDocker serialized = %s, want %q", b, `"docker"`)
	}
}

// TestAnalyze covers the environment x port matrix. The case that motivated
// the whole package is "Docker with a published range that does not include
// the configured port": the emulator starts and listens inside the container
// but is invisible from outside, with a green UI while the access control
// system sees every device offline.
func TestAnalyze(t *testing.T) {
	dockerPublished := Environment{
		Kind:            KindDocker,
		PublishedRanges: []PortRange{{4000, 4099}},
		RangesKnown:     true,
	}

	cases := []struct {
		name       string
		ports      []DevicePort
		env        Environment
		wantStatus []Status
		wantReason []string
	}{
		{
			name:       "docker: port inside the published range",
			ports:      []DevicePort{{DeviceID: 1, Port: 4001, Started: true}},
			env:        dockerPublished,
			wantStatus: []Status{StatusOK},
			wantReason: []string{""},
		},
		{
			name:       "docker: port outside the published range",
			ports:      []DevicePort{{DeviceID: 2, Port: 4200, Started: true}},
			env:        dockerPublished,
			wantStatus: []Status{StatusUnreachable},
			wantReason: []string{"port 4200 is not published in this Docker environment"},
		},
		{
			name:  "docker with unknown range: no made-up warning",
			ports: []DevicePort{{DeviceID: 3, Port: 4200, Started: true}},
			env: Environment{
				Kind:        KindDocker,
				RangesKnown: false,
			},
			wantStatus: []Status{StatusUnknown},
			wantReason: []string{"could not determine which ports this Docker environment publishes"},
		},
		{
			name:  "docker with host networking: treated as native",
			ports: []DevicePort{{DeviceID: 4, Port: 4200, Started: true}},
			env: Environment{
				Kind:        KindDocker,
				HostNetwork: true,
			},
			wantStatus: []Status{StatusOK},
			wantReason: []string{""},
		},
		{
			name:       "native linux: bind succeeded",
			ports:      []DevicePort{{DeviceID: 5, Port: 4200, Started: true}},
			env:        Environment{Kind: KindLinux},
			wantStatus: []Status{StatusOK},
			wantReason: []string{""},
		},
		{
			name:       "native linux: bind failed",
			ports:      []DevicePort{{DeviceID: 6, Port: 4200, Started: true, BindError: "address already in use"}},
			env:        Environment{Kind: KindLinux},
			wantStatus: []Status{StatusUnreachable},
			wantReason: []string{"port 4200 could not be opened: address already in use"},
		},
		{
			name:       "native linux: emulator never started",
			ports:      []DevicePort{{DeviceID: 7, Port: 4200, Started: false}},
			env:        Environment{Kind: KindLinux},
			wantStatus: []Status{StatusNotStarted},
			wantReason: []string{"the emulator has not been started yet"},
		},
		{
			name:       "wsl without mirrored mode: only the local machine can reach it",
			ports:      []DevicePort{{DeviceID: 8, Port: 4001, Started: true}},
			env:        Environment{Kind: KindWSL, WSLMirrored: false},
			wantStatus: []Status{StatusUnreachable},
			wantReason: []string{"WSL without mirrored networking: only this machine can reach port 4001"},
		},
		{
			name:       "wsl with mirrored mode: treated as native",
			ports:      []DevicePort{{DeviceID: 9, Port: 4001, Started: true}},
			env:        Environment{Kind: KindWSL, WSLMirrored: true},
			wantStatus: []Status{StatusOK},
			wantReason: []string{""},
		},
		{
			name:       "docker: bind failed inside the container, port published",
			ports:      []DevicePort{{DeviceID: 10, Port: 4001, Started: true, BindError: "address already in use"}},
			env:        dockerPublished,
			wantStatus: []Status{StatusUnreachable},
			wantReason: []string{"port 4001 could not be opened: address already in use"},
		},
		{
			name:       "wsl without mirroring: failed bind takes precedence over the NAT warning",
			ports:      []DevicePort{{DeviceID: 11, Port: 4001, Started: true, BindError: "permission denied"}},
			env:        Environment{Kind: KindWSL, WSLMirrored: false},
			wantStatus: []Status{StatusUnreachable},
			wantReason: []string{"port 4001 could not be opened: permission denied"},
		},
		{
			name:       "docker: stopped emulator with published port stays ok",
			ports:      []DevicePort{{DeviceID: 12, Port: 4001, Started: false}},
			env:        dockerPublished,
			wantStatus: []Status{StatusOK},
			wantReason: []string{""},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Analyze(c.ports, c.env)
			if len(r.Devices) != len(c.wantStatus) {
				t.Fatalf("len(Devices) = %d, want %d", len(r.Devices), len(c.wantStatus))
			}
			for i, d := range r.Devices {
				if d.Status != c.wantStatus[i] {
					t.Errorf("device %d: status = %v, want %v", d.DeviceID, d.Status, c.wantStatus[i])
				}
				if d.Reason != c.wantReason[i] {
					t.Errorf("device %d: reason = %q, want %q", d.DeviceID, d.Reason, c.wantReason[i])
				}
			}
		})
	}
}

// TestAnalyzeCounters ensures the report's top-level totals match the list;
// that is the number shown in the UI warning.
func TestAnalyzeCounters(t *testing.T) {
	env := Environment{
		Kind:            KindDocker,
		PublishedRanges: []PortRange{{4000, 4099}},
		RangesKnown:     true,
	}
	ports := []DevicePort{
		{DeviceID: 1, Port: 4001, Started: true},
		{DeviceID: 2, Port: 4200, Started: true},
		{DeviceID: 3, Port: 4300, Started: true},
	}

	r := Analyze(ports, env)
	if r.Unreachable != 2 {
		t.Errorf("Unreachable = %d, want 2", r.Unreachable)
	}
	if r.Unknown != 0 {
		t.Errorf("Unknown = %d, want 0", r.Unknown)
	}
}

// TestAnalyzeCountersNotStarted ensures a native device that was never
// started is counted in NotStarted, not Unknown. They mean different things:
// one is the normal state of a freshly installed package, the other is
// "something may be wrong and could not be verified".
func TestAnalyzeCountersNotStarted(t *testing.T) {
	env := Environment{Kind: KindLinux}
	ports := []DevicePort{
		{DeviceID: 1, Port: 4001, Started: false},
	}

	r := Analyze(ports, env)
	if r.NotStarted != 1 {
		t.Errorf("NotStarted = %d, want 1", r.NotStarted)
	}
	if r.Unknown != 0 {
		t.Errorf("Unknown = %d, want 0", r.Unknown)
	}
}

// TestAnalyzeNoDevices ensures an empty port list produces "devices":[] in
// JSON, not "devices":null. A nil slice would become null, which the devices
// screen does not expect; it is a trap for any future consumer that skips
// that check.
func TestAnalyzeNoDevices(t *testing.T) {
	env := Environment{Kind: KindLinux}

	r := Analyze(nil, env)
	if r.Devices == nil {
		t.Fatal("Devices is nil, want an empty (non-nil) slice")
	}
	if len(r.Devices) != 0 {
		t.Fatalf("len(Devices) = %d, want 0", len(r.Devices))
	}

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var body struct {
		Devices json.RawMessage `json:"devices"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if string(body.Devices) != "[]" {
		t.Errorf(`devices in JSON = %s, want "[]"`, body.Devices)
	}
}
