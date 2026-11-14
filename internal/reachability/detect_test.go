package reachability

import (
	"fmt"
	"testing"
)

// fakeFS is a fake file system: only the paths in the map exist.
type fakeFS map[string]string

func (f fakeFS) exists(path string) bool {
	_, ok := f[path]
	return ok
}

func (f fakeFS) read(path string) ([]byte, error) {
	v, ok := f[path]
	if !ok {
		return nil, fmt.Errorf("does not exist: %s", path)
	}
	return []byte(v), nil
}

func noEnv(string) string { return "" }

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestDetect covers how the environment is recognized. The trickiest case
// is Docker: from inside the container there is no way to see the host's
// port mapping, so the only source is the variable the compose file sets.
func TestDetect(t *testing.T) {
	cases := []struct {
		name            string
		fs              fakeFS
		getenv          func(string) string
		goos            string
		wantKind        Kind
		wantRangesKnown bool
		wantRanges      []PortRange
		wantHostNet     bool
		wantWSLMirrored bool
	}{
		{
			name:     "native windows",
			fs:       fakeFS{},
			getenv:   noEnv,
			goos:     "windows",
			wantKind: KindWindows,
		},
		{
			name:     "native linux",
			fs:       fakeFS{},
			getenv:   noEnv,
			goos:     "linux",
			wantKind: KindLinux,
		},
		{
			name:     "wsl without mirrored mode",
			fs:       fakeFS{"/proc/version": "Linux version 5.15.0-microsoft-standard-WSL2"},
			getenv:   noEnv,
			goos:     "linux",
			wantKind: KindWSL,
		},
		{
			name: "wsl with mirrored mode",
			fs: fakeFS{
				"/proc/version":                    "Linux version 5.15.0-microsoft-standard-WSL2",
				"/sys/class/net/loopback0/address": "00:00:00:00:00:00",
			},
			getenv:          noEnv,
			goos:            "linux",
			wantKind:        KindWSL,
			wantWSLMirrored: true,
		},
		{
			name:            "docker with published range",
			fs:              fakeFS{"/.dockerenv": ""},
			getenv:          env(map[string]string{"PUBLISHED_PORT_RANGE": "4000-4499"}),
			goos:            "linux",
			wantKind:        KindDocker,
			wantRangesKnown: true,
			wantRanges:      []PortRange{{4000, 4499}},
		},
		{
			name:            "docker without the variable: range unknown",
			fs:              fakeFS{"/.dockerenv": ""},
			getenv:          noEnv,
			goos:            "linux",
			wantKind:        KindDocker,
			wantRangesKnown: false,
		},
		{
			name:            "docker with malformed variable: range unknown",
			fs:              fakeFS{"/.dockerenv": ""},
			getenv:          env(map[string]string{"PUBLISHED_PORT_RANGE": "4499-4000"}),
			goos:            "linux",
			wantKind:        KindDocker,
			wantRangesKnown: false,
		},
		{
			name:        "docker with host networking",
			fs:          fakeFS{"/.dockerenv": ""},
			getenv:      env(map[string]string{"HOST_NETWORK": "1"}),
			goos:        "linux",
			wantKind:    KindDocker,
			wantHostNet: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := detect(c.fs, c.getenv, c.goos, 1024)

			if e.Kind != c.wantKind {
				t.Errorf("Kind = %v, want %v", e.Kind, c.wantKind)
			}
			if e.RangesKnown != c.wantRangesKnown {
				t.Errorf("RangesKnown = %v, want %v", e.RangesKnown, c.wantRangesKnown)
			}
			if e.HostNetwork != c.wantHostNet {
				t.Errorf("HostNetwork = %v, want %v", e.HostNetwork, c.wantHostNet)
			}
			if e.WSLMirrored != c.wantWSLMirrored {
				t.Errorf("WSLMirrored = %v, want %v", e.WSLMirrored, c.wantWSLMirrored)
			}
			if len(e.PublishedRanges) != len(c.wantRanges) {
				t.Fatalf("PublishedRanges = %v, want %v", e.PublishedRanges, c.wantRanges)
			}
			for i := range e.PublishedRanges {
				if e.PublishedRanges[i] != c.wantRanges[i] {
					t.Errorf("range %d = %v, want %v", i, e.PublishedRanges[i], c.wantRanges[i])
				}
			}
		})
	}
}
