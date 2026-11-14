package trace

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextFormatAndLevel(t *testing.T) {
	var buf bytes.Buffer
	tr, err := New(Options{Level: "warn", Output: &buf})
	if err != nil {
		t.Fatal(err)
	}
	tr.Info("hidden %d", 1)
	tr.Warning("shown %d", 2)
	tr.Error("100%% done")
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Errorf("info line written at warn level: %q", out)
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, `msg="shown 2"`) {
		t.Errorf("missing warn line: %q", out)
	}
	if !strings.Contains(out, `msg="100% done"`) {
		t.Errorf("printf args not applied: %q", out)
	}
}

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	tr, err := New(Options{Format: "json", Level: "debug", Output: &buf})
	if err != nil {
		t.Fatal(err)
	}
	tr.Debug("device %d", 7)
	var rec map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not JSON: %v: %q", err, buf.String())
	}
	if rec["msg"] != "device 7" || rec["level"] != "DEBUG" {
		t.Errorf("record = %v", rec)
	}
}

func TestInvalidOptions(t *testing.T) {
	if _, err := New(Options{Level: "loud"}); err == nil {
		t.Error("want error for unknown level")
	}
	if _, err := New(Options{Format: "html"}); err == nil {
		t.Error("want error for unknown format")
	}
}

func TestFileAndRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "trace.log")
	var stdout bytes.Buffer
	tr, err := New(Options{File: path, MaxFiles: 2, Output: &stdout})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	// Shrink the limit so a few lines force several rotations.
	tr.file.maxBytes = 200
	for i := 0; i < 50; i++ {
		tr.Info("line %d %s", i, strings.Repeat("x", 40))
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("current file: %v", err)
	}
	for _, suffix := range []string{".1", ".2"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Errorf("rotated file %s missing: %v", suffix, err)
		}
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("more files kept than MaxFiles")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "line 49") {
		t.Errorf("last line not in current file: %q", data)
	}
	if !strings.Contains(stdout.String(), "line 0") {
		t.Error("stdout copy missing")
	}
}

// The default tracer never touches the disk.
func TestNewTracerWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	NewTracer().Info("hello")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("files created: %v", entries)
	}
}
