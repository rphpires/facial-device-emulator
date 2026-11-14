package dahua

import (
	"fmt"
	"os"
	"testing"
)

// srcDir is the package source directory, for tests that read source files.
var srcDir string

// TestMain runs the package's tests from a temporary directory, so nothing
// they create lands in the source tree.
func TestMain(m *testing.M) {
	os.Exit(runInTempDir(m))
}

func runInTempDir(m *testing.M) int {
	var err error
	if srcDir, err = os.Getwd(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dir, err := os.MkdirTemp("", "dahua-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.Chdir(srcDir) //nolint:errcheck // best effort on the way out

	return m.Run()
}
