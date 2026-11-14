package dahua

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
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

// Emulator emulates a Dahua access-control device.
type Emulator struct {
	device     models.Device
	tracer     *trace.Tracer
	repo       *Repository
	macAddress string

	// Lifecycle. lifeMu serializes Start and Stop and guards server and
	// stopChan. running is atomic because event generators, stream handlers
	// and the manager read it without the lock. stopChan is per run: Start
	// creates a fresh one and every goroutine of that run captures it at
	// launch (runStop).
	lifeMu   sync.Mutex
	server   *http.Server
	running  atomic.Bool
	stopChan chan struct{}

	// Remote server settings. Written by setConfig handlers and read by the
	// keep-alive and event goroutines, hence remoteMu.
	remoteMu        sync.RWMutex
	remoteServer    string
	remotePort      string
	remoteServerURL string

	// localAuth caches the LocalAuthentication setting ("1" standalone,
	// "0" online) for the stream loops, which used to read it from the
	// database on every iteration. Refreshed by refreshLocalAuth.
	localAuth atomic.Pointer[string]

	// configs keeps what the client writes via configManager.cgi, so
	// getConfig can read it back.
	configs *configStore

	// eventLog is the AccessControlCardRec history, queried through
	// recordFinder when the client collects offline events.
	eventLog *dahuaEventLog

	// countCardsFn returns the card count for getQuerySize.
	// Injectable so handlers can be tested without a database.
	countCardsFn func() (int, error)

	// pushChan carries ready-made multipart parts to be written to the open
	// stream connection. The fingerprint enroll event goes out this way: the
	// client waits for it on the stream, not on a POST to /notification.
	pushChan chan []byte

	// enrollFn fires the enroll event. Injectable for tests.
	enrollFn func(userID string)

	// dlog is this device's log (the "record event log" checkbox).
	// May be nil: every devicelog method then becomes a no-op.
	dlog *devicelog.Logger
}

// NewEmulator creates a new Dahua emulator instance.
func NewEmulator(db database.DBInterface, device models.Device, tracer *trace.Tracer, dlog *devicelog.Logger) *Emulator {
	tracer.Info("Initializing Dahua emulator model: %s v1.1", device.Name)

	repo := NewRepository(db, device.ID)
	macAddress := utils.GenerateMacAddress()

	emulator := &Emulator{
		device:     device,
		tracer:     tracer,
		repo:       repo,
		macAddress: macAddress,
		stopChan:   make(chan struct{}),
		configs:    newConfigStore(macAddress),
		eventLog:   newDahuaEventLog(500),
		pushChan:   make(chan []byte, 8),
		dlog:       dlog,
	}

	emulator.enrollFn = emulator.sendFingerprintEnrollEvent

	emulator.countCardsFn = func() (int, error) {
		counts, err := repo.CountItems()
		if err != nil {
			return 0, err
		}
		return counts.Cards, nil
	}

	// Initialize remote server settings
	emulator.initializeRemoteSettings()

	return emulator
}

// recordEvent stores the access in the local history (AccessControlCardRec),
// so recordFinder returns real events instead of the card table.
func (e *Emulator) recordEvent(cardName, cardNo string, userID int, when time.Time) {
	if e.eventLog == nil {
		return
	}
	e.eventLog.Append(CardRecEntry{
		CreateTime: when,
		CardNo:     cardNo,
		CardName:   cardName,
		UserID:     userID,
		ReaderID:   1,
		Method:     15, // EventMethod.FACE
		ErrorCode:  0,  // ErrorCodeEvents.NO_ERROR
		Status:     1,
	})
}

// keepAliveConfig reads what the client wrote to
// Intelbras_ModeCfg.KeepAlive.* via setConfig. It returns the real device's
// defaults for any empty field.
func (e *Emulator) keepAliveConfig() (bool, time.Duration, string) {
	if e.configs == nil {
		return false, 5 * time.Second, "/keepalive"
	}

	enabled := strings.EqualFold(e.configs.Get("Intelbras_ModeCfg.KeepAlive.Enable"), "true")

	interval := 5 * time.Second
	if s := e.configs.Get("Intelbras_ModeCfg.KeepAlive.Interval"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			interval = time.Duration(n) * time.Second
		}
	}

	path := e.configs.Get("Intelbras_ModeCfg.KeepAlive.Path")
	if path == "" {
		path = "/keepalive"
	}

	return enabled, interval, path
}

