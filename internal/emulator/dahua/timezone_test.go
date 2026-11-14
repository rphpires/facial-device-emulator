package dahua

import (
	"testing"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/devicetime"
)

// TestEventTime_UsesDefaultZone verifies the event "Time" field is wall-clock
// time in the configured default zone, not the host's local zone.
func TestEventTime_UsesDefaultZone(t *testing.T) {
	prev := devicetime.Default()
	t.Cleanup(func() { devicetime.SetDefault(prev) })

	for _, loc := range []*time.Location{time.UTC, time.FixedZone("x", 9*3600), time.FixedZone("y", -3*3600)} {
		devicetime.SetDefault(loc)
		ev := buildEnrollEvent("00:11:22:33:44:55", "1", true)
		got, err := time.ParseInLocation("2006-01-02 15:04:05", ev.Time, loc)
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(got); d < -time.Second || d > 5*time.Second {
			t.Fatalf("zone %v: Time %q is off by %v", loc, ev.Time, d)
		}
		if ev.Events[0].Data.UTC != ev.Events[0].Data.CreateTime || ev.Events[0].Data.UTC != got.Unix() {
			t.Fatalf("zone %v: UTC epoch %d does not match Time %q", loc, ev.Events[0].Data.UTC, ev.Time)
		}
	}
}
