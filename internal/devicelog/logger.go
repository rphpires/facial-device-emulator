// Package devicelog writes the log of a single emulator: connections,
// incoming requests and events sent to the management client. It is turned
// on and off by the device's log_enabled flag; turning it off deletes the
// files.
//
// Every method accepts a nil receiver and becomes a no-op, so emulators do
// not need to check whether they were given a logger.
package devicelog

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNoLogs means the device has no log files on disk.
var ErrNoLogs = errors.New("devicelog: no log files")

// Options defines where logs go and how much is kept.
type Options struct {
	Dir      string // directory for the log files
	MaxSize  int64  // size of a file before it is rotated
	MaxFiles int    // active file + rotated files
}

// DefaultOptions returns logs/devices with 8 files of 3MB.
func DefaultOptions() Options {
	return Options{Dir: filepath.Join("logs", "devices"), MaxSize: 3 << 20, MaxFiles: 8}
}

// FilePath returns the device's n-th file: 0 is the active one, 1 the most
// recently rotated.
func (o Options) FilePath(id, n int) string {
	if n == 0 {
		return filepath.Join(o.Dir, fmt.Sprintf("device-%d.log", id))
	}
	return filepath.Join(o.Dir, fmt.Sprintf("device-%d.%d.log", id, n))
}

