package handlers

import (
	"context"
	"html/template"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/rphpires/facial-device-emulator/assets"
	"github.com/rphpires/facial-device-emulator/internal/database"
	"github.com/rphpires/facial-device-emulator/internal/devicelog"
	"github.com/rphpires/facial-device-emulator/internal/emulator"
	"github.com/rphpires/facial-device-emulator/internal/models"
	"github.com/rphpires/facial-device-emulator/internal/monitoring"
	"github.com/rphpires/facial-device-emulator/internal/reachability"
	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/gin-gonic/gin"
)

// Handler serves every HTTP route: the web console, the REST API and the
// monitoring endpoints.
type Handler struct {
	manager       *emulator.Manager
	batch         batchBackend // the manager in production; a fake in tests
	serviceDB     database.DBInterface
	appVersion    string
	tracer        *trace.Tracer
	healthMonitor *monitoring.HealthMonitor
	metrics       *monitoring.Metrics
	templates     map[string]*template.Template
	env           reachability.Environment
	logs          *devicelog.Registry
}

// NewHandler wires the handler to the emulator manager and the service
// database, and registers the health checkers.
func NewHandler(manager *emulator.Manager, serviceDB database.DBInterface, appVersion string, tracer *trace.Tracer) *Handler {
	healthMonitor := monitoring.NewHealthMonitor()
	metrics := monitoring.NewMetrics()

	healthMonitor.RegisterChecker(monitoring.NewDatabaseHealthChecker(
		"service_db",
		func(ctx context.Context) error { return serviceDB.Ping(ctx) },
		func() map[string]interface{} { return poolStats(serviceDB) },
	))

	healthMonitor.RegisterChecker(monitoring.NewDatabaseHealthChecker(
		"emulator_db",
		func(ctx context.Context) error { return manager.EmulatorDB.Ping(ctx) },
		func() map[string]interface{} { return poolStats(manager.EmulatorDB) },
	))

	healthMonitor.RegisterChecker(monitoring.NewEmulatorHealthChecker(
		func() ([]map[string]interface{}, error) {
			devices, err := manager.ListDevices()
			if err != nil {
				return nil, err
			}
			result := make([]map[string]interface{}, len(devices))
			for i, d := range devices {
				result[i] = map[string]interface{}{
					"id":     d.ID,
					"status": d.Status,
					"name":   d.Name,
				}
			}
			return result, nil
		},
	))

	return &Handler{
		manager:       manager,
		batch:         manager,
		logs:          manager.Logs,
		serviceDB:     serviceDB,
		appVersion:    appVersion,
		tracer:        tracer,
		healthMonitor: healthMonitor,
		metrics:       metrics,
		templates:     buildTemplateCache(),
		env:           reachability.Detect(),
	}
}

// poolStats returns the statistics of whichever pool implementation backs db.
func poolStats(db database.DBInterface) map[string]interface{} {
	switch p := db.(type) {
	case *database.DualPoolManager:
		return p.GetStats()
	case *database.AdaptivePool:
		return p.GetStats()
	default:
		return map[string]interface{}{"status": "unknown"}
	}
}

func (h *Handler) withBaseContext(extra gin.H) gin.H {
	context := gin.H{
		"app_version": h.appVersion,
	}

	for key, value := range extra {
		context[key] = value
	}

	return context
}

// Router builds the HTTP router with every route and middleware.
func (h *Handler) Router() http.Handler {
	router := gin.New()

	// Order matters: recovery wraps everything, metrics see every request.
	router.Use(monitoring.RecoveryMiddleware(h.metrics, h.tracer))
	router.Use(monitoring.MetricsMiddleware(h.metrics, h.tracer))
	router.Use(h.corsMiddleware())

	// Static files are embedded in the binary.
	router.StaticFS("/static", http.FS(assets.StaticFS()))

	h.setupWebRoutes(router)
	h.setupAPIRoutes(router)
	h.setupMonitoringRoutes(router)

	return router
}

// setupWebRoutes registers the web console routes.
func (h *Handler) setupWebRoutes(router *gin.Engine) {
	router.GET("/", h.mainPage)
	router.GET("/events", h.handleStream)
}

// setupMonitoringRoutes registers the monitoring and diagnostics routes.
func (h *Handler) setupMonitoringRoutes(router *gin.Engine) {
	monitoring := router.Group("/monitoring")
	{
		monitoring.GET("/health", h.advancedHealthCheck)
		monitoring.GET("/health/quick", h.quickHealthCheck)

		monitoring.GET("/metrics", h.getMetrics)
		monitoring.GET("/metrics/endpoints", h.getEndpointMetrics)
		monitoring.GET("/metrics/errors", h.getTopErrors)
		monitoring.GET("/metrics/database", h.getDatabaseMetrics)

		monitoring.GET("/debug/goroutines", h.getGoroutineCount)
		monitoring.GET("/debug/memory", h.getMemoryStats)
	}
}

