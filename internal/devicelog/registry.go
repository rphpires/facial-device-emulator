package devicelog

import (
	"io"
	"sync"
)

// Registry keeps one Logger per device. It lives in the Manager and survives
// emulator stop/start: the flag belongs to the device, not to the instance.
type Registry struct {
	opts    Options
	onError func(error)

	mu      sync.Mutex
	loggers map[int]*Logger
}

// NewRegistry creates an empty registry.
func NewRegistry(opts Options, onError func(error)) *Registry {
	return &Registry{opts: opts, onError: onError, loggers: map[int]*Logger{}}
}

// For returns the device's logger, creating it if needed, with its info
// updated.
func (r *Registry) For(info Info) *Logger {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.loggers[info.ID]; ok {
		l.SetInfo(info)
		return l
	}
	l := New(r.opts, info, r.onError)
	r.loggers[info.ID] = l
	return l
}

// SetEnabled turns the device's log on or off (off deletes the files).
func (r *Registry) SetEnabled(info Info, on bool) error {
	if r == nil {
		return nil
	}
	return r.For(info).SetEnabled(on)
}

// Remove turns the log off, deletes the files and forgets the device.
func (r *Registry) Remove(id int) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	l := r.loggers[id]
	delete(r.loggers, id)
	r.mu.Unlock()
	if l != nil {
		return l.SetEnabled(false)
	}
	return r.opts.removeAll(id)
}

// ActivePath is the device's most recent file.
func (r *Registry) ActivePath(id int) string {
	return r.opts.FilePath(id, 0)
}

// WriteZip writes all of the device's files into a zip.
func (r *Registry) WriteZip(id int, w io.Writer) error {
	if r == nil {
		return ErrNoLogs
	}
	r.mu.Lock()
	l := r.loggers[id]
	r.mu.Unlock()
	if l != nil {
		return l.WriteZip(w)
	}
	return writeZip(w, r.opts.Files(id))
}
