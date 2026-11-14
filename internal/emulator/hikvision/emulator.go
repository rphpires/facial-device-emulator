package hikvision

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rphpires/facial-device-emulator/assets"
	"github.com/rphpires/facial-device-emulator/internal/database"
	"github.com/rphpires/facial-device-emulator/internal/devicelog"
	"github.com/rphpires/facial-device-emulator/internal/devicetime"
	"github.com/rphpires/facial-device-emulator/internal/models"
	"github.com/rphpires/facial-device-emulator/internal/trace"
	"github.com/rphpires/facial-device-emulator/internal/utils"

	"github.com/gin-gonic/gin"
)

// Emulator emulates a Hikvision access-control terminal.
type Emulator struct {
	device           models.Device
	tracer           *trace.Tracer
	repo             *Repository
	macAddress       string
	ipv6Address      string // link-local address derived from macAddress
	deleteInProgress atomic.Bool
	startedAt        atomic.Int64 // UnixNano of the last successful Start, 0 if never

	// Lifecycle. lifeMu serializes Start and Stop and guards server and
	// stopChan. running is atomic because event generators, stream handlers
	// and the manager read it without the lock. stopChan is per run: Start
	// creates a fresh one and every goroutine of that run captures it at
	// launch (runStop), so a later Start never swaps a channel under a
	// goroutine that is still selecting on the old one.
	lifeMu   sync.Mutex
	server   *http.Server
	running  atomic.Bool
	stopChan chan struct{}

	// eventRand draws the standalone-mode subEventType. Guarded because the
	// event generator may run on more than one goroutine.
	eventRand   *rand.Rand
	eventRandMu sync.Mutex

	// deleteCardsFn/deleteFingersFn let the delete handlers be tested without a
	// database. In production NewEmulator points them at the repository methods.
	deleteCardsFn   func(employeeNo string) (int, error)
	deleteFingersFn func(employeeNo string) (int, error)

	// eventLog is the event history the device keeps "in memory", queried by
	// POST AcsEvent and purged by StorageCfg mode=time.
	eventLog *eventLog

	// dlog is this device's traffic log (the "record event log" option).
	// It may be nil: every devicelog method is then a no-op.
	dlog *devicelog.Logger

	// timeZone is the zone set by the client through PUT /ISAPI/System/time.
	// nil means "not set": devicetime.Default() applies.
	timeZone atomic.Pointer[time.Location]
}

// location is the time zone the device stamps and parses times with: the one
// the client configured via PUT /ISAPI/System/time or, when none was set, the
// emulator.default_timezone default. Events' dateTime, the AcsEvent `time`
// field and StorageCfg's checkTime must all use this same zone, otherwise the
// purge window shifts and StorageCfg never deletes anything.
func (e *Emulator) location() *time.Location {
	if loc := e.timeZone.Load(); loc != nil {
		return loc
	}
	return devicetime.Default()
}

// intPtr returns a pointer to the given int (used for optional event fields
// that must distinguish "zero" from "absent").
func intPtr(i int) *int { return &i }

// serialCounter numbers events like the real device: monotonic per process.
// Clients use serialNo as the remote-check key and frontSerialNo (the previous
// event's serialNo) as the "already decided" signal.
var serialCounter int64 = 4434

// linkLocalIPv6 derives the EUI-64 link-local IPv6 address for a MAC, as a
// real device would advertise it.
func linkLocalIPv6(mac string) string {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return "fe80::200:ff:fe00:1"
	}
	ip := net.IP{0xfe, 0x80, 0, 0, 0, 0, 0, 0,
		hw[0] ^ 0x02, hw[1], hw[2], 0xff, 0xfe, hw[3], hw[4], hw[5]}
	return ip.String()
}

func nextSerialNo() int {
	return int(atomic.AddInt64(&serialCounter, 1))
}

