package devicelog

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func testOpts(t *testing.T) Options {
	t.Helper()
	return Options{Dir: t.TempDir(), MaxSize: 1 << 20, MaxFiles: 8}
}

func testInfo() Info {
	return Info{ID: 1, Name: "Gate A", Type: "Hikvision", Port: 4001}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func TestDisabledCreatesNoFile(t *testing.T) {
	o := testOpts(t)
	l := New(o, testInfo(), nil)
	l.Printf("IN", "nothing")
	if fs := o.Files(1); len(fs) != 0 {
		t.Fatalf("disabled logger created files: %v", fs)
	}
}

func TestEnableWritesHeaderAndLine(t *testing.T) {
	o := testOpts(t)
	l := New(o, testInfo(), nil)
	if err := l.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	l.Printf("IN", "hello %d", 42)

	got := readFile(t, o.FilePath(1, 0))
	if !strings.HasPrefix(got, `# device 1 "Gate A" Hikvision port 4001 — opened `) {
		t.Errorf("wrong header:\n%s", got)
	}
	line := regexp.MustCompile(`(?m)^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3} IN hello 42$`)
	if !line.MatchString(got) {
		t.Errorf("IN line missing:\n%s", got)
	}
	if !strings.Contains(got, " LOG enabled") {
		t.Errorf("LOG enabled line missing:\n%s", got)
	}
}

func TestEnablingTwiceDoesNotDuplicateLogLine(t *testing.T) {
	o := testOpts(t)
	l := New(o, testInfo(), nil)
	_ = l.SetEnabled(true)
	_ = l.SetEnabled(true)
	if n := strings.Count(readFile(t, o.FilePath(1, 0)), "LOG enabled"); n != 1 {
		t.Errorf("LOG enabled %d times, want 1", n)
	}
}

func TestRotationKeepsAtMostMaxFiles(t *testing.T) {
	o := Options{Dir: t.TempDir(), MaxSize: 300, MaxFiles: 3}
	l := New(o, testInfo(), nil)
	_ = l.SetEnabled(true)
	for i := 0; i < 60; i++ {
		l.Printf("IN", "line %03d %s", i, strings.Repeat("x", 30))
	}

	fs := o.Files(1)
	if len(fs) != 3 {
		t.Fatalf("files = %v, want 3", fs)
	}
	if _, err := os.Stat(o.FilePath(1, 3)); !os.IsNotExist(err) {
		t.Errorf("device-1.3.log should not exist")
	}
	for _, f := range fs {
		if !strings.HasPrefix(readFile(t, f), "# device 1 ") {
			t.Errorf("%s has no header", f)
		}
	}
	// The active file has the last line; .1 has older lines than the active one.
	if !strings.Contains(readFile(t, o.FilePath(1, 0)), "line 059") {
		t.Errorf("active file lacks the last line")
	}
	if strings.Contains(readFile(t, o.FilePath(1, 1)), "line 059") {
		t.Errorf(".1 must not have the last line")
	}
}

func TestDisablingDeletesFiles(t *testing.T) {
	o := Options{Dir: t.TempDir(), MaxSize: 300, MaxFiles: 3}
	l := New(o, testInfo(), nil)
	_ = l.SetEnabled(true)
	for i := 0; i < 30; i++ {
		l.Printf("IN", "line %s", strings.Repeat("y", 40))
	}
	if err := l.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if fs := o.Files(1); len(fs) != 0 {
		t.Fatalf("left behind: %v", fs)
	}
	l.Printf("IN", "after disabling")
	if fs := o.Files(1); len(fs) != 0 {
		t.Fatalf("wrote after disabling: %v", fs)
	}
}

func TestNilLoggerDoesNotPanic(t *testing.T) {
	var l *Logger
	l.Printf("IN", "x")
	_ = l.SetEnabled(true)
	l.SetInfo(Info{})
	if l.Enabled() {
		t.Error("a nil logger must not be enabled")
	}
}

func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	o := testOpts(t)
	l := New(o, testInfo(), nil)
	_ = l.SetEnabled(true)

	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				l.Printf("IN", "g%02d-%03d %s", g, i, strings.Repeat("z", 50))
			}
		}(g)
	}
	wg.Wait()

	ok := regexp.MustCompile(`^(# device .*|\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3} (LOG enabled|IN g\d\d-\d{3} z{50}))$`)
	lines := strings.Split(strings.TrimRight(readFile(t, o.FilePath(1, 0)), "\n"), "\n")
	if len(lines) != 1+1+20*50 {
		t.Errorf("%d lines, want %d", len(lines), 1+1+20*50)
	}
	for _, ln := range lines {
		if !ok.MatchString(ln) {
			t.Fatalf("corrupted line: %q", ln)
		}
	}
}

func TestOpenFailureReportedOnce(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	errCount := 0
	l := New(Options{Dir: filepath.Join(file, "devices"), MaxSize: 1 << 20, MaxFiles: 8},
		testInfo(), func(error) { errCount++ })
	_ = l.SetEnabled(true)
	l.Printf("IN", "a")
	l.Printf("IN", "b")
	if errCount != 1 {
		t.Errorf("onError called %d times, want 1", errCount)
	}
}

func TestWriteZipContainsAllFiles(t *testing.T) {
	o := Options{Dir: t.TempDir(), MaxSize: 300, MaxFiles: 3}
	l := New(o, testInfo(), nil)
	_ = l.SetEnabled(true)
	for i := 0; i < 30; i++ {
		l.Printf("IN", "line %s", strings.Repeat("w", 40))
	}

	var buf bytes.Buffer
	if err := l.WriteZip(&buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if got := fmt.Sprint(names); got != "[device-1.log device-1.1.log device-1.2.log]" {
		t.Errorf("zip = %s", got)
	}
}

func TestWriteZipWithoutFiles(t *testing.T) {
	l := New(testOpts(t), testInfo(), nil)
	if err := l.WriteZip(&bytes.Buffer{}); err != ErrNoLogs {
		t.Errorf("err = %v, want ErrNoLogs", err)
	}
}

// blockingWriter holds the first write until released, simulating a slow
// zip download.
type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

// Building the zip must not hold the logger lock: the middleware and the
// emulator's ConnState would wait for the whole download.
func TestWriteZipDoesNotBlockWrites(t *testing.T) {
	o := testOpts(t)
	l := New(o, testInfo(), nil)
	_ = l.SetEnabled(true)
	l.Printf("IN", "before")

	w := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	go func() { _ = l.WriteZip(w) }()
	<-w.started
	defer close(w.release)

	done := make(chan struct{})
	go func() {
		l.Printf("IN", "during zip")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Printf blocked while the zip was being written")
	}
}
