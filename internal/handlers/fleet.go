package handlers

import "github.com/rphpires/facial-device-emulator/internal/models"

// FleetCounts aggregates the fleet into the three states the devices table
// distinguishes, so the header meter and the table always agree.
type FleetCounts struct {
	Total    int `json:"total"`
	Running  int `json:"running"`
	Stopped  int `json:"stopped"`
	Disabled int `json:"disabled"`
}

// countFleet puts each device in exactly one state. Enabled == 0 wins over
// the stored Status: a disabled device is out of service rather than
// stopped, and the console must show the difference because the available
// actions differ.
func countFleet(devices []models.Device) FleetCounts {
	counts := FleetCounts{Total: len(devices)}

	for _, device := range devices {
		switch {
		case device.Enabled == 0:
			counts.Disabled++
		case device.Status == "running":
			counts.Running++
		default:
			counts.Stopped++
		}
	}

	return counts
}

// toMap returns the counts in the shape the templates read through
// {{ .counter_cards.running }}.
func (f FleetCounts) toMap() map[string]int {
	return map[string]int{
		"total":    f.Total,
		"running":  f.Running,
		"stopped":  f.Stopped,
		"disabled": f.Disabled,
	}
}