// NewEmulator creates a new Hikvision emulator instance.
func NewEmulator(db database.DBInterface, device models.Device, tracer *trace.Tracer, dlog *devicelog.Logger) *Emulator {
	tracer.Info("Initializing Hikvision emulator model: %s", device.Name)

	repo := NewRepository(db, device.ID)
	macAddress := utils.GenerateMacAddress()

	emulator := &Emulator{
		device:      device,
		tracer:      tracer,
		repo:        repo,
		macAddress:  macAddress,
		ipv6Address: linkLocalIPv6(macAddress),
		stopChan:    make(chan struct{}),
		eventRand:   rand.New(rand.NewSource(time.Now().UnixNano())),
		eventLog:    newEventLog(500),
		dlog:        dlog,
	}

	emulator.deleteCardsFn = repo.DeleteCardsByEmployeeNo
	emulator.deleteFingersFn = repo.DeleteFingerprintsByEmployeeNo

	return emulator
}

// recordEvent stores the event in the local history used for offline queries,
// which backs POST /ISAPI/AccessControl/AcsEvent.
func (e *Emulator) recordEvent(event *Event, at time.Time) {
	if e.eventLog == nil {
		return
	}
	e.eventLog.Append(AcsEventRecord{
		Time:         at,
		Major:        event.AccessControllerEvent.MajorEventType,
		Minor:        event.AccessControllerEvent.SubEventType,
		CardNo:       event.AccessControllerEvent.CardNo,
		Name:         event.AccessControllerEvent.Name,
		EmployeeNo:   event.AccessControllerEvent.EmployeeNoString,
		CardReaderNo: event.AccessControllerEvent.CardReaderNo,
		DoorNo:       event.AccessControllerEvent.DoorNo,
		SerialNo:     event.AccessControllerEvent.SerialNo,
	})
}

// drawSubEvent returns the next standalone-mode subEventType.
func (e *Emulator) drawSubEvent() int {
	if e.eventRand == nil {
		return MinorFaceVerifyPass
	}
	e.eventRandMu.Lock()
	defer e.eventRandMu.Unlock()
	return pickStandaloneSubEvent(e.eventRand)
}

// newRouter builds the emulator's router with the diagnostic middlewares.
func (e *Emulator) newRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	// The log sits outside Recovery so it records the 500 of a handler that
	// panicked.
	router.Use(e.dlog.Gin())
	router.Use(gin.Recovery())

	// Diagnostic middleware: logs every request reaching this emulator.
	router.Use(func(c *gin.Context) {
		e.tracer.Info("[REQ] %s %s from %s (CL=%d CT=%q)",
			c.Request.Method, c.Request.URL.Path, c.Request.RemoteAddr,
			c.Request.ContentLength, c.Request.Header.Get("Content-Type"))
		c.Next()
		e.tracer.Info("[RES] %s %s -> %d (%d bytes)",
			c.Request.Method, c.Request.URL.Path, c.Writer.Status(), c.Writer.Size())
	})

	// NoRoute: logs requests to unregistered routes (avoids silent responses).
	router.NoRoute(func(c *gin.Context) {
		e.tracer.Error("[NOROUTE] %s %s not matched", c.Request.Method, c.Request.URL.Path)
		c.String(http.StatusNotFound, "not found: %s %s", c.Request.Method, c.Request.URL.Path)
	})

	e.SetupRoutes(router)
	return router
}

// runStop returns the stop channel of the current (or last) run. Goroutines
// belonging to a run call it once at launch and keep the result.
func (e *Emulator) runStop() chan struct{} {
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()
	return e.stopChan
}

// Start binds the device port and starts serving. The bind is synchronous:
// a port that is already taken is reported here, not lost in a goroutine.
func (e *Emulator) Start() error {
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()

	if e.running.Load() {
		return fmt.Errorf("emulator already running")
	}

	e.tracer.Info("Starting Hikvision emulator: %s", e.device.Name)

	ln, err := utils.ListenPort(e.device.Port)
	if err != nil {
		return err
	}

	e.tracer.Info("Hikvision HTTP server listening on %s (device: %s)", ln.Addr(), e.device.IPAddress)
	srv := &http.Server{
		Handler:      e.newRouter(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
		ConnState:    e.dlog.ConnState,
	}
	stop := make(chan struct{})

	e.server = srv
	e.stopChan = stop
	e.startedAt.Store(time.Now().UnixNano())
	e.running.Store(true)
	e.dlog.Printf("START", "emulator %q on port %d", e.device.Name, e.device.Port)

	go e.serve(srv, ln, stop)

	if e.device.EventInterval > 0 {
		go e.startEventGenerator(stop)
	}

	return nil
}

// serve runs the HTTP server of one run. If Serve returns for any reason
// other than Stop, the run is marked dead (IsRunning becomes false and its
// goroutines are released) so the manager's watchdog can restart it.
func (e *Emulator) serve(srv *http.Server, ln net.Listener, stop chan struct{}) {
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return
	}

	e.tracer.Error("Hikvision server for %s exited unexpectedly: %v", e.device.Name, err)
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()
	if e.stopChan == stop && e.running.Load() {
		e.running.Store(false)
		close(stop)
		e.dlog.Printf("STOP", "server exited unexpectedly: %v", err)
	}
}

