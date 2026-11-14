package emulator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/models"
	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/jackc/pgx/v4"
)

// newTestManager returns a Manager backed by fakes, with the service port
// at 8080 (the config.yaml default).
func newTestManager(tx *fakeTx) (*Manager, *fakeDB) {
	db := &fakeDB{tx: tx}
	return &Manager{
		ServiceDB:   db,
		EmulatorDB:  db,
		Tracer:      trace.NewTracer(),
		ServicePort: 8080,
		emulators:   map[int]Emulator{},
		watchdog:    map[int]*WatchdogInfo{},
		startErrors: map[int]string{},
	}, db
}

// sequentialIDs simulates INSERT ... RETURNING id yielding 1, 2, ...
func sequentialIDs(start int) func(sql string) pgx.Row {
	next := start
	return func(sql string) pgx.Row {
		if strings.Contains(sql, "RETURNING id") {
			id := next
			next++
			return fakeRow{values: []interface{}{id}}
		}
		return fakeRow{}
	}
}

func TestCreateDeviceInsertsStoppedDevice(t *testing.T) {
	tx := &fakeTx{queryRowFn: sequentialIDs(1)}
	m, _ := newTestManager(tx)

	dev, err := m.CreateDevice(context.Background(), DeviceSpec{
		Name: "lab-01", Model: ModelDahua, Port: 4000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if dev.ID != 1 {
		t.Errorf("id %d, want 1", dev.ID)
	}
	if dev.Status != "stopped" {
		t.Errorf("status %q, want stopped", dev.Status)
	}
	if dev.IPAddress != DefaultIP {
		t.Errorf("ip %q, want the default %q", dev.IPAddress, DefaultIP)
	}
	if !tx.committed {
		t.Error("want commit")
	}

	joined := strings.Join(tx.execs, "\n")
	if !strings.Contains(joined, "pg_advisory_xact_lock") {
		t.Error("want the lock that serializes concurrent creates")
	}
	if !strings.Contains(joined, "INSERT INTO service.devices") {
		t.Error("want the device INSERT")
	}
}

func TestCreateDeviceRejectsInvalidSpec(t *testing.T) {
	tx := &fakeTx{queryRowFn: sequentialIDs(1)}
	m, _ := newTestManager(tx)

	_, err := m.CreateDevice(context.Background(), DeviceSpec{Name: "x", Model: "Nedap", Port: 4000})

	if !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("want ErrInvalidSpec, got %v", err)
	}
	if len(tx.execs) != 0 {
		t.Errorf("nothing should have run, got %v", tx.execs)
	}
}

func TestCreateDeviceRejectsTakenPort(t *testing.T) {
	tx := &fakeTx{
		queryRowFn: sequentialIDs(1),
		queryRows:  &fakeRows{rows: [][]interface{}{{4000}, {7070}}},
	}
	m, _ := newTestManager(tx)

	_, err := m.CreateDevice(context.Background(), DeviceSpec{
		Name: "lab-01", Model: ModelDahua, Port: 4000,
	})

	var conf *ConflictError
	if !errors.As(err, &conf) {
		t.Fatalf("want ConflictError, got %v", err)
	}
	if len(conf.Ports) != 1 || conf.Ports[0] != 4000 {
		t.Errorf("conflicts: %v, want [4000]", conf.Ports)
	}
	if tx.committed {
		t.Error("transaction must not be committed")
	}
}

func TestCreateDeviceRejectsServicePort(t *testing.T) {
	tx := &fakeTx{queryRowFn: sequentialIDs(1)}
	m, _ := newTestManager(tx) // ServicePort = 8080

	_, err := m.CreateDevice(context.Background(), DeviceSpec{
		Name: "collides", Model: ModelDahua, Port: 8080,
	})

	var conf *ConflictError
	if !errors.As(err, &conf) {
		t.Fatalf("want ConflictError, got %v", err)
	}
	if len(conf.Reserved) != 1 || conf.Reserved[0] != 8080 {
		t.Errorf("reserved: %v, want [8080]", conf.Reserved)
	}
}

func TestCreateDeviceRangeCreatesEveryPort(t *testing.T) {
	tx := &fakeTx{queryRowFn: sequentialIDs(1)}
	m, _ := newTestManager(tx)

	devs, err := m.CreateDeviceRange(context.Background(), RangeSpec{
		NamePrefix: "lab", Model: ModelHikvision, PortStart: 4000, PortEnd: 4002,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(devs) != 3 {
		t.Fatalf("want 3 devices, got %d", len(devs))
	}
	if devs[0].Name != "lab-4000" || devs[2].Name != "lab-4002" {
		t.Errorf("generated names: %q ... %q", devs[0].Name, devs[2].Name)
	}
	if devs[0].ID == devs[1].ID {
		t.Error("duplicate IDs in batch")
	}
	if !tx.committed {
		t.Error("want commit")
	}
}

// A collision on any port in the batch writes nothing: the all-or-nothing
// behavior the spec requires.
func TestCreateDeviceRangeWithCollisionWritesNothing(t *testing.T) {
	tx := &fakeTx{
		queryRowFn: sequentialIDs(1),
		queryRows:  &fakeRows{rows: [][]interface{}{{4001}}},
	}
	m, _ := newTestManager(tx)

	_, err := m.CreateDeviceRange(context.Background(), RangeSpec{
		NamePrefix: "lab", Model: ModelDahua, PortStart: 4000, PortEnd: 4002,
	})

	var conf *ConflictError
	if !errors.As(err, &conf) {
		t.Fatalf("want ConflictError, got %v", err)
	}
	for _, sql := range tx.execs {
		if strings.Contains(sql, "INSERT INTO service.devices") {
			t.Error("no INSERT should have happened")
		}
	}
	if tx.committed {
		t.Error("transaction must not be committed")
	}
}

// A batch asking for the same port twice must be rejected even when no one
// holds that port: Conflicts only compares against what is already stored,
// so two specs asking for 4000 would both pass that check alone.
func TestCreateManyRejectsDuplicatePortWithinBatch(t *testing.T) {
	enabled := true
	tx := &fakeTx{queryRowFn: sequentialIDs(1)}
	m, _ := newTestManager(tx)

	specs := []DeviceSpec{
		{Name: "lab-4000-a", Model: ModelDahua, Port: 4000, IPAddress: DefaultIP, EventInterval: 10, Enabled: &enabled},
		{Name: "lab-4000-b", Model: ModelDahua, Port: 4000, IPAddress: DefaultIP, EventInterval: 10, Enabled: &enabled},
	}

	_, err := m.createMany(context.Background(), specs)

	var conf *ConflictError
	if !errors.As(err, &conf) {
		t.Fatalf("want ConflictError, got %v", err)
	}
	if len(conf.Ports) != 1 || conf.Ports[0] != 4000 {
		t.Errorf("conflicts: %v, want [4000]", conf.Ports)
	}
	for _, sql := range tx.execs {
		if strings.Contains(sql, "INSERT INTO service.devices") {
			t.Error("no INSERT should have happened")
		}
	}
	if tx.committed {
		t.Error("transaction must not be committed")
	}
}

func TestDeleteDevicePurgesEveryTable(t *testing.T) {
	tx := &fakeTx{}
	m, db := newTestManager(tx)
	db.row = fakeRow{values: []interface{}{900001}}

	if err := m.DeleteDevice(context.Background(), 900001); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	joined := strings.Join(tx.execs, "\n")
	tables := []string{
		"emulator.dahua_cards", "emulator.dahua_faces",
		"emulator.hikvision_users", "emulator.hikvision_cards",
		"emulator.hikvision_faces", "emulator.hikvision_fingers",
		"emulator.device_settings",
		"service.devices",
	}
	for _, tab := range tables {
		if !strings.Contains(joined, tab) {
			t.Errorf("%s was not cleared", tab)
		}
	}
	if !tx.committed {
		t.Error("want commit")
	}
}

// The device_id = 0 row in emulator.device_settings holds the global
// defaults seeded by V001. Purging by a specific device_id must never reach
// it.
func TestDeleteDeviceKeepsGlobalDefaults(t *testing.T) {
	tx := &fakeTx{}
	m, db := newTestManager(tx)
	db.row = fakeRow{values: []interface{}{900001}}

	if err := m.DeleteDevice(context.Background(), 900001); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, sql := range tx.execs {
		if strings.Contains(sql, "device_settings") {
			args := tx.execArgs[i]
			if len(args) == 0 || args[0].(int) != 900001 {
				t.Errorf("device_settings purge with args %v", args)
			}
		}
	}
}

func TestDeleteDeviceUnknownIDIsNotFound(t *testing.T) {
	tx := &fakeTx{}
	m, db := newTestManager(tx)
	db.row = fakeRow{err: pgx.ErrNoRows}

	err := m.DeleteDevice(context.Background(), 17)

	if !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("want ErrDeviceNotFound, got %v", err)
	}
}

func TestUpdateDeviceUnknownIDIsNotFound(t *testing.T) {
	tx := &fakeTx{}
	m, db := newTestManager(tx)
	db.row = fakeRow{err: pgx.ErrNoRows}

	_, err := m.UpdateDevice(context.Background(), 17, DeviceSpec{
		Name: "new", Model: ModelDahua, Port: 4000,
	})

	if !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("want ErrDeviceNotFound, got %v", err)
	}
}

// fakeEmulator implements Emulator to simulate a running instance.
type fakeEmulator struct{ running bool }

func (e *fakeEmulator) Start() error                { return nil }
func (e *fakeEmulator) Stop() error                 { e.running = false; return nil }
func (e *fakeEmulator) IsRunning() bool             { return e.running }
func (e *fakeEmulator) GetInfo() models.Device      { return models.Device{} }
func (e *fakeEmulator) GenerateEvent() error        { return nil }
func (e *fakeEmulator) GetType() string             { return ModelDahua }
func (e *fakeEmulator) GetTotalUsers() (int, error) { return 0, nil }

func TestUpdateDeviceRejectsRunningEmulator(t *testing.T) {
	tx := &fakeTx{}
	m, db := newTestManager(tx)
	db.row = fakeRow{values: []interface{}{900001}}
	m.emulators[900001] = &fakeEmulator{running: true}

	_, err := m.UpdateDevice(context.Background(), 900001, DeviceSpec{
		Name: "new", Model: ModelDahua, Port: 4000,
	})

	if !errors.Is(err, ErrDeviceRunning) {
		t.Errorf("want ErrDeviceRunning, got %v", err)
	}
}

// deviceRowReader simulates, inside the transaction, the query that reads
// the device's own port, total_users and log_enabled. It is separate from
// requireExists (which runs outside the transaction, via fakeDB.row) and from
// sequentialIDs (INSERT ... RETURNING, used only on create).
func deviceRowReader(port, totalUsers, logEnabled int) func(sql string) pgx.Row {
	return func(sql string) pgx.Row {
		if strings.Contains(sql, "SELECT port") {
			return fakeRow{values: []interface{}{port, totalUsers, logEnabled}}
		}
		return fakeRow{}
	}
}

// Regression: the device's current port must be excluded from the taken set
// before checking conflicts, or every edit that keeps the port would collide
// with itself.
func TestUpdateDeviceKeepsCurrentPort(t *testing.T) {
	tx := &fakeTx{
		queryRowFn: deviceRowReader(4000, 0, 0),
		queryRows:  &fakeRows{rows: [][]interface{}{{4000}, {7070}}},
	}
	m, db := newTestManager(tx)
	db.row = fakeRow{values: []interface{}{900001}}

	dev, err := m.UpdateDevice(context.Background(), 900001, DeviceSpec{
		Name: "lab-01", Model: ModelDahua, Port: 4000,
	})
	if err != nil {
		t.Fatalf("keeping its own port must not conflict: %v", err)
	}
	if dev.Port != 4000 {
		t.Errorf("port %d, want 4000", dev.Port)
	}

	joined := strings.Join(tx.execs, "\n")
	if !strings.Contains(joined, "UPDATE service.devices") {
		t.Error("want the device UPDATE")
	}
	if !tx.committed {
		t.Error("want commit")
	}
}

// The HTTP response is rendered straight from the models.Device returned by
// UpdateDevice: total_users and log_enabled must not reset on edit, since
// the UPDATE deliberately leaves those two columns alone.
func TestUpdateDevicePreservesTotalUsersAndLogEnabled(t *testing.T) {
	tx := &fakeTx{
		queryRowFn: deviceRowReader(4000, 300, 1),
		queryRows:  &fakeRows{rows: [][]interface{}{{4000}, {7070}}},
	}
	m, db := newTestManager(tx)
	db.row = fakeRow{values: []interface{}{900001}}

	dev, err := m.UpdateDevice(context.Background(), 900001, DeviceSpec{
		Name: "lab-01", Model: ModelDahua, Port: 4000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if dev.TotalUsers != 300 {
		t.Errorf("total_users %d, want 300", dev.TotalUsers)
	}
	if dev.LogEnabled != 1 {
		t.Errorf("log_enabled %d, want 1", dev.LogEnabled)
	}
}
