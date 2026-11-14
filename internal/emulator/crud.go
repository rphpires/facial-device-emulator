package emulator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/models"

	"github.com/jackc/pgx/v4"
)

// createLockKey serializes emulator creation. Port validation and the
// insert must be atomic with respect to each other: without the lock, two
// concurrent creates both pass validation and collide on insert.
//
// service.devices(port) is also UNIQUE; the lock exists to return a readable
// ConflictError (with the port list) instead of a raw constraint violation.
const createLockKey int64 = 8080_4000

// CreateDevice registers a new emulated device.
func (m *Manager) CreateDevice(ctx context.Context, spec DeviceSpec) (models.Device, error) {
	if err := spec.Normalize(); err != nil {
		return models.Device{}, err
	}

	devices, err := m.createMany(ctx, []DeviceSpec{spec})
	if err != nil {
		return models.Device{}, err
	}
	return devices[0], nil
}

// CreateDeviceRange registers one emulator per port in the range. Either all
// of them are created or none is.
func (m *Manager) CreateDeviceRange(ctx context.Context, spec RangeSpec) ([]models.Device, error) {
	if err := spec.Normalize(); err != nil {
		return nil, err
	}
	return m.createMany(ctx, spec.Expand())
}

// createMany is the single write path for both create verbs.
func (m *Manager) createMany(ctx context.Context, specs []DeviceSpec) ([]models.Device, error) {
	tx, err := m.ServiceDB.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", createLockKey); err != nil {
		return nil, fmt.Errorf("failed to acquire create lock: %w", err)
	}

	taken, err := takenPorts(ctx, tx)
	if err != nil {
		return nil, err
	}

	wanted := make([]int, 0, len(specs))
	for _, s := range specs {
		wanted = append(wanted, s.Port)
	}

	// The batch must be validated against itself, not only against what is
	// already stored: two specs asking for the same port would both pass
	// Conflicts(wanted, taken) (neither is "taken" yet) and only collide at
	// INSERT time with a UNIQUE(port) violation instead of a ConflictError.
	// RangeSpec.Expand (strictly increasing ports) and CreateDevice (a single
	// spec) cannot trigger this today, but any future createMany caller with
	// arbitrary specs needs the check.
	if dups := internalDuplicates(wanted); len(dups) > 0 {
		return nil, &ConflictError{Ports: dups}
	}

	conflict := &ConflictError{Ports: Conflicts(wanted, taken)}
	if m.ServicePort > 0 {
		conflict.Reserved = Conflicts(wanted, map[int]bool{m.ServicePort: true})
	}
	if len(conflict.Ports) > 0 || len(conflict.Reserved) > 0 {
		return nil, conflict
	}

	devices := make([]models.Device, 0, len(specs))
	for _, s := range specs {
		dev := models.Device{
			Name:          s.Name,
			IPAddress:     s.IPAddress,
			Port:          s.Port,
			Model:         s.Model,
			Enabled:       boolToInt(*s.Enabled),
			Type:          m.getDeviceType(s.Model),
			Status:        "stopped",
			EventInterval: s.EventInterval,
			TotalUsers:    0,
			LogEnabled:    0,
		}

		err := tx.QueryRow(ctx, `
			INSERT INTO service.devices (
				name, ip_address, port, model, enabled, type,
				status, event_interval, total_users, log_enabled
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING id`,
			dev.Name, dev.IPAddress, dev.Port, dev.Model, dev.Enabled,
			dev.Type, dev.Status, dev.EventInterval, dev.TotalUsers, dev.LogEnabled).Scan(&dev.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to insert device on port %d: %w", s.Port, err)
		}

		devices = append(devices, dev)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit create: %w", err)
	}

	// Only after commit: a watchdog entry for a device that does not exist
	// would health-check nothing.
	m.watchdogMutex.Lock()
	for _, dev := range devices {
		m.watchdog[dev.ID] = &WatchdogInfo{LastCheck: time.Now(), LastStatus: "stopped"}
	}
	m.watchdogMutex.Unlock()

	// A large batch becomes a single resync instead of a thousand events.
	if len(devices) > 20 {
		m.notifyResync()
	} else {
		for _, dev := range devices {
			m.NotifyChanged(dev.ID)
		}
	}

	return devices, nil
}

// takenPorts reads every port already registered.
func takenPorts(ctx context.Context, tx pgx.Tx) (map[int]bool, error) {
	rows, err := tx.Query(ctx, "SELECT port FROM service.devices")
	if err != nil {
		return nil, fmt.Errorf("failed to read registered ports: %w", err)
	}
	defer rows.Close()

	taken := map[int]bool{}
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			return nil, fmt.Errorf("failed to read registered port: %w", err)
		}
		taken[port] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate registered ports: %w", err)
	}
	return taken, nil
}

