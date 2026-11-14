package handlers

import (
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/models"
)

// TestCountFleet covers the rule that keeps the header and the table in
// agreement: a device with Enabled == 0 is "disabled" whatever its stored
// Status says, and is never counted as stopped. The table applies the same
// rule (stream.go, newDeviceView).
func TestCountFleet(t *testing.T) {
	cases := []struct {
		name    string
		devices []models.Device
		want    FleetCounts
	}{
		{
			name:    "empty fleet",
			devices: nil,
			want:    FleetCounts{Total: 0, Running: 0, Stopped: 0, Disabled: 0},
		},
		{
			name: "disabled does not count as stopped",
			devices: []models.Device{
				{ID: 1, Enabled: 1, Status: "running"},
				{ID: 2, Enabled: 1, Status: "stopped"},
				{ID: 3, Enabled: 0, Status: "stopped"},
			},
			want: FleetCounts{Total: 3, Running: 1, Stopped: 1, Disabled: 1},
		},
		{
			name: "disabled with running status is still disabled",
			devices: []models.Device{
				{ID: 1, Enabled: 0, Status: "running"},
			},
			want: FleetCounts{Total: 1, Running: 0, Stopped: 0, Disabled: 1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countFleet(tc.devices); got != tc.want {
				t.Errorf("countFleet() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