// Stop stops the emulator.
func (e *Emulator) Stop() error {
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()

	if !e.running.Load() {
		return fmt.Errorf("emulator not running")
	}

	e.tracer.Info("Stopping Hikvision emulator: %s", e.device.Name)

	e.running.Store(false)
	close(e.stopChan)
	e.dlog.Printf("STOP", "emulator stopped")

	if e.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return e.server.Shutdown(ctx)
	}

	return nil
}

// IsRunning reports whether the emulator is serving.
func (e *Emulator) IsRunning() bool {
	return e.running.Load()
}

// GetInfo returns information about the emulator.
func (e *Emulator) GetInfo() models.Device {
	device := e.device
	if e.running.Load() {
		device.Status = "running"
	} else {
		device.Status = "stopped"
	}

	// Refresh the user count
	if count, err := e.repo.GetTotalUsers(); err == nil {
		device.TotalUsers = count
	}

	return device
}

// GetType returns the emulator type.
func (e *Emulator) GetType() string {
	return "Hikvision"
}

// GetStatus returns the emulator's current status.
func (e *Emulator) GetStatus() string {
	if e.running.Load() {
		return "running"
	}
	return "stopped"
}

// GenerateEvent generates and sends an event.
func (e *Emulator) GenerateEvent() error {
	// Check whether local authentication is enabled
	localAuth, err := e.repo.GetSetting("LocalAuthentication")
	if err != nil {
		return fmt.Errorf("failed to get LocalAuthentication setting: %w", err)
	}

	if localAuth == "0" {
		// Remote authentication mode: send to the remote server
		return e.generateOnlineEvent()
	}

	return nil
}

// startEventGenerator runs the automatic event generator.
// stop is the channel of the run that launched it.
func (e *Emulator) startEventGenerator(stop <-chan struct{}) {
	e.tracer.Info("Starting event generator with interval: %ds", e.device.EventInterval)

	ticker := time.NewTicker(time.Duration(e.device.EventInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if e.running.Load() {
				if err := e.GenerateEvent(); err != nil {
					e.tracer.Error("Failed to generate event: %v", err)
				}
			}
		case <-stop:
			e.tracer.Info("Event generator stopped")
			return
		}
	}
}