// setupAPIRoutes registers the REST API.
func (h *Handler) setupAPIRoutes(router *gin.Engine) {
	api := router.Group("/api")
	{
		emulators := api.Group("/emulators")
		{
			// Whole-fleet control.
			emulators.POST("/start", h.apiStartEmulators)
			emulators.POST("/stop", h.apiStopEmulators)

			// Batch actions on an explicit list of IDs.
			emulators.POST("/batch", h.apiBatchEmulators)

			// Emulator CRUD. /api/devices stays for per-device control
			// (start/stop/settings/mode).
			emulators.GET("", h.apiListEmulators)
			emulators.POST("", h.apiCreateEmulator)
			emulators.POST("/range", h.apiCreateEmulatorRange)
			emulators.PUT("/:id", h.apiUpdateEmulator)
			emulators.DELETE("/:id", h.apiDeleteEmulator)
		}

		devices := api.Group("/devices")
		{
			devices.GET("", h.listDevices)
			devices.GET("/:id", h.getDevice)
			devices.POST("/:id/start", h.startSingleDevice)
			devices.POST("/:id/stop", h.stopSingleDevice)
			devices.PUT("/:id/settings", h.updateDeviceSettings)
			devices.GET("/:id/users", h.getDeviceUsers)
			devices.GET("/:id/settings", h.getDeviceSettings)
			devices.GET("/:id/log", h.getDeviceLog)
			devices.GET("/:id/log.zip", h.getDeviceLogZip)
			devices.GET("/:id/mode", h.apiGetDeviceMode)
			devices.POST("/:id/mode", h.apiSetDeviceMode)
		}

		api.GET("/status", h.getSystemStatus)
		api.GET("/pool-stats", h.getPoolStats)
		api.GET("/reachability", h.getReachability)
	}
}

func (h *Handler) corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// ====================================================================
// Web console
// ====================================================================

// mainPage renders the devices page. The table itself is drawn in the
// browser from the fleet state the event stream keeps current; the server
// only embeds the current fleet so the first paint does not wait for the
// SSE snapshot. Filtering, sorting and pagination all happen client-side.
func (h *Handler) mainPage(c *gin.Context) {
	views, counts, err := h.fleetViews(c.Request.Context())
	if err != nil {
		h.tracer.Error("Failed to get current devices: %v", err)
		h.renderPage(c, "error.html", http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.renderPage(c, "devices.html", http.StatusOK, gin.H{
		"fleet":         views,
		"counter_cards": counts.toMap(),
	})
}

// ====================================================================
// Whole-fleet control
// ====================================================================

// apiStartEmulators starts every enabled emulator.
//
// By default the work runs in the background and the call answers 202:
// starting a large fleet can take minutes, and progress is visible on the
// event stream and on GET /api/status. Pass ?wait=true to block until done.
func (h *Handler) apiStartEmulators(c *gin.Context) {
	h.tracer.Info("Starting all emulators")

	if c.Query("wait") == "true" {
		if err := h.manager.StartAll(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "started"})
		return
	}

	go func() {
		if err := h.manager.StartAll(); err != nil {
			h.tracer.Error("StartAll: %v", err)
		}
	}()
	c.JSON(http.StatusAccepted, gin.H{"status": "starting"})
}

// apiStopEmulators stops every running emulator. Same ?wait=true contract
// as apiStartEmulators.
func (h *Handler) apiStopEmulators(c *gin.Context) {
	h.tracer.Info("Stopping all emulators")

	if c.Query("wait") == "true" {
		h.manager.StopAll()
		c.JSON(http.StatusOK, gin.H{"status": "stopped"})
		return
	}

	go h.manager.StopAll()
	c.JSON(http.StatusAccepted, gin.H{"status": "stopping"})
}

// ====================================================================
// Devices
// ====================================================================

// listDevices lists every device.
func (h *Handler) listDevices(c *gin.Context) {
	devices, err := h.manager.ListDevices()
	if err != nil {
		h.tracer.Error("Failed to list devices: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve devices"})
		return
	}
	if devices == nil {
		devices = []models.Device{}
	}

	c.JSON(http.StatusOK, gin.H{
		"devices": devices,
		"count":   len(devices),
	})
}

// getDevice returns one device.
func (h *Handler) getDevice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	device, err := h.manager.GetDevice(id)
	if err != nil {
		h.writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, device)
}

// startSingleDevice starts one device.
func (h *Handler) startSingleDevice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	if err := h.manager.Start(id); err != nil {
		h.tracer.Error("Failed to start device %d: %v", id, err)
		h.writeLifecycleError(c, err)
		return
	}

	h.tracer.Info("Device %d started", id)
	c.JSON(http.StatusOK, gin.H{"message": "Device started"})
}

// stopSingleDevice stops one device.
func (h *Handler) stopSingleDevice(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	if err := h.manager.Stop(id); err != nil {
		h.tracer.Error("Failed to stop device %d: %v", id, err)
		h.writeLifecycleError(c, err)
		return
	}

	h.tracer.Info("Device %d stopped", id)
	c.JSON(http.StatusOK, gin.H{"message": "Device stopped"})
}

