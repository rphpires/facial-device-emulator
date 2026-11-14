package emulator

import (
	"errors"
	"testing"
)

// TestLastStartError ensures the port-bind error outlives the start call.
// Without it, "address already in use" dies in the log and the devices page
// cannot tell that the emulator is unreachable.
func TestLastStartError(t *testing.T) {
	m := &Manager{}

	if got := m.LastStartError(42); got != "" {
		t.Errorf("before any start: %q, want empty", got)
	}

	m.recordStartResult(42, errors.New("listen tcp 0.0.0.0:4200: address already in use"))
	if got := m.LastStartError(42); got == "" {
		t.Error("after a failed start: empty, want the error message")
	}

	m.recordStartResult(42, nil)
	if got := m.LastStartError(42); got != "" {
		t.Errorf("after a successful start: %q, want empty", got)
	}
}

// TestLastStartErrorIsolatesDevices: one device's error must not leak into
// another's verdict.
func TestLastStartErrorIsolatesDevices(t *testing.T) {
	m := &Manager{}
	m.recordStartResult(1, errors.New("boom"))
	m.recordStartResult(2, nil)

	if m.LastStartError(1) == "" {
		t.Error("device 1: want a recorded error")
	}
	if got := m.LastStartError(2); got != "" {
		t.Errorf("device 2: %q, want empty", got)
	}
}