// generateOnlineEvent generates an online (remote-check) event.
func (e *Emulator) generateOnlineEvent() error {
	e.tracer.Info("Generating online event")

	// Pick a random user + card row
	name, cardNo, employeeNo, err := e.repo.GetRandomUserAndCard()
	if err != nil {
		e.tracer.Warning("No user/card found for event generation: %v", err)
		return nil
	}

	// Device time zone
	loc := e.location()
	currentTime := time.Now().In(loc)

	// Build the event
	event := &Event{
		IPAddress:        e.device.IPAddress,
		IPv6Address:      e.ipv6Address,
		PortNo:           e.device.Port,
		Protocol:         "HTTP",
		MacAddress:       e.macAddress,
		ChannelID:        1,
		DateTime:         currentTime.Format("2006-01-02T15:04:05-07:00"),
		ActivePostCount:  1,
		EventType:        "AccessControllerEvent",
		EventState:       "active",
		EventDescription: "Access Controller Event",
	}

	// Fill in the event data
	event.AccessControllerEvent.DeviceName = "subdoorOne"
	event.AccessControllerEvent.MajorEventType = 5
	event.AccessControllerEvent.SubEventType = 75
	event.AccessControllerEvent.CardNo = cardNo
	event.AccessControllerEvent.CardType = 1
	event.AccessControllerEvent.Name = name
	event.AccessControllerEvent.CardReaderKind = 1
	event.AccessControllerEvent.CardReaderNo = 1
	event.AccessControllerEvent.VerifyNo = 189
	event.AccessControllerEvent.EmployeeNoString = employeeNo
	event.AccessControllerEvent.SerialNo = 4435
	event.AccessControllerEvent.UserType = "normal"
	event.AccessControllerEvent.CurrentVerifyMode = "faceOrFpOrCardOrPw"
	event.AccessControllerEvent.CurrentEvent = true
	// Online mode (remoteCheckDoorEnabled): the device does NOT decide here. It
	// emits a remote-check request: frontSerialNo/statusValue absent (nil),
	// remoteCheck=true. The missing frontSerialNo makes the server take the
	// remote verification path and answer with PUT /ISAPI/AccessControl/remoteCheck.
	event.AccessControllerEvent.FrontSerialNo = nil
	event.AccessControllerEvent.AttendanceStatus = "undefined"
	event.AccessControllerEvent.Label = ""
	event.AccessControllerEvent.StatusValue = nil
	event.AccessControllerEvent.RemoteCheck = true
	event.AccessControllerEvent.Mask = "no"
	event.AccessControllerEvent.Helmet = "unknown"
	event.AccessControllerEvent.PicturesNumber = 1
	event.AccessControllerEvent.PurePwdVerifyEnable = true
	event.AccessControllerEvent.FaceRect.Height = 0.268
	event.AccessControllerEvent.FaceRect.Width = 0.477
	event.AccessControllerEvent.FaceRect.X = 0.286
	event.AccessControllerEvent.FaceRect.Y = 0.354
	event.AccessControllerEvent.UnlockRoomNo = "3723243075"

	e.recordEvent(event, currentTime)

	// Send the event to the remote server
	if err := e.sendEventToRemoteServer(event); err != nil {
		return fmt.Errorf("failed to send event: %w", err)
	}

	// Randomly simulate the door events that complete the access
	if utils.RandomAccessNotDone() {
		e.tracer.Info("Sending door events to complete access")
		go e.simulateDoorEvents()
	}

	return nil
}

// buildPushEventMultipart assembles the event POST body the emulator sends
// to the remote server (the URL configured via ISAPI httpHosts). The first part carries the JSON event with
// Content-Disposition: form-data; name="event_log" so clients parse it as the
// event_log form field, as with a real device.
func buildPushEventMultipart(event *Event, imageData []byte, boundary string) []byte {
	eventJSON, _ := json.MarshalIndent(event, "", "  ")

	var body strings.Builder

	body.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	body.WriteString("Content-Disposition: form-data; name=\"event_log\"\r\n")
	body.WriteString("Content-Type: application/json; charset=\"UTF-8\"\r\n")
	body.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(eventJSON)))
	body.WriteString("\r\n")
	body.Write(eventJSON)
	body.WriteString("\r\n")

	body.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	body.WriteString("Content-Disposition: form-data; name=\"Picture\"; filename=\"Picture.jpg\"\r\n")
	body.WriteString("Content-Type: image/jpeg\r\n")
	body.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(imageData)))
	body.WriteString("Content-ID: pictureImage\r\n")
	body.WriteString("\r\n")
	body.Write(imageData)
	body.WriteString("\r\n")

	body.WriteString(fmt.Sprintf("--%s--\r\n", boundary))

	return []byte(body.String())
}

func (e *Emulator) sendEventToRemoteServer(event *Event) error {
	imageData, err := GetPhotoImageData()
	if err != nil {
		return fmt.Errorf("failed to decode image: %w", err)
	}

	boundary := "MIME_boundary"
	body := buildPushEventMultipart(event, imageData, boundary)

	// Build the URL from the per-device settings
	server, _ := e.repo.GetSetting("RemoteServer")
	port, _ := e.repo.GetSetting("RemotePort")
	path, _ := e.repo.GetSetting("RemoteURL")
	remoteURL := fmt.Sprintf("http://%s:%s%s", server, port, path)
	e.tracer.Info("Sending event to server: %s", remoteURL)

	// multipart/form-data, like a real device's push POST. x-mixed-replace is
	// the content type of alertStream (streaming), not of the push POST:
	// receivers that parse form fields (e.g. Werkzeug's request.form) get an
	// empty form with it and silently drop the event.
	contentType := fmt.Sprintf("multipart/form-data; boundary=%s", boundary)
	if err := e.postEvent(remoteURL, contentType, body, 5*time.Second); err != nil {
		return err
	}

	e.tracer.Info("Event sent successfully to %s", remoteURL)
	return nil
}

