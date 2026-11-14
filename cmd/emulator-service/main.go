package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rphpires/facial-device-emulator/assets"
	"github.com/rphpires/facial-device-emulator/internal/config"
	"github.com/rphpires/facial-device-emulator/internal/database"
	"github.com/rphpires/facial-device-emulator/internal/devicetime"
	"github.com/rphpires/facial-device-emulator/internal/emulator"
	"github.com/rphpires/facial-device-emulator/internal/handlers"
	"github.com/rphpires/facial-device-emulator/internal/trace"
)

// httpShutdownTimeout bounds how long shutdown waits for in-flight HTTP
// requests. Together with stopping the devices it must fit Docker's stop
// grace period (10 s by default, 20 s in the compose files), or the
// container is killed before the device listeners and pools are closed.
const httpShutdownTimeout = 5 * time.Second

func main() {
	configPath := flag.String("config", "configs/config.yaml", "Path to config file")
	resetDB := flag.Bool("reset-db", false,
		"If the database structure does not match, DROP the service and emulator schemas (all their data) and recreate them")
	flag.Parse()

	tracer := trace.NewTracer()
	tracer.SetDefault()

	fatal := func(format string, args ...interface{}) {
		tracer.Error(format, args...)
		tracer.Close()
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("Failed to load config %s: %v", *configPath, err)
	}
	if err := tracer.Configure(trace.Options{
		Level:     cfg.Log.Level,
		Format:    cfg.Log.Format,
		File:      cfg.Log.File,
		MaxSizeMB: cfg.Log.MaxSizeMB,
		MaxFiles:  cfg.Log.MaxFiles,
	}); err != nil {
		fatal("Invalid log configuration: %v", err)
	}
	// The standard "log" package now writes through the same handler.
	tracer.SetDefault()

	tracer.Info("Starting Facial Emulator Service")
	tracer.Info("Using config file: %s", *configPath)

	if err := devicetime.SetDefaultName(cfg.Emulator.DefaultTimezone); err != nil {
		fatal("Invalid emulator.default_timezone: %v", err)
	}
	tracer.Info("Default device time zone: %s", devicetime.Default())

	if err := database.ValidateDatabaseOnStartup(cfg.ServiceDB, *resetDB); err != nil {
		fatal("Database check failed: %v", err)
	}

	tracer.Info("Initializing database connections...")
	serviceDB, err := database.GetServiceDB(cfg.ServiceDB)
	if err != nil {
		fatal("Failed to get ServiceDB: %v", err)
	}

	tracer.Info("Applying schema migrations...")
	migFS, err := assets.MigrationFiles()
	if err != nil {
		fatal("Failed to open embedded migrations: %v", err)
	}
	migCtx, migCancel := context.WithTimeout(context.Background(), 60*time.Second)
	err = database.ApplyMigrations(migCtx, serviceDB, migFS)
	migCancel()
	if err != nil {
		// A half-migrated database is worse than a service that won't start.
		fatal("Failed to apply migrations: %v", err)
	}
	tracer.Info("Schema migrations up to date")

	emulatorDB, err := database.GetEmulatorDB(cfg.EmulatorDB)
	if err != nil {
		fatal("Failed to get EmulatorDB: %v", err)
	}

	tracer.Info("Testing database connections...")
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := serviceDB.Ping(pingCtx); err != nil {
		fatal("ServiceDB connection test failed: %v", err)
	}
	if err := emulatorDB.Ping(pingCtx); err != nil {
		fatal("EmulatorDB connection test failed: %v", err)
	}
	pingCancel()

	manager := emulator.NewManager(serviceDB, emulatorDB, tracer)
	manager.ServicePort = cfg.Server.Port
	if err := manager.Initialize(); err != nil {
		fatal("Failed to initialize emulator manager: %v", err)
	}

	// Release mode unless GIN_MODE says otherwise: no per-route debug dump at startup.
	if os.Getenv(gin.EnvGinMode) == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	handler := handlers.NewHandler(manager, serviceDB, cfg.AppVersion, tracer)

	// Every request context derives from baseCtx, canceled as soon as
	// shutdown starts. Long-lived requests (the /events SSE stream) watch
	// their context and return, instead of holding Shutdown until its
	// timeout.
	baseCtx, cancelBase := context.WithCancel(context.Background())

	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:           handler.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Must outlast the batch/range handlers' own budget; see
		// handlers.ServerWriteTimeout. The SSE stream clears it.
		WriteTimeout: handlers.ServerWriteTimeout,
		IdleTimeout:  60 * time.Second,
		BaseContext:  func(net.Listener) context.Context { return baseCtx },
	}
	server.RegisterOnShutdown(cancelBase)

	serverErr := make(chan error, 1)
	go func() {
		tracer.Info("Starting HTTP server on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	exitCode := 0
	select {
	case sig := <-quit:
		tracer.Info("Received %v, shutting down...", sig)
	case err := <-serverErr:
		tracer.Error("HTTP server failed: %v", err)
		exitCode = 1
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	if err := server.Shutdown(shutdownCtx); err != nil {
		tracer.Warning("HTTP server did not drain in %v, closing remaining connections: %v", httpShutdownTimeout, err)
		_ = server.Close()
	}
	shutdownCancel()
	cancelBase()

	manager.Shutdown()
	database.CloseAll()

	tracer.Info("Server exited")
	tracer.Close()
	os.Exit(exitCode)
}