// startKeepAlive issues the periodic GET to the client's /keepalive endpoint
// while the emulator is running. It re-reads the configuration every tick,
// because the client toggles online mode at runtime.
func (e *Emulator) startKeepAlive(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		var last time.Time
		client := &http.Client{Timeout: 2 * time.Second}

		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				enabled, interval, path := e.keepAliveConfig()
				base := e.remoteURL()
				if !enabled || base == "" {
					continue
				}
				if now.Sub(last) < interval {
					continue
				}
				last = now

				url := base + path
				start := time.Now()
				resp, err := client.Get(url)
				status := 0
				if resp != nil {
					status = resp.StatusCode
				}
				e.dlog.Out("GET", url, "", nil, status, err, time.Since(start))
				if err != nil {
					e.tracer.Warning("[keepalive] %s failed: %v", url, err)
					continue
				}
				resp.Body.Close()
			}
		}
	}()
}

// buildStreamTextPart wraps a payload in a text/plain part of the multipart
// stream. Clients slice the body by Content-Length, so it is mandatory:
// without the header a multi-line payload gets truncated.
func buildStreamTextPart(payload []byte) []byte {
	return []byte(fmt.Sprintf("%s--myboundary%sContent-Type: text/plain%sContent-Length: %d%s%s%s%s",
		crlf, crlf, crlf, len(payload), crlf, crlf, string(payload), crlf))
}

// pushStreamEvent queues a part for the active stream connection. If no
// stream is open (or the queue is full) the event is dropped — a real device
// does not keep what nobody is listening to either.
func (e *Emulator) pushStreamEvent(part []byte) {
	if e.pushChan == nil {
		return
	}
	select {
	case e.pushChan <- part:
	default:
		e.tracer.Warning("[stream] queue full or no listener; event dropped")
	}
}

// deviceLocation is the time zone the Dahua emulator reports local times in
// (getCurrentTime, event "Time" fields): the emulator.default_timezone
// default. Unix ("UTC", "CreateTime") fields are zone-independent.
func deviceLocation() *time.Location {
	return devicetime.Default()
}

// buildEnrollEvent builds the fingerprint capture event the real device
// pushes on the stream after accessControl.cgi?action=captureFingerprint.
// The client diverts the event as soon as it finds the CollectResult key and
// reads Fingerprint to store the template.
func buildEnrollEvent(mac, userID string, success bool) *Event {
	result := success
	now := time.Now().In(deviceLocation())

	raw := make([]byte, 512)
	for i := range raw {
		raw[i] = byte((i*11 + 7) % 251)
	}

	return &Event{
		Events: []EventData{{
			Action: "Pulse",
			Code:   "FingerPrintCollect",
			Data: EventDataDetails{
				CollectResult: &result,
				UserID:        userID,
				ReaderID:      "1",
				CreateTime:    now.Unix(),
				UTC:           now.Unix(),
				Fingerprint:   base64.StdEncoding.EncodeToString(raw),
			},
			Index:           0,
			PhysicalAddress: mac,
		}},
		Time: now.Format("2006-01-02 15:04:05"),
	}
}

// sendFingerprintEnrollEvent emits the enroll event after a short delay,
// simulating the time to place the finger on the sensor. The client blocks
// for up to 70s waiting for it.
func (e *Emulator) sendFingerprintEnrollEvent(userID string) {
	go func() {
		time.Sleep(2 * time.Second)

		body, err := json.Marshal(buildEnrollEvent(e.macAddress, userID, true))
		if err != nil {
			e.tracer.Error("[enroll] marshal: %v", err)
			return
		}
		e.tracer.Info("[enroll] emitting capture event for UserID=%s", userID)
		e.pushStreamEvent(buildStreamTextPart(body))
	}()
}