// Files returns the device's existing files, newest first.
func (o Options) Files(id int) []string {
	var out []string
	for n := 0; n < o.MaxFiles; n++ {
		p := o.FilePath(id, n)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func (o Options) removeAll(id int) error {
	var first error
	for n := 0; n < o.MaxFiles; n++ {
		err := os.Remove(o.FilePath(id, n))
		if err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

// Info identifies the device in the header of each file.
type Info struct {
	ID   int
	Name string
	Type string
	Port int
}

// Logger is the log of a single device.
type Logger struct {
	opts    Options
	onError func(error)
	now     func() time.Time
	enabled atomic.Bool

	mu                sync.Mutex
	info              Info
	file              *os.File
	size              int64
	failed            bool
	rotateErrReported bool
	conns             map[string]*connInfo
	streams           map[string]*streamInfo
}

// New creates a disabled logger. onError receives disk failures (may be nil).
func New(opts Options, info Info, onError func(error)) *Logger {
	if opts.MaxFiles < 2 {
		opts.MaxFiles = 2
	}
	return &Logger{
		opts:    opts,
		info:    info,
		onError: onError,
		now:     time.Now,
		conns:   map[string]*connInfo{},
		streams: map[string]*streamInfo{},
	}
}

// Enabled reports whether logging is on.
func (l *Logger) Enabled() bool {
	return l != nil && l.enabled.Load()
}

// SetInfo updates the name/port used in the next file header.
func (l *Logger) SetInfo(info Info) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.info = info
	l.mu.Unlock()
}

// SetEnabled turns logging on or off. Turning it on again after a disk
// failure retries. Turning it off closes and deletes all files.
func (l *Logger) SetEnabled(on bool) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if on {
		if l.enabled.Load() && !l.failed {
			return nil
		}
		l.failed = false
		l.enabled.Store(true)
		l.writeLocked("LOG", "enabled")
		return nil
	}

	l.enabled.Store(false)
	l.closeLocked()
	l.failed = false
	l.conns = map[string]*connInfo{}
	l.streams = map[string]*streamInfo{}
	return l.opts.removeAll(l.info.ID)
}

// Printf writes a "date time KIND message" line.
func (l *Logger) Printf(kind, format string, args ...interface{}) {
	if !l.Enabled() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeLocked(kind, fmt.Sprintf(format, args...))
}

// writeLocked requires l.mu. It re-checks enabled because a
// SetEnabled(false) may have run between the caller's Enabled() and the lock.
func (l *Logger) writeLocked(kind, msg string) {
	if !l.enabled.Load() || l.failed {
		return
	}
	line := l.now().Format("2006-01-02 15:04:05.000") + " " + kind + " " + msg + "\n"

	if l.file == nil {
		if err := l.openLocked(); err != nil {
			l.fail(err)
			return
		}
	}
	if l.size > 0 && l.size+int64(len(line)) > l.opts.MaxSize {
		if err := l.rotateLocked(); err != nil {
			if l.file == nil {
				l.fail(err)
				return
			}
			// Keep writing to the current file; retry on the next line.
			if !l.rotateErrReported {
				l.rotateErrReported = true
				l.report(fmt.Errorf("rotation: %w", err))
			}
		} else {
			l.rotateErrReported = false
		}
	}

	n, err := l.file.WriteString(line)
	l.size += int64(n)
	if err != nil {
		l.fail(err)
	}
}

func (l *Logger) openLocked() error {
	if err := os.MkdirAll(l.opts.Dir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.opts.FilePath(l.info.ID, 0), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.file, l.size = f, st.Size()
	if l.size == 0 {
		n, err := fmt.Fprintf(f, "# device %d %q %s port %d — opened %s\n",
			l.info.ID, l.info.Name, l.info.Type, l.info.Port,
			l.now().Format("2006-01-02 15:04:05"))
		l.size += int64(n)
		if err != nil {
			return err
		}
	}
	return nil
}

// rotateLocked deletes the oldest file, shifts .n to .n+1 and opens a new
// active file.
func (l *Logger) rotateLocked() error {
	l.closeLocked()
	id, last := l.info.ID, l.opts.MaxFiles-1

	if err := os.Remove(l.opts.FilePath(id, last)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return l.reopenAfter(err)
	}
	for n := last - 1; n >= 0; n-- {
		err := os.Rename(l.opts.FilePath(id, n), l.opts.FilePath(id, n+1))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return l.reopenAfter(err)
		}
	}
	return l.openLocked()
}

// reopenAfter reopens the active file after a failed rotation so logging
// can continue in it.
func (l *Logger) reopenAfter(cause error) error {
	if err := l.openLocked(); err != nil {
		return err
	}
	return cause
}

func (l *Logger) closeLocked() {
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	l.size = 0
}

// fail gives up until the next SetEnabled(true): a disk failure must not
// turn into an error line in the trace on every request.
func (l *Logger) fail(err error) {
	l.failed = true
	l.closeLocked()
	l.report(err)
}

func (l *Logger) report(err error) {
	if l.onError != nil {
		l.onError(fmt.Errorf("device %d log: %w", l.info.ID, err))
	}
}

// WriteZip writes a zip with all of the device's files. The files are opened
// under the lock, so a rotation cannot happen halfway, and compressed outside
// it: the middleware and the emulator's ConnState must not wait for the
// download. A rename after opening does not affect the open descriptor.
func (l *Logger) WriteZip(w io.Writer) error {
	if l == nil {
		return ErrNoLogs
	}
	l.mu.Lock()
	if l.file != nil {
		_ = l.file.Sync()
	}
	opened, err := openAll(l.opts.Files(l.info.ID))
	l.mu.Unlock()
	if err != nil {
		return err
	}
	return zipOpened(w, opened)
}

func writeZip(w io.Writer, files []string) error {
	opened, err := openAll(files)
	if err != nil {
		return err
	}
	return zipOpened(w, opened)
}

func openAll(files []string) ([]*os.File, error) {
	if len(files) == 0 {
		return nil, ErrNoLogs
	}
	var opened []*os.File
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			for _, o := range opened {
				o.Close()
			}
			return nil, err
		}
		opened = append(opened, f)
	}
	return opened, nil
}

func zipOpened(w io.Writer, files []*os.File) error {
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	zw := zip.NewWriter(w)
	for _, f := range files {
		if err := addToZip(zw, f); err != nil {
			return err
		}
	}
	return zw.Close()
}

func addToZip(zw *zip.Writer, f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(st)
	if err != nil {
		return err
	}
	hdr.Name = filepath.Base(f.Name())
	hdr.Method = zip.Deflate
	dst, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, f)
	return err
}