// postEvent sends the push POST to the remote server and records the outgoing
// request in the device log, on success or error.
func (e *Emulator) postEvent(remoteURL, contentType string, body []byte, timeout time.Duration) error {
	req, err := http.NewRequest("POST", remoteURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	start := time.Now()
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
		defer resp.Body.Close()
	}
	e.dlog.Out("POST", remoteURL, contentType, body, status, err, time.Since(start))

	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected response: %s", resp.Status)
	}
	return nil
}

// simulateDoorEvents simulates the door sequence the real device emits after
// granting access: open (25) and, a few seconds later, close (26). Clients
// rely on these two events to complete the access cycle.
func (e *Emulator) simulateDoorEvents() {
	time.Sleep(2 * time.Second)
	if err := e.sendDoorEvent(MinorDoorOpenNormal); err != nil {
		e.tracer.Error("Failed to send door open event: %v", err)
	}

	time.Sleep(3 * time.Second)
	if err := e.sendDoorEvent(MinorDoorCloseNormal); err != nil {
		e.tracer.Error("Failed to send door close event: %v", err)
	}
}

// buildDoorEvent builds a door event in ISAPI format. Clients read the event's
// "dateTime" and "macAddress" fields, so a non-ISAPI payload (e.g. a Dahua
// {"Events":[...]} object) would be dropped.
func (e *Emulator) buildDoorEvent(subEventType int) *Event {
	loc := e.location()
	currentTime := time.Now().In(loc)

	event := &Event{
		IPAddress:        e.device.IPAddress,
		IPv6Address:      e.ipv6Address,
		PortNo:           e.device.Port,
		Protocol:         "HTTP",
		MacAddress:       e.macAddress,
		ChannelID:        1,
		DateTime:         currentTime.Format("2006-01-02T15:04:05-07:00"),
		ActivePostCount:  1,
		EventType:        "AccessControllerEvent",
		EventState:       "active",
		EventDescription: "Access Controller Event",
	}

	serial := nextSerialNo()

	event.AccessControllerEvent.DeviceName = "subdoorOne"
	event.AccessControllerEvent.MajorEventType = 5
	event.AccessControllerEvent.SubEventType = subEventType
	event.AccessControllerEvent.CardReaderKind = 1
	event.AccessControllerEvent.CardReaderNo = 1
	event.AccessControllerEvent.DoorNo = 1
	event.AccessControllerEvent.SerialNo = serial
	event.AccessControllerEvent.UserType = "normal"
	event.AccessControllerEvent.CurrentEvent = true
	// A door state event is already decided by the device: frontSerialNo and
	// statusValue are present. Their absence is what signals a remote check.
	event.AccessControllerEvent.FrontSerialNo = intPtr(serial - 1)
	event.AccessControllerEvent.StatusValue = intPtr(0)
	event.AccessControllerEvent.AttendanceStatus = "undefined"
	event.AccessControllerEvent.Mask = "unknown"
	event.AccessControllerEvent.Helmet = "unknown"

	e.recordEvent(event, currentTime)

	return event
}

// sendDoorEvent sends a door state event to the remote server through the same
// multipart path as access events ("event_log" field).
func (e *Emulator) sendDoorEvent(subEventType int) error {
	event := e.buildDoorEvent(subEventType)
	e.tracer.Info("Sending door event subEventType=%d serialNo=%d", subEventType, event.AccessControllerEvent.SerialNo)
	return e.sendEventToRemoteServer(event)
}

