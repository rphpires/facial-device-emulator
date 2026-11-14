package hikvision

import (
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/trace"
)

// newTestEmulator returns a minimal *Emulator suitable for handler unit
// tests. Tests that need repo access must set their own fields; this helper
// only guarantees a valid tracer so methods that log do not nil-panic.
func newTestEmulator(t *testing.T) *Emulator {
	t.Helper()
	return &Emulator{
		tracer:   trace.NewTracer(),
		stopChan: make(chan struct{}),
	}
}

func TestNewTestEmulator_Smoke(t *testing.T) {
	e := newTestEmulator(t)
	if e.tracer == nil {
		t.Fatal("expected non-nil tracer")
	}
	e.tracer.Info("smoke-test log line from newTestEmulator")
}
