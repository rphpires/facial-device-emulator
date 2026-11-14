package emulator

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v4"

	"github.com/rphpires/facial-device-emulator/internal/models"
)

var errNoRows = pgx.ErrNoRows

// lifeEmulator is a thread-safe fake whose Start/Stop outcome is scripted.
type lifeEmulator struct {
	running  atomic.Bool
	starts   atomic.Int32
	stopErr  error
	startErr error
}

func (e *lifeEmulator) Start() error {
	e.starts.Add(1)
	if e.startErr != nil {
		return e.startErr
	}
	e.running.Store(true)
	return nil
}
func (e *lifeEmulator) Stop() error {
	if e.stopErr != nil {
		return e.stopErr
	}
	e.running.Store(false)
	return nil
}
func (e *lifeEmulator) IsRunning() bool             { return e.running.Load() }
func (e *lifeEmulator) GetInfo() models.Device      { return models.Device{} }
func (e *lifeEmulator) GenerateEvent() error        { return nil }
func (e *lifeEmulator) GetType() string             { return ModelHikvision }
func (e *lifeEmulator) GetTotalUsers() (int, error) { return 0, nil }

// storedDevice is the row readStoredDevice scans (deviceColumns order).
func storedDevice(id int, status string, enabled int) fakeRow {
	return fakeRow{values: []interface{}{
		id, "dev", "127.0.0.1", 4000 + id, ModelHikvision, enabled, 2, status, 0, 0, 0,
	}}
}

// TestWatchdogRestartsDeadEmulator: a device stored as running whose server
// died is restarted after watchdogThreshold checks, and not before.
func TestWatchdogRestartsDeadEmulator(t *testing.T) {
	m, db := newTestManager(&fakeTx{})
	db.row = storedDevice(7, "running", 1)

	dead := &lifeEmulator{} // in the map, not serving: its server exited
	m.emulators[7] = dead

	fresh := &lifeEmulator{}
	m.newEmulator = func(models.Device) (Emulator, error) { return fresh, nil }

	device := models.Device{ID: 7, Status: "running", Enabled: 1}
	for i := 1; i < watchdogThreshold; i++ {
		m.checkDeviceHealth(device)
		if fresh.starts.Load() != 0 {
			t.Fatalf("restarted after %d checks, want %d", i, watchdogThreshold)
		}
	}
	m.checkDeviceHealth(device)

	if fresh.starts.Load() != 1 {
		t.Fatalf("starts = %d after %d checks, want 1", fresh.starts.Load(), watchdogThreshold)
	}
	if m.emulators[7] != Emulator(fresh) || !fresh.IsRunning() {
		t.Error("the restarted emulator is not the one tracked")
	}
	if got := m.watchdog[7].FailureCount; got != 0 {
		t.Errorf("FailureCount = %d after the restart, want 0", got)
	}
}

// TestWatchdogLeavesStoppedDevicesAlone: stored as stopped and not serving
// is the consistent state; no restart.
func TestWatchdogLeavesStoppedDevicesAlone(t *testing.T) {
	m, _ := newTestManager(&fakeTx{})
	m.newEmulator = func(models.Device) (Emulator, error) {
		t.Fatal("watchdog started a stopped device")
		return nil, nil
	}
	for i := 0; i < watchdogThreshold+1; i++ {
		m.checkDeviceHealth(models.Device{ID: 3, Status: "stopped", Enabled: 1})
	}
}

// TestWatchdogStopsDisabledRunningDevice: a device serving while disabled
// is stopped after the threshold.
func TestWatchdogStopsDisabledRunningDevice(t *testing.T) {
	m, _ := newTestManager(&fakeTx{})
	em := &lifeEmulator{}
	em.running.Store(true)
	m.emulators[4] = em

	for i := 0; i < watchdogThreshold; i++ {
		m.checkDeviceHealth(models.Device{ID: 4, Status: "running", Enabled: 0})
	}
	if em.IsRunning() {
		t.Error("disabled device still running after the watchdog threshold")
	}
}

// TestStopRestoresEmulatorOnFailure: a failed Stop keeps the emulator
// tracked, so it can still be stopped later.
func TestStopRestoresEmulatorOnFailure(t *testing.T) {
	m, _ := newTestManager(&fakeTx{})
	em := &lifeEmulator{stopErr: errors.New("shutdown timed out")}
	em.running.Store(true)
	m.emulators[5] = em

	if err := m.Stop(5); err == nil {
		t.Fatal("Stop succeeded, want the emulator's error")
	}
	if m.emulators[5] != Emulator(em) {
		t.Error("emulator dropped from the map after a failed stop")
	}
}

// TestStopDoesNotHoldLockDuringEmulatorStop: listing must not wait for a
// slow emulator Stop.
func TestStopDoesNotHoldLockDuringEmulatorStop(t *testing.T) {
	m, _ := newTestManager(&fakeTx{})
	release := make(chan struct{})
	slow := &blockingEmulator{release: release, entered: make(chan struct{})}
	slow.running.Store(true)
	m.emulators[6] = slow

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = m.Stop(6)
	}()
	<-slow.entered

	locked := make(chan struct{})
	go func() {
		m.emulatorMutex.Lock()
		close(locked)
		m.emulatorMutex.Unlock()
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Error("emulatorMutex held while the emulator stops")
	}
	close(release)
	wg.Wait()
}

type blockingEmulator struct {
	lifeEmulator
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (e *blockingEmulator) Stop() error {
	e.once.Do(func() { close(e.entered) })
	<-e.release
	e.running.Store(false)
	return nil
}

// TestStartReportsNotFoundAndDisabled: the sentinels the API maps to 404 and
// 409.
func TestStartReportsNotFoundAndDisabled(t *testing.T) {
	m, db := newTestManager(&fakeTx{})
	db.row = storedDevice(8, "stopped", 0)
	if err := m.Start(8); !errors.Is(err, ErrDeviceDisabled) {
		t.Errorf("disabled: err = %v, want ErrDeviceDisabled", err)
	}

	db.row = fakeRow{err: errNoRows}
	if err := m.Start(9); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("missing: err = %v, want ErrDeviceNotFound", err)
	}
	if err := m.Stop(9); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("stop missing: err = %v, want ErrDeviceNotFound", err)
	}

	db.row = storedDevice(8, "stopped", 1)
	if err := m.Stop(8); !errors.Is(err, ErrNotRunning) {
		t.Errorf("stop stopped: err = %v, want ErrNotRunning", err)
	}
}