// generateRandomEvent generates a random event for the alert stream.
func (e *Emulator) generateRandomEvent() ([]byte, error) {
	e.tracer.Info("Generating random event for streaming")

	// Check whether local authentication is enabled
	localAuth, err := e.repo.GetSetting("LocalAuthentication")
	if err != nil || localAuth != "1" {
		return nil, nil // No event outside local mode
	}

	// Pick a random user + card row
	name, cardNo, employeeNo, err := e.repo.GetRandomUserAndCard()
	if err != nil {
		return nil, fmt.Errorf("failed to get random user info: %w", err)
	}
	e.tracer.Info("generateRandomEvent.Hik: Name=%s", name)

	// Device time zone
	loc := e.location()
	currentTime := time.Now().In(loc)

	// Build the event
	event := &Event{
		IPAddress:        e.device.IPAddress,
		IPv6Address:      e.ipv6Address,
		PortNo:           e.device.Port,
		Protocol:         "HTTP",
		MacAddress:       e.macAddress,
		ChannelID:        1,
		DateTime:         currentTime.Format("2006-01-02T15:04:05-07:00"),
		ActivePostCount:  1,
		EventType:        "AccessControllerEvent",
		EventState:       "active",
		EventDescription: "Access Controller Event",
	}

	// Standalone mode: the device decides on its own, so the subtype varies,
	// exercising the client's INVALID_CARD / BIOMETRIC_VERIFICATION_FAILED /
	// CARD_EXPIRED paths as well as grants.
	minor := e.drawSubEvent()
	serial := nextSerialNo()

	// Fill in the access-control event data
	event.AccessControllerEvent.DeviceName = "subdoorOne"
	event.AccessControllerEvent.MajorEventType = 5
	event.AccessControllerEvent.SubEventType = minor
	event.AccessControllerEvent.CardNo = cardNo
	event.AccessControllerEvent.CardType = 1
	event.AccessControllerEvent.Name = name
	event.AccessControllerEvent.CardReaderKind = 1
	event.AccessControllerEvent.CardReaderNo = 1
	event.AccessControllerEvent.DoorNo = 1
	event.AccessControllerEvent.VerifyNo = 189
	event.AccessControllerEvent.EmployeeNoString = employeeNo
	event.AccessControllerEvent.SerialNo = serial
	event.AccessControllerEvent.UserType = "normal"
	event.AccessControllerEvent.CurrentVerifyMode = "faceOrFpOrCardOrPw"
	event.AccessControllerEvent.CurrentEvent = true
	// Local mode: event already decided, frontSerialNo/statusValue present.
	event.AccessControllerEvent.FrontSerialNo = intPtr(serial - 1)
	event.AccessControllerEvent.AttendanceStatus = "undefined"
	event.AccessControllerEvent.Label = ""
	event.AccessControllerEvent.StatusValue = intPtr(0)
	event.AccessControllerEvent.Mask = "no"
	event.AccessControllerEvent.Helmet = "unknown"
	event.AccessControllerEvent.PicturesNumber = 1
	event.AccessControllerEvent.PurePwdVerifyEnable = true
	event.AccessControllerEvent.FaceRect.Height = 0.268
	event.AccessControllerEvent.FaceRect.Width = 0.477
	event.AccessControllerEvent.FaceRect.X = 0.286
	event.AccessControllerEvent.FaceRect.Y = 0.354
	event.AccessControllerEvent.UnlockRoomNo = "3723243075"

	// On a denial the real device does not report the holder, only the number
	// read. Clearing name and employee number keeps the client from recording a
	// denied access as identified.
	if !subEventIsGrant(minor) {
		event.AccessControllerEvent.Name = ""
		event.AccessControllerEvent.EmployeeNoString = ""
	}

	e.recordEvent(event, currentTime)

	// Encode the event as compact JSON (real Hikvision devices don't send
	// indented JSON).
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal event: %w", err)
	}

	imageData, err := GetPhotoImageData()
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	boundary := "MIME_boundary"

	var body strings.Builder

	// First part: JSON. The Content-Disposition with name="event_log" is what
	// the client's parser uses to identify the payload as an event and decode
	// it (without it the client treats it as a generic blob).
	body.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	body.WriteString("Content-Disposition: form-data; name=\"event_log\"\r\n")
	body.WriteString("Content-Type: application/json; charset=\"UTF-8\"\r\n")
	body.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(eventJSON)))
	body.WriteString("\r\n")
	body.Write(eventJSON)
	body.WriteString("\r\n")

	// Second part: image
	body.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	body.WriteString("Content-Disposition: form-data; name=\"Picture\"; filename=\"Picture.jpg\"\r\n")
	body.WriteString("Content-Type: image/jpeg\r\n")
	body.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(imageData)))
	body.WriteString("Content-ID: pictureImage\r\n")
	body.WriteString("\r\n")
	body.Write(imageData)
	body.WriteString("\r\n")

	// NOTE: no closing boundary ("--MIME_boundary--") while streaming; it would
	// end the multipart per RFC 2046 and the client would treat it as EOF.

	return []byte(body.String()), nil
}