// UpdateDevice replaces the editable fields of a stopped device.
func (m *Manager) UpdateDevice(ctx context.Context, id int, spec DeviceSpec) (models.Device, error) {
	if err := spec.Normalize(); err != nil {
		return models.Device{}, err
	}

	if err := m.requireStopped(ctx, id); err != nil {
		return models.Device{}, err
	}

	tx, err := m.ServiceDB.Begin(ctx)
	if err != nil {
		return models.Device{}, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", createLockKey); err != nil {
		return models.Device{}, fmt.Errorf("failed to acquire lock: %w", err)
	}

	taken, err := takenPorts(ctx, tx)
	if err != nil {
		return models.Device{}, err
	}
	// The device's own current port does not count as a conflict: keeping
	// the port is the most common edit. The read goes through the same
	// transaction: taking a second pool connection while holding the
	// advisory lock is pointless contention, and the value must come from
	// the same snapshot as takenPorts.
	var currentPort, currentTotalUsers, currentLog int
	err = tx.QueryRow(ctx, `
		SELECT port, total_users, log_enabled
		  FROM service.devices
		 WHERE id = $1`, id).Scan(&currentPort, &currentTotalUsers, &currentLog)
	if err != nil {
		return models.Device{}, fmt.Errorf("failed to read device %d: %w", id, err)
	}
	delete(taken, currentPort)

	conflict := &ConflictError{Ports: Conflicts([]int{spec.Port}, taken)}
	if m.ServicePort > 0 {
		conflict.Reserved = Conflicts([]int{spec.Port}, map[int]bool{m.ServicePort: true})
	}
	if len(conflict.Ports) > 0 || len(conflict.Reserved) > 0 {
		return models.Device{}, conflict
	}

	_, err = tx.Exec(ctx, `
		UPDATE service.devices
		   SET name = $2, ip_address = $3, port = $4, model = $5,
		       enabled = $6, type = $7, event_interval = $8, updated_at = NOW()
		 WHERE id = $1`,
		id, spec.Name, spec.IPAddress, spec.Port, spec.Model,
		boolToInt(*spec.Enabled), m.getDeviceType(spec.Model), spec.EventInterval)
	if err != nil {
		return models.Device{}, fmt.Errorf("failed to update device %d: %w", id, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return models.Device{}, fmt.Errorf("failed to commit update: %w", err)
	}

	m.NotifyChanged(id)

	return models.Device{
		ID: id, Name: spec.Name, IPAddress: spec.IPAddress, Port: spec.Port,
		Model: spec.Model, Enabled: boolToInt(*spec.Enabled),
		Type: m.getDeviceType(spec.Model), Status: "stopped",
		EventInterval: spec.EventInterval,
		TotalUsers:    currentTotalUsers,
		LogEnabled:    currentLog,
	}, nil
}

// dataTables lists every table with a loose device_id. The schema has no
// FKs, so cleanup is explicit.
//
// Assumption: DeleteDevice opens the transaction on m.ServiceDB and deletes
// straight from these emulator.* tables, even though ServiceDB should only
// know the service schema. This only works because service_db and
// emulator_db currently point at the same Postgres database (one instance,
// two schemas). If they ever point at different databases, this transaction
// will fail (or, worse, silently delete nothing if an emulator schema happens
// to exist in both) because m.ServiceDB would not see m.EmulatorDB's tables.
// In that case DeleteDevice would need two coordinated transactions, one per
// connection.
var dataTables = []string{
	"emulator.dahua_cards",
	"emulator.dahua_faces",
	"emulator.hikvision_users",
	"emulator.hikvision_cards",
	"emulator.hikvision_faces",
	"emulator.hikvision_fingers",
	"emulator.device_settings",
}

// DeleteDevice removes a device and everything it accumulated. It stops the
// emulator first instead of requiring the operator to do it: deleting is an
// unambiguous intent, editing is not.
func (m *Manager) DeleteDevice(ctx context.Context, id int) error {
	if err := m.requireExists(ctx, id); err != nil {
		return err
	}

	m.emulatorMutex.RLock()
	inst, running := m.emulators[id]
	m.emulatorMutex.RUnlock()
	if running && inst.IsRunning() {
		if err := m.Stop(id); err != nil {
			return fmt.Errorf("failed to stop emulator %d before removal: %w", id, err)
		}
	}

	tx, err := m.ServiceDB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, table := range dataTables {
		// Filter by the specific device_id: the device_id = 0 row in
		// device_settings holds the global defaults and belongs to no
		// device.
		if _, err := tx.Exec(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE device_id = $1", table), id); err != nil {
			return fmt.Errorf("failed to clear %s for device %d: %w", table, id, err)
		}
	}

	if _, err := tx.Exec(ctx,
		"DELETE FROM service.devices WHERE id = $1", id); err != nil {
		return fmt.Errorf("failed to delete device %d: %w", id, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit delete: %w", err)
	}

	if err := m.Logs.Remove(id); err != nil {
		m.Tracer.Warning("deleting logs for device %d: %v", id, err)
	}

	m.watchdogMutex.Lock()
	delete(m.watchdog, id)
	m.watchdogMutex.Unlock()

	m.emulatorMutex.Lock()
	delete(m.emulators, id)
	m.emulatorMutex.Unlock()

	m.Tracer.Info("Removed device %d", id)
	m.notifyRemoved(id)
	return nil
}

// requireExists returns ErrDeviceNotFound when the device does not exist.
func (m *Manager) requireExists(ctx context.Context, id int) error {
	var found int
	err := m.ServiceDB.QueryRow(ctx,
		"SELECT id FROM service.devices WHERE id = $1", id).Scan(&found)

	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %d", ErrDeviceNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("failed to read device %d: %w", id, err)
	}
	return nil
}

// requireStopped is the edit guard: the device must exist and be stopped.
func (m *Manager) requireStopped(ctx context.Context, id int) error {
	if err := m.requireExists(ctx, id); err != nil {
		return err
	}

	m.emulatorMutex.RLock()
	inst, exists := m.emulators[id]
	m.emulatorMutex.RUnlock()
	if exists && inst.IsRunning() {
		return fmt.Errorf("%w: stop emulator %d before editing", ErrDeviceRunning, id)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
