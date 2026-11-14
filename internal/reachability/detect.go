package reachability

import (
	"os"
	"runtime"
	"strings"
)

// fileReader isolates file system reads so detect can be tested without
// depending on the real environment of the build machine.
type fileReader interface {
	exists(path string) bool
	read(path string) ([]byte, error)
}

type osReader struct{}

func (osReader) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (osReader) read(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// Detect takes a snapshot of the environment. Called once at startup.
func Detect() Environment {
	return detect(osReader{}, os.Getenv, runtime.GOOS, maxOpenFiles())
}

func detect(fs fileReader, getenv func(string) string, goos string, maxOpenFiles uint64) Environment {
	e := Environment{MaxOpenFiles: maxOpenFiles}

	switch {
	case fs.exists("/.dockerenv"):
		e.Kind = KindDocker
		e.HostNetwork = getenv("HOST_NETWORK") == "1"
		if !e.HostNetwork {
			if ranges, err := ParseRanges(getenv("PUBLISHED_PORT_RANGE")); err == nil {
				e.PublishedRanges = ranges
				e.RangesKnown = true
			}
		}

	case isWSL(fs):
		e.Kind = KindWSL
		e.WSLMirrored = wslMirrored(fs)

	case goos == "windows":
		e.Kind = KindWindows

	default:
		e.Kind = KindLinux
	}

	return e
}

func isWSL(fs fileReader) bool {
	content, err := fs.read("/proc/version")
	if err != nil {
		return false
	}
	v := strings.ToLower(string(content))
	return strings.Contains(v, "microsoft") || strings.Contains(v, "wsl")
}

// wslMirrored looks for the loopback0 interface, which WSL2 creates when
// networkingMode=mirrored is on. Verified empirically on a WSL2 without
// mirrored mode (`wsl -e ls /sys/class/net/` listed only eth0 and lo, no
// loopback0): the dangerous direction, where the marker would be present
// without mirrored mode and the code would hide a broken setup, did not
// occur. Confirming the opposite direction (enable networkingMode=mirrored
// and see loopback0 appear) is left for later, since it requires editing
// the machine's .wslconfig and restarting WSL.
func wslMirrored(fs fileReader) bool {
	return fs.exists("/sys/class/net/loopback0/address")
}