// initializeRemoteSettings loads the remote server settings.
func (e *Emulator) initializeRemoteSettings() {
	e.remoteMu.Lock()
	defer e.remoteMu.Unlock()

	if server, err := e.repo.GetSetting("RemoteServer"); err == nil && server != "" {
		e.remoteServer = server
	} else {
		e.remoteServer = "localhost"
	}

	if port, err := e.repo.GetSetting("RemotePort"); err == nil && port != "" {
		e.remotePort = port
	} else {
		e.remotePort = models.DefaultEventServerPort
	}

	e.remoteServerURL = fmt.Sprintf("http://%s:%s", e.remoteServer, e.remotePort)
	e.tracer.Info("Remote server URL: %s", e.remoteServerURL)
}

// remoteURL returns the base URL of the event server ("http://host:port").
func (e *Emulator) remoteURL() string {
	e.remoteMu.RLock()
	defer e.remoteMu.RUnlock()
	return e.remoteServerURL
}

// setRemote updates the event server address; empty values keep the current
// one.
func (e *Emulator) setRemote(server, port string) {
	e.remoteMu.Lock()
	defer e.remoteMu.Unlock()
	if server != "" {
		e.remoteServer = server
	}
	if port != "" {
		e.remotePort = port
	}
	e.remoteServerURL = fmt.Sprintf("http://%s:%s", e.remoteServer, e.remotePort)
}

// refreshLocalAuth re-reads LocalAuthentication from the database into the
// cache the stream loops use. On a read error the cached value is kept.
func (e *Emulator) refreshLocalAuth() {
	if e.repo == nil {
		return
	}
	v, err := e.repo.GetSetting("LocalAuthentication")
	if err != nil {
		e.tracer.Warning("Failed to read LocalAuthentication: %v", err)
		return
	}
	e.localAuth.Store(&v)
}

// cachedLocalAuth returns the cached LocalAuthentication value, reading it
// once if nothing is cached yet. "" means unknown.
func (e *Emulator) cachedLocalAuth() string {
	if v := e.localAuth.Load(); v != nil {
		return *v
	}
	e.refreshLocalAuth()
	if v := e.localAuth.Load(); v != nil {
		return *v
	}
	return ""
}

