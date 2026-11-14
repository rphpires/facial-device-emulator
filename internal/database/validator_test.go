package database

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyStructure(t *testing.T) {
	cases := []struct {
		name     string
		found    int
		problems []string
		want     structureState
	}{
		{"empty", 0, []string{"table service.devices is missing"}, structureEmpty},
		{"complete", 8, nil, structureComplete},
		{"some tables missing", 3, []string{"table emulator.dahua_cards is missing"}, structurePartial},
		{"column missing", 8, []string{"column service.devices.port is missing"}, structurePartial},
	}
	for _, c := range cases {
		if got := classify(c.found, 8, c.problems); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A mismatch must name what is missing and how to opt into the reset,
// and be recognizable by callers.
func TestMismatchError(t *testing.T) {
	err := mismatchError([]string{"table service.devices is missing"})
	if !errors.Is(err, ErrStructureMismatch) {
		t.Error("not ErrStructureMismatch")
	}
	for _, want := range []string{"service.devices", "-reset-db", "reset_on_mismatch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
