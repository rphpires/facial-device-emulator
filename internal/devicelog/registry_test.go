package devicelog

import (
	"bytes"
	"os"
	"testing"
)

func TestForReturnsSameLoggerAndUpdatesInfo(t *testing.T) {
	r := NewRegistry(testOpts(t), nil)
	a := r.For(Info{ID: 4, Name: "old"})
	b := r.For(Info{ID: 4, Name: "new"})
	if a != b {
		t.Fatal("For created two loggers for the same id")
	}
	if b.info.Name != "new" {
		t.Errorf("info not updated: %+v", b.info)
	}
}

func TestRemoveDeletesEvenWithoutLogger(t *testing.T) {
	o := testOpts(t)
	if err := os.MkdirAll(o.Dir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(o.FilePath(5, 0), []byte("x"), 0644)
	_ = os.WriteFile(o.FilePath(5, 1), []byte("x"), 0644)
	r := NewRegistry(o, nil)
	if err := r.Remove(5); err != nil {
		t.Fatal(err)
	}
	if fs := o.Files(5); len(fs) != 0 {
		t.Errorf("left behind: %v", fs)
	}
}

func TestRemoveLeavesIDWithSamePrefixAlone(t *testing.T) {
	o := testOpts(t)
	r := NewRegistry(o, nil)
	_ = r.SetEnabled(Info{ID: 1}, true)
	_ = r.SetEnabled(Info{ID: 12}, true)
	_ = r.Remove(1)
	if fs := o.Files(12); len(fs) != 1 {
		t.Errorf("files of device 12: %v", fs)
	}
	var buf bytes.Buffer
	if err := r.WriteZip(1, &buf); err != ErrNoLogs {
		t.Errorf("zip of device 1 after removal: %v", err)
	}
}

func TestRegistryWriteZipWithoutLoggerReadsFromDisk(t *testing.T) {
	o := testOpts(t)
	_ = os.MkdirAll(o.Dir, 0755)
	_ = os.WriteFile(o.FilePath(8, 0), []byte("line\n"), 0644)
	r := NewRegistry(o, nil)
	var buf bytes.Buffer
	if err := r.WriteZip(8, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("empty zip")
	}
}

func TestNilRegistryDoesNotPanic(t *testing.T) {
	var r *Registry
	if r.For(Info{ID: 1}) != nil {
		t.Error("For on a nil registry should return nil")
	}
	_ = r.SetEnabled(Info{ID: 1}, true)
	_ = r.Remove(1)
}