// writeLifecycleError answers a failed start or stop: 404 for an unknown
// device, 409 for a state conflict (already running, not running, disabled,
// port taken), 500 otherwise. Unlike writeError it keeps the message on a
// 500: a start timeout or bind failure is what the operator needs to see,
// and it carries no driver internals.
func (h *Handler) writeLifecycleError(c *gin.Context, err error) {
	c.JSON(statusForError(err), gin.H{"error": err.Error()})
}

// updateDeviceSettings toggles the per-device event log. It applies
// immediately, whether the emulator is running or not.
func (h *Handler) updateDeviceSettings(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	var request struct {
		LogEnabled bool `json:"log_enabled"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if err := h.manager.UpdateDeviceSettings(id, request.LogEnabled); err != nil {
		if statusForError(err) == http.StatusNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		h.tracer.Error("Failed to update settings of device %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Settings updated"})
}

// getSystemStatus returns the fleet counters.
func (h *Handler) getSystemStatus(c *gin.Context) {
	devices, err := h.manager.ListDevices()
	if err != nil {
		h.tracer.Error("Failed to get system status: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve system status"})
		return
	}

	counts := countFleet(devices)

	c.JSON(http.StatusOK, gin.H{
		"total_devices":    counts.Total,
		"running_devices":  counts.Running,
		"stopped_devices":  counts.Stopped,
		"disabled_devices": counts.Disabled,
		"timestamp":        time.Now().UTC(),
	})
}

func (h *Handler) getPoolStats(c *gin.Context) {
	stats := h.manager.GetPoolStats()
	c.JSON(http.StatusOK, stats)
}

// ====================================================================
// Monitoring and observability
// ====================================================================

// advancedHealthCheck runs every health checker and reports the result.
func (h *Handler) advancedHealthCheck(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	result := h.healthMonitor.CheckAll(ctx)

	// Degraded still answers 200: an optional component being slow must not
	// make start scripts and probes report the service as down.
	status := result["status"].(monitoring.HealthStatus)
	httpStatus := http.StatusOK
	if status == monitoring.HealthStatusUnhealthy {
		httpStatus = http.StatusServiceUnavailable
	}

	c.JSON(httpStatus, result)
}

// quickHealthCheck returns the cached health result.
func (h *Handler) quickHealthCheck(c *gin.Context) {
	result := h.healthMonitor.QuickCheck()

	status := result["status"].(monitoring.HealthStatus)
	httpStatus := http.StatusOK
	if status == monitoring.HealthStatusUnhealthy {
		httpStatus = http.StatusServiceUnavailable
	}

	c.JSON(httpStatus, result)
}

// getMetrics returns a full metrics snapshot.
func (h *Handler) getMetrics(c *gin.Context) {
	snapshot := h.metrics.GetSnapshot()
	c.JSON(http.StatusOK, snapshot)
}

// getEndpointMetrics returns per-endpoint metrics.
func (h *Handler) getEndpointMetrics(c *gin.Context) {
	snapshot := h.metrics.GetSnapshot()
	endpoints := snapshot["endpoints"]

	c.JSON(http.StatusOK, gin.H{
		"endpoints": endpoints,
		"timestamp": time.Now().UTC(),
	})
}

// getTopErrors returns the endpoints with the most errors.
func (h *Handler) getTopErrors(c *gin.Context) {
	limit := 10
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	topErrors := h.metrics.GetTopErrorEndpoints(limit)

	c.JSON(http.StatusOK, gin.H{
		"top_errors": topErrors,
		"limit":      limit,
		"timestamp":  time.Now().UTC(),
	})
}

// getGoroutineCount returns the number of live goroutines.
func (h *Handler) getGoroutineCount(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"goroutines": runtime.NumGoroutine(),
		"timestamp":  time.Now().UTC(),
	})
}

// getMemoryStats returns Go runtime memory statistics.
func (h *Handler) getMemoryStats(c *gin.Context) {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	c.JSON(http.StatusOK, gin.H{
		"alloc_mb":       memStats.Alloc / 1024 / 1024,
		"total_alloc_mb": memStats.TotalAlloc / 1024 / 1024,
		"sys_mb":         memStats.Sys / 1024 / 1024,
		"num_gc":         memStats.NumGC,
		"gc_pause_ms":    float64(memStats.PauseNs[(memStats.NumGC+255)%256]) / 1000000,
		"heap_alloc_mb":  memStats.HeapAlloc / 1024 / 1024,
		"heap_sys_mb":    memStats.HeapSys / 1024 / 1024,
		"heap_idle_mb":   memStats.HeapIdle / 1024 / 1024,
		"heap_inuse_mb":  memStats.HeapInuse / 1024 / 1024,
		"timestamp":      time.Now().UTC(),
	})
}

// getDatabaseMetrics returns the database pool statistics.
func (h *Handler) getDatabaseMetrics(c *gin.Context) {
	stats := h.manager.GetPoolStats()
	c.JSON(http.StatusOK, stats)
}
