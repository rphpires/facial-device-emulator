package handlers

import (
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/emulator"
	"github.com/rphpires/facial-device-emulator/internal/models"
)

func TestPendingChangesLatestEventWins(t *testing.T) {
	p := newPendingChanges()
	if !p.empty() {
		t.Fatal("new pending changes are not empty")
	}

	p.record(emulator.FleetEvent{Kind: emulator.FleetChanged, DeviceID: 1})
	p.record(emulator.FleetEvent{Kind: emulator.FleetRemoved, DeviceID: 1})
	p.record(emulator.FleetEvent{Kind: emulator.FleetRemoved, DeviceID: 2})
	p.record(emulator.FleetEvent{Kind: emulator.FleetChanged, DeviceID: 2})

	if p.changed[1] || !p.removed[1] {
		t.Errorf("device 1 changed then removed should only be removed: %+v", p)
	}
	if !p.changed[2] || p.removed[2] {
		t.Errorf("device 2 removed then recreated should only be changed: %+v", p)
	}
	if p.resync {
		t.Error("resync set without a resync event")
	}

	p.record(emulator.FleetEvent{Kind: emulator.FleetResync})
	if !p.resync {
		t.Error("resync not set")
	}
}

func TestNewDeviceViewCopiesFieldsAndDefaultMode(t *testing.T) {
	v := newDeviceView(models.Device{ID: 9, Enabled: 0, Status: "running", IPAddress: "10.0.0.1"})

	if v.Status != "disabled" {
		t.Errorf("status = %q, want disabled", v.Status)
	}
	if v.IPAddress != "10.0.0.1" {
		t.Errorf("IP not copied: %+v", v)
	}
	if v.Mode != modeStandalone {
		t.Errorf("default mode = %q, want %q", v.Mode, modeStandalone)
	}
}
