package emulator

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/devicelog"
	"github.com/rphpires/facial-device-emulator/internal/trace"
)

func managerWithLogs(t *testing.T, db *fakeDB) (*Manager, devicelog.Options) {
	t.Helper()
	o := devicelog.Options{Dir: t.TempDir(), MaxSize: 1 << 20, MaxFiles: 8}
	return &Manager{
		ServiceDB:   db,
		EmulatorDB:  db,
		Tracer:      trace.NewTracer(),
		emulators:   map[int]Emulator{},
		watchdog:    map[int]*WatchdogInfo{},
		startErrors: map[int]string{},
		Logs:        devicelog.NewRegistry(o, nil),
	}, o
}

func TestUpdateDeviceSettingsEnablesWithHeaderFromDB(t *testing.T) {
	// UPDATE ... RETURNING name, model, port
	db := &fakeDB{row: fakeRow{values: []interface{}{"Gate B", "Dahua", 4005}}}
	m, o := managerWithLogs(t, db)

	if err := m.UpdateDeviceSettings(5, true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(o.FilePath(5, 0))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !strings.HasPrefix(string(b), `# device 5 "Gate B" Dahua port 4005`) {
		t.Errorf("header: %s", b)
	}

	if err := m.UpdateDeviceSettings(5, false); err != nil {
		t.Fatal(err)
	}
	if fs := o.Files(5); len(fs) != 0 {
		t.Errorf("disabling did not delete: %v", fs)
	}
}

// Same setup as TestDeleteDevicePurgesEveryTable (crud_test.go):
// requireExists reads db.row and must find the device.
func TestDeleteDeviceDeletesLogs(t *testing.T) {
	m, db := newTestManager(&fakeTx{})
	db.row = fakeRow{values: []interface{}{900001}}
	o := devicelog.Options{Dir: t.TempDir(), MaxSize: 1 << 20, MaxFiles: 8}
	m.Logs = devicelog.NewRegistry(o, nil)
	_ = m.Logs.SetEnabled(devicelog.Info{ID: 900001}, true)

	if err := m.DeleteDevice(context.Background(), 900001); err != nil {
		t.Fatal(err)
	}
	if fs := o.Files(900001); len(fs) != 0 {
		t.Errorf("DeleteDevice did not delete the logs: %v", fs)
	}
}