// newRouter builds the emulator router with the diagnostic middlewares.
func (e *Emulator) newRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	// The log sits outside Recovery so it records the 500 of a panicking
	// handler.
	router.Use(e.dlog.Gin())
	router.Use(gin.Recovery())

	// Diagnostic middleware: logs every request reaching this emulator
	// (same [REQ]/[RES] pair as Hikvision).
	router.Use(func(c *gin.Context) {
		e.tracer.Info("[REQ] %s %s?%s from %s (CL=%d CT=%q)",
			c.Request.Method, c.Request.URL.Path, c.Request.URL.RawQuery,
			c.Request.RemoteAddr, c.Request.ContentLength,
			c.Request.Header.Get("Content-Type"))
		c.Next()
		e.tracer.Info("[RES] %s %s -> %d (%d bytes)",
			c.Request.Method, c.Request.URL.Path, c.Writer.Status(), c.Writer.Size())
	})

	// NoRoute: without this, an endpoint the client calls but the emulator
	// does not implement becomes a silent 404.
	router.NoRoute(func(c *gin.Context) {
		e.tracer.Error("[NOROUTE] %s %s?%s not matched",
			c.Request.Method, c.Request.URL.Path, c.Request.URL.RawQuery)
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

	e.tracer.Info("Starting Dahua emulator: %s", e.device.Name)

	ln, err := utils.ListenPort(e.device.Port)
	if err != nil {
		return err
	}

	e.tracer.Info("Dahua HTTP server listening on %s (device: %s)", ln.Addr(), e.device.IPAddress)
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
	e.running.Store(true)
	e.dlog.Printf("START", "emulator %q on port %d", e.device.Name, e.device.Port)

	go e.serve(srv, ln, stop)

	if e.device.EventInterval > 0 {
		go e.startEventGenerator(stop)
	}

	// Keepalive: only actually fires once the client enables
	// Intelbras_ModeCfg.KeepAlive.Enable via setConfig.
	e.startKeepAlive(stop)

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

	e.tracer.Error("Dahua server for %s exited unexpectedly: %v", e.device.Name, err)
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

	e.tracer.Info("Stopping Dahua emulator: %s", e.device.Name)

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

	// Refresh user counts
	if count, err := e.repo.GetTotalUsers(); err == nil {
		device.TotalUsers = count
	}

	return device
}

// GetType returns the emulator type.
func (e *Emulator) GetType() string {
	return "Dahua"
}

// GetStatus returns the current emulator status.
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

func (e *Emulator) generateRandomEventParts() ([]byte, []byte, error) {
	// Only standalone devices ("1") emit events on the stream. The value is
	// cached; the stream loop refreshes it.
	if e.cachedLocalAuth() != "1" {
		return nil, nil, nil
	}

	// Fetch a random card
	cardName, cardNo, userID, err := e.repo.GetRandomCard()
	if err != nil {
		e.tracer.Error("Failed to get random card: %v", err)
		return nil, nil, fmt.Errorf("failed to get random card: %w", err)
	}
	e.tracer.Info("Random card fetched - CardNumber: %s, CardName: %s, UserID: %d", cardNo, cardName, userID)

	// Record in the local history: recordFinder serves it when the client
	// scans AccessControlCardRec after an offline period.
	e.recordEvent(cardName, cardNo, userID, time.Now())

	// Build the event in Dahua format (plain text with \r\n, like the real device)
	eventText := fmt.Sprintf("Events[0].Alive=100\r\n"+
		"Events[0].CardName=%s\r\n"+
		"Events[0].CardNo=%s\r\n"+
		"Events[0].CardType=0\r\n"+
		"Events[0].CreateTime=%d\r\n"+
		"Events[0].Door=0\r\n"+
		"Events[0].ErrorCode=0\r\n"+
		"Events[0].EventBaseInfo.Action=Pulse\r\n"+
		"Events[0].EventBaseInfo.Code=AccessControl\r\n"+
		"Events[0].EventBaseInfo.Index=0\r\n"+
		"Events[0].FaceIndex=0\r\n"+
		"Events[0].ImageInfo[0].Height=384\r\n"+
		"Events[0].ImageInfo[0].Length=15225\r\n"+
		"Events[0].ImageInfo[0].Offset=0\r\n"+
		"Events[0].ImageInfo[0].Type=1\r\n"+
		"Events[0].ImageInfo[0].Width=640\r\n"+
		"Events[0].ImageInfo[1].Height=420\r\n"+
		"Events[0].ImageInfo[1].Length=21710\r\n"+
		"Events[0].ImageInfo[1].Offset=15225\r\n"+
		"Events[0].ImageInfo[1].Type=0\r\n"+
		"Events[0].ImageInfo[1].Width=360\r\n"+
		"Events[0].ImageInfo[2].Height=608\r\n"+
		"Events[0].ImageInfo[2].Length=22531\r\n"+
		"Events[0].ImageInfo[2].Offset=36935\r\n"+
		"Events[0].ImageInfo[2].Type=2\r\n"+
		"Events[0].ImageInfo[2].Width=480\r\n"+
		"Events[0].Method=15\r\n"+
		"Events[0].ReaderID=1\r\n"+
		"Events[0].RealUTC=%d\r\n"+
		"Events[0].Similarity=99\r\n"+
		"Events[0].SnapPath=/var/tmp/white_part5.jpg\r\n"+
		"Events[0].Status=1\r\n"+
		"Events[0].Type=Entry\r\n"+
		"Events[0].UTC=%d\r\n"+
		"Events[0].UserID=%d\r\n"+
		"Events[0].UserType=0\r\n",
		cardName, cardNo, time.Now().Unix(), time.Now().Unix(), time.Now().Unix(), userID)

	boundary := "myboundary"

	// Decode the image
	imageData, err := GetPhotoImageData()
	if err != nil {
		e.tracer.Error("[DEBUG] Failed to decode image: %v", err)
		return nil, nil, fmt.Errorf("failed to decode image: %w", err)
	}
	e.tracer.Info("Photo image decoded, size: %d bytes", len(imageData))

	// Same format as a real Dahua device:
	// --myboundary\r\n
	// Content-Type: text/plain\r\n
	// Content-Length: XXX\r\n
	// \r\n
	// [content]\r\n
	// \r\n
	textPart := []byte(fmt.Sprintf("\r\n--%s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s\r\n",
		boundary, len(eventText), eventText))

	// Image part - same format as the real device:
	// --myboundary\r\n
	// Content-Type: image/jpeg\r\n
	// Content-Length: XXX\r\n
	// \r\n
	// [binary image data]
	imageHeader := fmt.Sprintf("--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n",
		boundary, len(imageData))
	imagePart := append([]byte(imageHeader), imageData...)

	e.tracer.Info("=== generateRandomEventParts COMPLETE ===")
	e.tracer.Info("Returning 2 parts: text(%d bytes) + image(%d bytes)",
		len(textPart), len(imagePart))

	return textPart, imagePart, nil
}

// generateOnlineEvent builds an online event and sends it to the remote server.
func (e *Emulator) generateOnlineEvent() error {
	e.tracer.Info("Generating online event")

	// Fetch a random card with all its data
	cardName, cardNo, userID, err := e.repo.GetRandomCard()
	if err != nil {
		e.tracer.Warning("No card found for event generation: %v", err)
		return nil
	}

	currentTime := time.Now().In(deviceLocation())
	e.recordEvent(cardName, cardNo, userID, currentTime)
	unixTime := currentTime.Unix()

	// Decode the image to get its real size
	imageData, err := GetPhotoImageData()
	if err != nil {
		return fmt.Errorf("failed to decode image: %w", err)
	}
	imageLength := len(imageData)

	// Build the event in the same format as a real Dahua device
	event := &OnlineEvent{
		Channel: 0,
		Events: []EventData{
			{
				Action: "Pulse",
				Code:   "AccessControl",
				Data: EventDataDetails{
					Alive:      100,
					BlockId:    27,
					CardName:   cardName,
					CardNo:     cardNo,
					CardType:   0,
					CreateTime: unixTime,
					Door:       0,
					ErrorCode:  0,
					FaceIndex:  0,
					FeatureId:  4,
					HatColor:   "Other",
					HatType:    0,
					ImageInfo: []ImageInfo{
						{
							Height: 640,
							Length: imageLength,
							Offset: 0,
							Type:   1,
							Width:  384,
						},
					},
					Method: 15,
					ObjectProperties: &ObjectProperties{
						HatInfo: &HatInfo{
							HatColor: "Other",
							HatType:  0,
						},
					},
					ReaderID:   "1",
					RealUTC:    unixTime,
					Similarity: 99,
					SnapPath:   "/var/tmp/white_part3.jpg",
					Status:     1,
					Type:       "Entry",
					UTC:        unixTime,
					UserID:     fmt.Sprintf("%d", userID),
					UserType:   0,
				},
				Index:           0,
				PhysicalAddress: e.macAddress,
			},
		},
		FilePath: fmt.Sprintf("/SnapShotFilePath/%s/%d_99_100_%s.jpg",
			currentTime.Format("2006-01-02/15/04"),
			userID,
			currentTime.Format("20060102150405000")),
		Time: currentTime.Format("2006-01-02 15:04:05"),
	}

	// Encode with 3-space indentation (like the real device)
	eventJSON, err := json.MarshalIndent(event, "", "   ")
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// Build the multipart message in the real Dahua device format
	boundary := "myboundary"

	// Part 1: event JSON
	textPart := fmt.Sprintf("--%s\r\nContent-Type: text/plain\r\nContent-Disposition: form-data; name=\"info\"\r\nContent-Length: %d\r\n\r\n%s\r\n",
		boundary, len(eventJSON), string(eventJSON))

	// Part 2: JPEG image
	imagePart := fmt.Sprintf("--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n",
		boundary, imageLength)

	// Final part: closing boundary
	closingPart := fmt.Sprintf("\r\n--%s--\r\n", boundary)

	// Compute the total Content-Length
	totalLength := len(textPart) + len(imagePart) + imageLength + len(closingPart)

	// Send the complete event
	if err := e.sendOnlineEventToServer(textPart, imagePart, imageData, closingPart, totalLength); err != nil {
		return fmt.Errorf("failed to send online event: %w", err)
	}

	// Randomly simulate door events
	if utils.RandomAccessNotDone() {
		e.tracer.Info("Sending door events to complete access")
		go e.simulateDoorEvents()
	}

	return nil
}

// sendOnlineEventToServer posts an online event to the remote server in Dahua format.
func (e *Emulator) sendOnlineEventToServer(textPart, imagePart string, imageData []byte, closingPart string, totalLength int) error {
	remoteURL := e.remoteURL() + "/notification"
	e.tracer.Info("Sending online event to server: %s (total size: %d bytes)", remoteURL, totalLength)

	var body bytes.Buffer
	body.WriteString(textPart)
	body.WriteString(imagePart)
	body.Write(imageData)
	body.WriteString(closingPart)

	if err := e.postNotification(remoteURL, body.Bytes(), 10*time.Second); err != nil {
		return err
	}
	e.tracer.Info("Online event sent successfully to %s", remoteURL)
	return nil
}

// simulateDoorEvents simulates door open/close events.
func (e *Emulator) simulateDoorEvents() {
	time.Sleep(2 * time.Second)
	if err := e.sendDoorEvent("Open"); err != nil {
		e.tracer.Error("Failed to send door open event: %v", err)
	}

	time.Sleep(3 * time.Second)
	if err := e.sendDoorEvent("Close"); err != nil {
		e.tracer.Error("Failed to send door close event: %v", err)
	}
}

// sendDoorEvent sends a door state event.
func (e *Emulator) sendDoorEvent(status string) error {
	currentTime := time.Now().In(deviceLocation())
	event := map[string]interface{}{
		"Events": []map[string]interface{}{
			{
				"Action": "Pulse",
				"Code":   "DoorStatus",
				"Data": map[string]interface{}{
					"Status": status,
					"UTC":    currentTime.Unix(),
				},
				"Index":           0,
				"PhysicalAddress": e.macAddress,
			},
		},
		"Time": currentTime.Format("02-01-2006 15:04:05"),
	}

	// Encode the event as JSON
	eventJSON, err := json.MarshalIndent(event, "", "   ")
	if err != nil {
		return fmt.Errorf("failed to marshal door event: %w", err)
	}

	// Format the event as multipart
	boundary := "myboundary"
	textPart := fmt.Sprintf("--%s\r\nContent-Type: text/plain\r\nContent-Disposition: form-data; name=\"info\"\r\nContent-Length: %d\r\n\r\n%s\r\n--%s--\r\n",
		boundary, len(eventJSON), string(eventJSON), boundary)

	// Send the event to the remote server
	return e.sendTextEventToServer(textPart)
}

// sendTextEventToServer posts a plain text event (no image) to the remote server.
func (e *Emulator) sendTextEventToServer(body string) error {
	remoteURL := e.remoteURL() + "/notification"
	e.tracer.Info("Sending text event to server: %s", remoteURL)

	if err := e.postNotification(remoteURL, []byte(body), 5*time.Second); err != nil {
		return err
	}
	e.tracer.Info("Text event sent successfully to %s", remoteURL)
	return nil
}

// postNotification performs POST /notification with the real device's
// headers and records the outbound call in the device log.
func (e *Emulator) postNotification(remoteURL string, payload []byte, timeout time.Duration) error {
	const contentType = "multipart/mixed; boundary=myboundary"
	req, err := http.NewRequest("POST", remoteURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(payload)))

	start := time.Now()
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
		defer resp.Body.Close()
	}
	e.dlog.Out("POST", remoteURL, contentType, payload, status, err, time.Since(start))

	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected response: %s", resp.Status)
	}
	return nil
}

func (e *Emulator) GetTotalUsers() (int, error) {
	return e.repo.GetTotalUsers()
}

// GetPhotoImageData returns the placeholder JPEG attached to events
// (assets/device/photo.jpg). The caller gets its own copy.
func GetPhotoImageData() ([]byte, error) {
	return assets.DevicePhoto(), nil
}