// getHeartbeatMessage returns a heartbeat message.
func (e *Emulator) getHeartbeatMessage() []byte {
	// Build the heartbeat message
	heartbeat := map[string]interface{}{
		"ipAddress":        e.device.IPAddress,
		"portNo":           e.device.Port,
		"protocol":         "HTTP",
		"macAddress":       e.macAddress,
		"channelID":        1,
		"dateTime":         time.Now().In(e.location()).Format("2006-01-02T15:04:05-07:00"),
		"activePostCount":  1,
		"eventType":        "videoloss",
		"eventState":       "inactive",
		"eventDescription": "videoloss alarm",
	}

	// Encode the heartbeat as JSON
	heartbeatJSON, _ := json.MarshalIndent(heartbeat, "", "  ")
	contentLength := len(heartbeatJSON)

	// Format the heartbeat as multipart (RFC 2046): each part starts with
	// "--boundary\r\n", headers, "\r\n", body, "\r\n". No closing boundary,
	// to keep the stream open.
	boundary := "MIME_boundary"
	heartbeatMsg := fmt.Sprintf("--%s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s\r\n",
		boundary, contentLength, string(heartbeatJSON))

	return []byte(heartbeatMsg)
}

// handleEventStream streams events over the raw connection hijacked in
// handleGetAlertStream. It writes multipart parts straight to the socket,
// without chunked encoding, to reproduce the real device.
func (e *Emulator) handleEventStream(conn net.Conn, bufrw *bufio.ReadWriter) {
	e.tracer.Info("[GET] /alertStream - Starting event stream")

	// Immediate initial heartbeat: streaming clients typically block until the
	// first body byte arrives; without it the stream looks "stuck" from the
	// client side.
	initial := e.getHeartbeatMessage()
	n, err := bufrw.Write(initial)
	if err != nil {
		e.tracer.Error("Failed to write initial heartbeat: %v", err)
		return
	}
	if err := bufrw.Flush(); err != nil {
		e.tracer.Error("Failed to flush initial heartbeat: %v", err)
		return
	}
	e.tracer.Info("[alertStream] initial heartbeat sent (%d bytes)", n)

	stop := e.runStop()

	heartbeat := time.NewTicker(streamHeartbeatInterval)
	defer heartbeat.Stop()

	var eventTick <-chan time.Time
	if e.device.EventInterval > 0 {
		t := time.NewTicker(time.Duration(e.device.EventInterval) * time.Second)
		defer t.Stop()
		eventTick = t.C
	}

	// Write or flush failures mean the client disconnected (the hijacked
	// equivalent of c.Request.Context().Done()).
	for {
		select {
		case <-stop:
			e.tracer.Info("Event stream stopped due to emulator shutdown")
			return

		case <-eventTick:
			eventData, err := e.generateRandomEvent()
			if err != nil {
				e.tracer.Error("Failed to generate random event: %v", err)
				continue
			}
			if eventData == nil {
				continue
			}
			if err := writeFlush(bufrw, eventData); err != nil {
				e.tracer.Error("Failed to write event data: %v (client disconnected)", err)
				return
			}
			e.dlog.StreamEvent(conn.RemoteAddr().String(),
				devicelog.KeyFields("", eventData, nil), len(eventData))
			heartbeat.Reset(streamHeartbeatInterval)

		case <-heartbeat.C:
			if err := writeFlush(bufrw, e.getHeartbeatMessage()); err != nil {
				e.tracer.Error("Failed to write heartbeat: %v (client disconnected)", err)
				return
			}
		}
	}
}

// streamHeartbeatInterval is the idle time after which alertStream sends a
// heartbeat part.
const streamHeartbeatInterval = 10 * time.Second

// writeFlush writes one part to the hijacked connection and flushes it.
func writeFlush(w *bufio.ReadWriter, data []byte) error {
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Flush()
}

func (e *Emulator) GetTotalUsers() (int, error) {
	return e.repo.GetTotalUsers()
}

// GetPhotoImageData returns the placeholder JPEG attached to events
// (assets/device/photo.jpg). The caller gets its own copy.
func GetPhotoImageData() ([]byte, error) {
	return assets.DevicePhoto(), nil
}
