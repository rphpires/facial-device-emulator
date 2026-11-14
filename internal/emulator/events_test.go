package emulator

import "testing"

func TestFleetHubDeliversInOrder(t *testing.T) {
	m := &Manager{}
	l := m.AddFleetListener()
	defer m.RemoveFleetListener(l)

	m.NotifyChanged(1)
	m.notifyRemoved(2)
	m.notifyResync()

	want := []FleetEvent{
		{Kind: FleetChanged, DeviceID: 1},
		{Kind: FleetRemoved, DeviceID: 2},
		{Kind: FleetResync},
	}
	for i, q := range want {
		if got := <-l.C; got != q {
			t.Errorf("event %d = %+v, want %+v", i, got, q)
		}
	}
	if l.TakeOverflow() {
		t.Error("overflow flagged without overflowing")
	}
}

// A full channel must not block the notifier, and the drop must be
// recorded: that is what makes the stream send a snapshot instead of leaving
// the UI stale.
func TestFleetHubFlagsOverflow(t *testing.T) {
	m := &Manager{}
	l := m.AddFleetListener()
	defer m.RemoveFleetListener(l)

	for i := 0; i < fleetListenerBuffer+5; i++ {
		m.NotifyChanged(i)
	}

	if !l.TakeOverflow() {
		t.Fatal("overflow not flagged")
	}
	if l.TakeOverflow() {
		t.Error("TakeOverflow did not clear the flag")
	}
}

func TestFleetHubRemoveClosesChannel(t *testing.T) {
	m := &Manager{}
	l := m.AddFleetListener()
	m.RemoveFleetListener(l)

	if _, open := <-l.C; open {
		t.Error("channel still open after RemoveFleetListener")
	}
	m.NotifyChanged(1) // must not panic with a removed listener
}
