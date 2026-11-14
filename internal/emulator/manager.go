package emulator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v4"

	"github.com/rphpires/facial-device-emulator/internal/database"
	"github.com/rphpires/facial-device-emulator/internal/devicelog"
	"github.com/rphpires/facial-device-emulator/internal/emulator/dahua"
	"github.com/rphpires/facial-device-emulator/internal/emulator/hikvision"
	"github.com/rphpires/facial-device-emulator/internal/models"
	"github.com/rphpires/facial-device-emulator/internal/trace"
)

// Manager owns every emulator instance: lifecycle, watchdog and status.
type Manager struct {
	ServiceDB  database.DBInterface // AdaptivePool or DualPoolManager
	EmulatorDB database.DBInterface // AdaptivePool or DualPoolManager
	Tracer     *trace.Tracer

	// ServicePort is the service's own HTTP port. Registering an emulator on
	// it would produce a device that can never start, and the error would
	// only surface at start time, far from the cause.
	ServicePort int

	// Logs holds each device's log ("Record event log" checkbox). Exported
	// so the handlers can serve the files.
	Logs *devicelog.Registry

	// Active emulators by device ID
	emulators     map[int]Emulator
	emulatorMutex sync.RWMutex // guards emulators

	// Watchdog state, with its own mutex to avoid race conditions
	watchdog      map[int]*WatchdogInfo
	watchdogMutex sync.RWMutex // guards watchdog

	// Control channels
	shutdownChan   chan struct{}
	watchdogTicker *time.Ticker

	hub fleetHub

	// starting reserves the IDs whose start is in progress. Starting an
	// emulator can take up to 10 s; holding emulatorMutex that long would
	// serialize StartAll and block ListDevices (and the UI with it) while
	// the fleet comes up.
	starting map[int]bool

	// Watchdog pause control
	watchdogPaused atomic.Bool

	// Last port-bind error per device. This is the reachability signal in
	// native deployments, where the bind is direct.
	startErrors     map[int]string
	startErrorMutex sync.RWMutex

	// newEmulator overrides createEmulator in tests.
	newEmulator func(models.Device) (Emulator, error)
}

// WatchdogInfo holds per-device monitoring state
type WatchdogInfo struct {
	FailureCount int
	LastCheck    time.Time
	LastStatus   string
}

// NewManager creates a new emulator manager
func NewManager(serviceDB database.DBInterface, emulatorDB database.DBInterface, tracer *trace.Tracer) *Manager {
	return &Manager{
		ServiceDB:      serviceDB,
		EmulatorDB:     emulatorDB,
		Tracer:         tracer,
		emulators:      make(map[int]Emulator),
		watchdog:       make(map[int]*WatchdogInfo),
		shutdownChan:   make(chan struct{}),
		watchdogTicker: time.NewTicker(10 * time.Second), // health check every 10 s
		starting:       make(map[int]bool),
		Logs: devicelog.NewRegistry(devicelog.DefaultOptions(), func(err error) {
			tracer.Error("%v", err)
		}),
	}
}

// Initialize resets device status and starts the watchdog.
func (m *Manager) Initialize() error {
	m.Tracer.Info("Initializing emulator manager")

	// Mark every device as stopped on startup
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := m.ServiceDB.Exec(ctx, "UPDATE service.devices SET status = 'stopped'")
	if err != nil {
		return fmt.Errorf("failed to reset device status: %w", err)
	}

	// Seed watchdog state for existing devices
	devices, err := m.ListDevices()
	if err != nil {
		return fmt.Errorf("failed to list devices for initialization: %w", err)
	}

	for _, device := range devices {
		m.watchdog[device.ID] = &WatchdogInfo{
			FailureCount: 0,
			LastCheck:    time.Now(),
			LastStatus:   "stopped",
		}
	}

	// Start the watchdog in the background
	go m.startWatchdog()

	return nil
}

// getDeviceType derives the device type from its model
func (m *Manager) getDeviceType(model string) int {
	switch model {
	case "Dahua":
		return 1
	case "Hikvision":
		return 2
	default:
		return 0
	}
}

// deviceColumns is the column list every device query scans, in scanDevice
// order.
const deviceColumns = `id, name, ip_address, port, model, enabled, type,
	status, event_interval, total_users, log_enabled`

