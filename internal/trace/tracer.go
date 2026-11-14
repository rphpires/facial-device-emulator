// Package trace is the service log. It is a thin layer over log/slog: one
// process-wide logger, text or JSON, written to stdout and optionally to a
// size-rotated file.
//
// The printf-style methods (Info, Warning, Error, Debug) exist so the many
// call sites written as tracer.Info("device %d started", id) keep working.
// New code that wants structured attributes can use Logger() directly.
package trace

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// Options configures the service log.
type Options struct {
	// Level is debug, info, warn or error. Empty means info.
	Level string
	// Format is text or json. Empty means text.
	Format string
	// File, when set, also writes the log to this path, rotated by size.
	File string
	// MaxSizeMB is the size at which File is rotated. Zero means 10.
	MaxSizeMB int
	// MaxFiles is how many rotated files are kept besides File. Zero means 5.
	MaxFiles int
	// Output replaces stdout. Tests use it; nil means os.Stdout.
	Output io.Writer
}

const (
	defaultMaxSizeMB = 10
	defaultMaxFiles  = 5
)

// ParseLevel maps a level name to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// ValidFormat reports whether s is a supported log format.
func ValidFormat(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text", "json":
		return true
	}
	return false
}

// Tracer is the service logger. Get one with NewTracer (the process-wide
// instance) or New (a standalone one).
type Tracer struct {
	logger atomic.Pointer[slog.Logger]

	mu   sync.Mutex
	file *rotatingFile
}

var (
	instance *Tracer
	once     sync.Once
)

// NewTracer returns the process-wide tracer. Until Configure is called it
// logs text at info level to stdout and touches no file, so tests that use
// it never write into the source tree.
func NewTracer() *Tracer {
	once.Do(func() {
		instance = &Tracer{}
		instance.logger.Store(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	})
	return instance
}

// New builds a standalone tracer from opts, independent of the singleton.
func New(opts Options) (*Tracer, error) {
	t := &Tracer{}
	if err := t.Configure(opts); err != nil {
		return nil, err
	}
	return t, nil
}

// Configure replaces the handler. On error the previous configuration stays.
func (t *Tracer) Configure(opts Options) error {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return err
	}
	if !ValidFormat(opts.Format) {
		return fmt.Errorf("unknown log format %q (want text or json)", opts.Format)
	}

	var out io.Writer = os.Stdout
	if opts.Output != nil {
		out = opts.Output
	}

	var file *rotatingFile
	if opts.File != "" {
		maxSize := opts.MaxSizeMB
		if maxSize <= 0 {
			maxSize = defaultMaxSizeMB
		}
		maxFiles := opts.MaxFiles
		if maxFiles <= 0 {
			maxFiles = defaultMaxFiles
		}
		file, err = openRotatingFile(opts.File, int64(maxSize)*1024*1024, maxFiles)
		if err != nil {
			return err
		}
		out = io.MultiWriter(out, file)
	}

	hopts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(strings.TrimSpace(opts.Format), "json") {
		h = slog.NewJSONHandler(out, hopts)
	} else {
		h = slog.NewTextHandler(out, hopts)
	}

	t.mu.Lock()
	old := t.file
	t.file = file
	t.logger.Store(slog.New(h))
	t.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// SetDefault makes this tracer the slog default, so the standard "log"
// package and slog's top-level functions write through it too.
func (t *Tracer) SetDefault() {
	slog.SetDefault(t.Logger())
}

// Logger returns the underlying slog logger for structured logging.
func (t *Tracer) Logger() *slog.Logger {
	return t.logger.Load()
}

// Close closes the log file, if any. Logging keeps working on stdout.
func (t *Tracer) Close() {
	t.mu.Lock()
	f := t.file
	t.file = nil
	t.mu.Unlock()
	if f != nil {
		_ = f.Close()
	}
}

func (t *Tracer) logf(level slog.Level, format string, args ...interface{}) {
	l := t.Logger()
	ctx := context.Background()
	if !l.Enabled(ctx, level) {
		return
	}
	l.Log(ctx, level, fmt.Sprintf(format, args...))
}

// Debug logs at debug level. Thin printf wrapper over slog.
func (t *Tracer) Debug(format string, args ...interface{}) {
	t.logf(slog.LevelDebug, format, args...)
}

// Info logs at info level. Thin printf wrapper over slog.
func (t *Tracer) Info(format string, args ...interface{}) {
	t.logf(slog.LevelInfo, format, args...)
}

// Warning logs at warn level. Thin printf wrapper over slog.
func (t *Tracer) Warning(format string, args ...interface{}) {
	t.logf(slog.LevelWarn, format, args...)
}

// Error logs at error level. Thin printf wrapper over slog.
func (t *Tracer) Error(format string, args ...interface{}) {
	t.logf(slog.LevelError, format, args...)
}