// scanDevice reads one row selected with deviceColumns.
func scanDevice(row pgx.Row, d *models.Device) error {
	return row.Scan(&d.ID, &d.Name, &d.IPAddress, &d.Port,
		&d.Model, &d.Enabled, &d.Type, &d.Status,
		&d.EventInterval, &d.TotalUsers, &d.LogEnabled)
}

// listStoredDevices returns every device exactly as stored. Its Status is
// the desired state ("running" after a successful start, "stopped" after a
// stop), which is what the watchdog reconciles against.
func (m *Manager) listStoredDevices() ([]models.Device, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := m.ServiceDB.Query(ctx,
		"SELECT "+deviceColumns+" FROM service.devices ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("failed to query devices: %w", err)
	}
	defer rows.Close()

	var devices []models.Device
	for rows.Next() {
		var device models.Device
		if err := scanDevice(rows, &device); err != nil {
			m.Tracer.Error("Failed to scan device: %v", err)
			continue
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

// liveStatusLocked is the actual state of a device: "running" when its emulator
// serves, "starting" while a start is in progress, "stopped" otherwise.
// Callers hold emulatorMutex.
func (m *Manager) liveStatusLocked(id int) string {
	if em, ok := m.emulators[id]; ok && em.IsRunning() {
		return "running"
	}
	if m.starting[id] {
		return "starting"
	}
	return "stopped"
}

// ListDevices returns every device with its live status.
func (m *Manager) ListDevices() ([]models.Device, error) {
	devices, err := m.listStoredDevices()
	if err != nil {
		return nil, err
	}

	// One RLock for the whole fleet; the rows are already read.
	m.emulatorMutex.RLock()
	for i := range devices {
		devices[i].Status = m.liveStatusLocked(devices[i].ID)
	}
	m.emulatorMutex.RUnlock()

	return devices, nil
}

// readStoredDevice reads one device as stored. A missing row is
// ErrDeviceNotFound; any other failure (timeout, connection) is returned as
// is, so the API can tell "not found" from "database unavailable".
func (m *Manager) readStoredDevice(id int) (models.Device, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var device models.Device
	err := scanDevice(m.ServiceDB.QueryRow(ctx,
		"SELECT "+deviceColumns+" FROM service.devices WHERE id = $1", id), &device)
	if errors.Is(err, pgx.ErrNoRows) {
		return device, fmt.Errorf("%w: %d", ErrDeviceNotFound, id)
	}
	if err != nil {
		return device, fmt.Errorf("failed to read device %d: %w", id, err)
	}
	return device, nil
}

// GetDevice returns one device with its live status.
func (m *Manager) GetDevice(id int) (models.Device, error) {
	device, err := m.readStoredDevice(id)
	if err != nil {
		return device, err
	}

	m.emulatorMutex.RLock()
	device.Status = m.liveStatusLocked(id)
	m.emulatorMutex.RUnlock()

	return device, nil
}

// Start starts the emulator for one device.
func (m *Manager) Start(id int) error {
	// Reserve the ID and release the mutex before the actual start: the
	// emulator opens a port and connections, which can take seconds, and
	// this is the same mutex ListDevices uses to read fleet state.
	m.emulatorMutex.Lock()
	if emulator, exists := m.emulators[id]; exists && emulator.IsRunning() {
		m.emulatorMutex.Unlock()
		return fmt.Errorf("%w: emulator %d already running", ErrDeviceRunning, id)
	}
	if m.starting[id] {
		m.emulatorMutex.Unlock()
		return fmt.Errorf("%w: emulator %d is already starting", ErrDeviceRunning, id)
	}
	if m.starting == nil {
		m.starting = make(map[int]bool)
	}
	m.starting[id] = true
	m.emulatorMutex.Unlock()

	err := m.startReserved(id)

	m.emulatorMutex.Lock()
	delete(m.starting, id)
	m.emulatorMutex.Unlock()

	// Failures notify too: the client that requested the start is waiting
	// for the final state, and the last error comes with the re-read.
	m.NotifyChanged(id)
	return err
}

// startTimeout bounds one emulator Start. The bind is synchronous and
// normally takes milliseconds; the bound protects the fleet ramp from a
// constructor stuck on the database.
const startTimeout = 10 * time.Second

// startReserved starts an ID already reserved in m.starting.
func (m *Manager) startReserved(id int) error {
	device, err := m.readStoredDevice(id)
	if err != nil {
		return err
	}

	if device.Enabled != 1 {
		return fmt.Errorf("%w: device %d", ErrDeviceDisabled, id)
	}

	emulator, err := m.createEmulator(device)
	if err != nil {
		return fmt.Errorf("failed to create emulator: %w", err)
	}

	startErrChan := make(chan error, 1)
	go func() {
		startErrChan <- emulator.Start()
	}()

	select {
	case err := <-startErrChan:
		m.recordStartResult(id, err)
		if err != nil {
			return fmt.Errorf("failed to start emulator: %w", err)
		}
	case <-time.After(startTimeout):
		// The Start may still finish later. Wait for it in the background
		// and stop what it started: an emulator that is not in m.emulators
		// would hold its port with nothing able to stop it.
		go func() {
			if err := <-startErrChan; err == nil {
				_ = emulator.Stop()
			}
		}()
		timeoutErr := fmt.Errorf("timeout starting emulator %d after %v", id, startTimeout)
		m.recordStartResult(id, timeoutErr)
		return timeoutErr
	}

	// Store the emulator BEFORE updating the database (keeps state consistent)
	m.emulatorMutex.Lock()
	m.emulators[id] = emulator
	m.emulatorMutex.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = m.ServiceDB.Exec(ctx, "UPDATE service.devices SET status = 'running' WHERE id = $1", id)
	if err != nil {
		m.Tracer.Error("Failed to update device status in DB for device %d: %v", id, err)
		// Don't fail, just log - the emulator is already running in memory
	}

	m.resetWatchdog(id, "running")

	m.Tracer.Info("Started emulator for device %d (%s)", id, device.Name)
	return nil
}

// resetWatchdog clears the device's failure counter after a state change
// the manager made itself.
func (m *Manager) resetWatchdog(id int, status string) {
	m.watchdogMutex.Lock()
	defer m.watchdogMutex.Unlock()
	if m.watchdog == nil {
		m.watchdog = make(map[int]*WatchdogInfo)
	}
	info, ok := m.watchdog[id]
	if !ok {
		info = &WatchdogInfo{}
		m.watchdog[id] = info
	}
	info.FailureCount = 0
	info.LastStatus = status
	info.LastCheck = time.Now()
}

// logInfo is what goes in the header of each device log file.
func logInfo(device models.Device) devicelog.Info {
	return devicelog.Info{ID: device.ID, Name: device.Name, Type: device.Model, Port: device.Port}
}

// deviceLog returns the device logger with the database flag applied.
func (m *Manager) deviceLog(device models.Device) *devicelog.Logger {
	l := m.Logs.For(logInfo(device))
	if err := l.SetEnabled(device.LogEnabled == 1); err != nil {
		m.Tracer.Warning("device %d log: %v", device.ID, err)
	}
	return l
}

// createEmulator builds an emulator for the device type
func (m *Manager) createEmulator(device models.Device) (Emulator, error) {
	if m.newEmulator != nil {
		return m.newEmulator(device)
	}
	dlog := m.deviceLog(device)
	switch device.Model {
	case "Dahua":
		return dahua.NewEmulator(m.EmulatorDB, device, m.Tracer, dlog), nil
	case "Hikvision":
		return hikvision.NewEmulator(m.EmulatorDB, device, m.Tracer, dlog), nil
	default:
		return nil, fmt.Errorf("unsupported device model: %s", device.Model)
	}
}

// Stop stops the emulator for one device. The emulator leaves the map under
// the lock and is stopped outside it (a stop can take seconds and the same
// lock serves the fleet listing); on failure it is put back.
func (m *Manager) Stop(id int) error {
	m.emulatorMutex.Lock()
	emulator, exists := m.emulators[id]
	if !exists {
		m.emulatorMutex.Unlock()
		if _, err := m.readStoredDevice(id); err != nil {
			return err // not found, or the database failed
		}
		return fmt.Errorf("%w: emulator %d", ErrNotRunning, id)
	}
	delete(m.emulators, id)
	m.emulatorMutex.Unlock()

	// A dead emulator (its server exited on its own) has nothing left to
	// stop; recording the stop is what keeps the watchdog from restarting
	// it.
	if emulator.IsRunning() {
		if err := emulator.Stop(); err != nil {
			m.emulatorMutex.Lock()
			if _, taken := m.emulators[id]; !taken {
				m.emulators[id] = emulator
			}
			m.emulatorMutex.Unlock()
			return fmt.Errorf("failed to stop emulator: %w", err)
		}
	}

	m.markStopped(id)
	m.Tracer.Info("Stopped emulator for device %d", id)
	m.NotifyChanged(id)
	return nil
}

// markStopped persists the stopped state and resets the watchdog. It runs
// outside emulatorMutex: it does database I/O, and holding the mutex here
// would block fleet reads.
func (m *Manager) markStopped(id int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := m.ServiceDB.Exec(ctx, "UPDATE service.devices SET status = 'stopped' WHERE id = $1", id); err != nil {
		m.Tracer.Error("Failed to update device status: %v", err)
	}

	m.resetWatchdog(id, "stopped")
}

// StartAll starts every enabled emulator with bounded concurrency and retries
func (m *Manager) StartAll() error {
	// Pause the watchdog during mass startup
	m.Tracer.Info("Pausing watchdog during mass startup")
	m.watchdogPaused.Store(true)
	defer func() {
		// Wait 15 seconds after startup to stabilize before resuming the watchdog
		go func() {
			time.Sleep(15 * time.Second)
			m.watchdogPaused.Store(false)
			m.Tracer.Info("Watchdog resumed after startup stabilization")
		}()
	}()

	devices, err := m.ListDevices()
	if err != nil {
		return fmt.Errorf("failed to list devices: %w", err)
	}

	// Keep only enabled, stopped devices
	var devicesToStart []models.Device
	for _, device := range devices {
		if device.Enabled == 1 && device.Status == "stopped" {
			devicesToStart = append(devicesToStart, device)
		}
	}

	if len(devicesToStart) == 0 {
		m.Tracer.Info("No devices to start")
		return nil
	}

	m.Tracer.Info("Starting %d emulators with controlled concurrency (watchdog paused)", len(devicesToStart))

	// Tuning for mass startup
	const (
		maxConcurrent = 20                     // up to 20 emulators at once
		delayBetween  = 200 * time.Millisecond // short delay between batches
		maxRetries    = 3                      // up to 3 attempts
		retryDelay    = 1 * time.Second        // fast retry
	)

	// Semaphore bounding concurrency
	semaphore := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	errorsChan := make(chan error, len(devicesToStart))

	// Start devices in controlled batches
	for i, device := range devicesToStart {
		wg.Add(1)

		// Small delay between goroutine launches (only between batches)
		if i > 0 && i%maxConcurrent == 0 {
			time.Sleep(delayBetween)
		}

		go func(dev models.Device, index int) {
			defer wg.Done()

			// Acquire a semaphore slot
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			m.Tracer.Info("[%d/%d] Starting device %d (%s)...", index+1, len(devicesToStart), dev.ID, dev.Name)

			// Try to start with bounded retries
			var lastErr error
			for attempt := 1; attempt <= maxRetries; attempt++ {
				err := m.Start(dev.ID)
				if err == nil {
					m.Tracer.Info("[%d/%d] ✓ Device %d started successfully", index+1, len(devicesToStart), dev.ID)
					return
				}

				lastErr = err

				// Not the last attempt: retry quickly
				if attempt < maxRetries {
					m.Tracer.Warning("[%d/%d] Attempt %d failed for device %d, retrying in %v...",
						index+1, len(devicesToStart), attempt, dev.ID, retryDelay)
					time.Sleep(retryDelay)
				}
			}

			// All attempts failed
			finalErr := fmt.Errorf("device %d (%s) failed after %d attempts: %w",
				dev.ID, dev.Name, maxRetries, lastErr)
			m.Tracer.Error("[%d/%d] ✗ %v", index+1, len(devicesToStart), finalErr)
			errorsChan <- finalErr

		}(device, i)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errorsChan)

	// Collect errors
	var errors []error
	for err := range errorsChan {
		errors = append(errors, err)
	}

	// Report the final result
	successCount := len(devicesToStart) - len(errors)
	m.Tracer.Info("StartAll completed: %d/%d devices started successfully", successCount, len(devicesToStart))

	if len(errors) > 0 {
		return fmt.Errorf("failed to start %d/%d devices", len(errors), len(devicesToStart))
	}

	return nil
}

// StopAll stops every emulator
func (m *Manager) StopAll() {
	// Take the whole map at once and stop outside the mutex: stopping and
	// writing to the database one by one with the mutex held would block
	// ListDevices until the end.
	m.emulatorMutex.Lock()
	active := m.emulators
	m.emulators = make(map[int]Emulator)
	m.emulatorMutex.Unlock()

	for id, emulator := range active {
		// Dead emulators are recorded as stopped too, so the watchdog does
		// not bring them back.
		if emulator.IsRunning() {
			if err := emulator.Stop(); err != nil {
				m.Tracer.Error("Failed to stop emulator %d: %v", id, err)
			}
		}
		m.markStopped(id)
		// Notify per device so "Stop all" shows up in the UI without a
		// page reload.
		m.NotifyChanged(id)
	}

	m.Tracer.Info("Stopped all emulators")
}

// startWatchdog runs the periodic health-check loop.
func (m *Manager) startWatchdog() {
	m.Tracer.Info("Starting watchdog system")

	for {
		select {
		case <-m.watchdogTicker.C:
			m.performHealthChecks()
		case <-m.shutdownChan:
			m.Tracer.Info("Watchdog system shutting down")
			return
		}
	}
}

// watchdogThreshold is how many consecutive checks must see a device in the
// wrong state before the watchdog acts. It absorbs the short windows in
// which the stored and the live state legitimately disagree (between a
// start registering the emulator and writing status = 'running').
const watchdogThreshold = 3

// performHealthChecks reconciles each device's desired state (the stored
// status and enabled flag) with its actual state (whether its emulator is
// serving).
func (m *Manager) performHealthChecks() {
	if m.watchdogPaused.Load() {
		m.Tracer.Debug("Watchdog paused, skipping health checks")
		return
	}

	devices, err := m.listStoredDevices()
	if err != nil {
		m.Tracer.Error("Failed to list devices for health check: %v", err)
		return
	}

	for _, device := range devices {
		m.checkDeviceHealth(device)
	}
}

// checkDeviceHealth takes one stored device (Status = desired state).
//
//   - Should run but does not serve (its server exited, or the emulator is
//     gone): restarted after watchdogThreshold consecutive checks.
//   - Serves but should not (stored as stopped, or disabled): stopped after
//     the same number of checks.
//
// A device whose start is in progress is left alone.
func (m *Manager) checkDeviceHealth(device models.Device) {
	m.emulatorMutex.RLock()
	emulator, exists := m.emulators[device.ID]
	isRunning := exists && emulator.IsRunning()
	starting := m.starting[device.ID]
	m.emulatorMutex.RUnlock()

	if starting {
		return
	}

	if isRunning {
		m.updateDeviceTotalUsers(device.ID, device.TotalUsers, emulator)
	}

	wantRunning := device.Status == "running" && device.Enabled == 1

	m.watchdogMutex.Lock()
	if m.watchdog == nil {
		m.watchdog = make(map[int]*WatchdogInfo)
	}
	info, ok := m.watchdog[device.ID]
	if !ok {
		info = &WatchdogInfo{LastStatus: "unknown"}
		m.watchdog[device.ID] = info
	}
	info.LastCheck = time.Now()
	if wantRunning == isRunning {
		info.FailureCount = 0
		m.watchdogMutex.Unlock()
		return
	}
	info.FailureCount++
	failures := info.FailureCount
	m.watchdogMutex.Unlock()

	if wantRunning {
		m.Tracer.Warning("[WATCHDOG] Device %d (%s) should be running but is not serving (check %d/%d)",
			device.ID, device.Name, failures, watchdogThreshold)
	} else {
		m.Tracer.Warning("[WATCHDOG] Device %d (%s) is serving but should be stopped (check %d/%d)",
			device.ID, device.Name, failures, watchdogThreshold)
	}
	if failures < watchdogThreshold {
		return
	}

	if wantRunning {
		m.Tracer.Info("[WATCHDOG] Restarting device %d", device.ID)
		if err := m.Start(device.ID); err != nil {
			m.Tracer.Error("[WATCHDOG] Failed to restart device %d: %v", device.ID, err)
			return
		}
	} else {
		m.Tracer.Info("[WATCHDOG] Stopping device %d", device.ID)
		if err := m.Stop(device.ID); err != nil {
			m.Tracer.Error("[WATCHDOG] Failed to stop device %d: %v", device.ID, err)
			return
		}
	}
	m.resetWatchdog(device.ID, device.Status)
}

// updateDeviceTotalUsers persists the user count only when it changes and
// notifies the UI. Writing every running device on every watchdog cycle
// would mean 1000 UPDATEs and 2000 trace lines every 10 s on a large fleet.
func (m *Manager) updateDeviceTotalUsers(deviceID, current int, emulator Emulator) {
	totalUsers, err := emulator.GetTotalUsers()
	if err != nil {
		m.Tracer.Error("[WATCHDOG] Failed to get total users for device %d: %v", deviceID, err)
		return
	}
	if totalUsers == current {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := m.ServiceDB.Exec(ctx,
		"UPDATE service.devices SET total_users = $1, updated_at = NOW() WHERE id = $2",
		totalUsers, deviceID); err != nil {
		m.Tracer.Error("[WATCHDOG] Failed to update total_users for device %d: %v", deviceID, err)
		return
	}

	m.Tracer.Info("[WATCHDOG] Device %d: total_users %d -> %d", deviceID, current, totalUsers)
	m.NotifyChanged(deviceID)
}

// Shutdown stops the watchdog and every emulator
func (m *Manager) Shutdown() {
	m.Tracer.Info("Shutting down emulator manager")

	// Signal the watchdog to stop
	close(m.shutdownChan)

	// Stop the ticker
	m.watchdogTicker.Stop()

	// Stop every emulator
	m.StopAll()
}

// UpdateDeviceSettings toggles the device's request log. A device that does
// not exist is ErrDeviceNotFound, and no logger is registered for it.
func (m *Manager) UpdateDeviceSettings(id int, logEnabled bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logEnabledInt := 0
	if logEnabled {
		logEnabledInt = 1
	}

	// RETURNING both proves the row exists and gives the log header its
	// fields, in one round trip.
	info := devicelog.Info{ID: id}
	err := m.ServiceDB.QueryRow(ctx,
		`UPDATE service.devices SET log_enabled = $1, updated_at = NOW()
		  WHERE id = $2
		  RETURNING name, model, port`,
		logEnabledInt, id).Scan(&info.Name, &info.Type, &info.Port)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %d", ErrDeviceNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("failed to update device settings: %w", err)
	}

	// Takes effect on a running emulator: it shares the same logger.
	if err := m.Logs.SetEnabled(info, logEnabled); err != nil {
		m.Tracer.Warning("device %d log: %v", id, err)
	}

	m.NotifyChanged(id)
	return nil
}

func (m *Manager) GetPoolStats() map[string]interface{} {
	stats := map[string]interface{}{
		"timestamp": time.Now(),
	}

	// Prefer DualPoolManager stats, fall back to AdaptivePool
	if dpm, ok := m.ServiceDB.(*database.DualPoolManager); ok {
		stats["service_db"] = dpm.GetStats()
	} else if ap, ok := m.ServiceDB.(*database.AdaptivePool); ok {
		stats["service_db"] = ap.GetStats()
	}

	if dpm, ok := m.EmulatorDB.(*database.DualPoolManager); ok {
		stats["emulator_db"] = dpm.GetStats()
	} else if ap, ok := m.EmulatorDB.(*database.AdaptivePool); ok {
		stats["emulator_db"] = ap.GetStats()
	}

	return stats
}

// recordStartResult stores or clears the device's last start error. A
// successful start clears the previous error: the verdict must reflect the
// latest attempt, not the worst one.
func (m *Manager) recordStartResult(deviceID int, err error) {
	m.startErrorMutex.Lock()
	defer m.startErrorMutex.Unlock()

	if m.startErrors == nil {
		m.startErrors = make(map[int]string)
	}

	if err == nil {
		delete(m.startErrors, deviceID)
		return
	}
	m.startErrors[deviceID] = err.Error()
}

// LastStartError returns the device's last start error, or an empty string
// if the last start succeeded or there was never a start.
func (m *Manager) LastStartError(deviceID int) string {
	m.startErrorMutex.RLock()
	defer m.startErrorMutex.RUnlock()
	return m.startErrors[deviceID]
}
