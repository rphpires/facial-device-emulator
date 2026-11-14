package reachability

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestComposeMatchesPublishedRangeVar: the UI warning relies on
// PUBLISHED_PORT_RANGE describing exactly what the compose file publishes.
// If the two diverge, the application either flags a healthy device or
// misses an unreachable one, the two worst outcomes for this feature.
func TestComposeMatchesPublishedRangeVar(t *testing.T) {
	content, err := os.ReadFile("../../packaging/docker/docker-compose.yml")
	if err != nil {
		t.Fatalf("reading compose file: %v", err)
	}

	var compose struct {
		Services struct {
			App struct {
				Environment map[string]string `yaml:"environment"`
				Ports       []string          `yaml:"ports"`
			} `yaml:"app"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(content, &compose); err != nil {
		t.Fatalf("parsing compose file: %v", err)
	}

	declared := compose.Services.App.Environment["PUBLISHED_PORT_RANGE"]
	if declared == "" {
		t.Fatal("PUBLISHED_PORT_RANGE missing from the Docker package compose file")
	}

	ranges, err := ParseRanges(declared)
	if err != nil {
		t.Fatalf("PUBLISHED_PORT_RANGE = %q is invalid: %v", declared, err)
	}

	// Every published port other than the web UI's must be covered by the
	// variable.
	for _, p := range compose.Services.App.Ports {
		host, _, _ := splitMapping(p)
		if host == "7070" {
			continue
		}
		published, err := ParseRanges(host)
		if err != nil {
			t.Fatalf("mapping %q could not be parsed: %v", p, err)
		}
		for _, pub := range published {
			if !covers(ranges, pub) {
				t.Errorf("compose publishes %s but PUBLISHED_PORT_RANGE=%q does not cover it", pub, declared)
			}
		}
	}
}

// TestComposeLinuxConsistent: in docker-compose.linux.yml the HOST_NETWORK
// variable is a CLAIM about the network mode, not the mode itself; direct
// port access comes from network_mode: host. The two must not diverge: if
// someone removes network_mode: host and forgets the variable, Analyze keeps
// trusting HOST_NETWORK=1 and reports every device as reachable while no
// port is published, exactly the false green this package exists to avoid.
func TestComposeLinuxConsistent(t *testing.T) {
	content, err := os.ReadFile("../../packaging/docker/docker-compose.linux.yml")
	if err != nil {
		t.Fatalf("reading compose file: %v", err)
	}

	var compose struct {
		Services struct {
			App struct {
				NetworkMode string            `yaml:"network_mode"`
				Environment map[string]string `yaml:"environment"`
				Ports       []string          `yaml:"ports"`
			} `yaml:"app"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(content, &compose); err != nil {
		t.Fatalf("parsing compose file: %v", err)
	}

	if compose.Services.App.NetworkMode != "host" {
		t.Errorf("network_mode = %q, want \"host\"", compose.Services.App.NetworkMode)
	}
	if compose.Services.App.Environment["HOST_NETWORK"] != "1" {
		t.Errorf("HOST_NETWORK = %q, want \"1\"", compose.Services.App.Environment["HOST_NETWORK"])
	}
	if len(compose.Services.App.Ports) != 0 {
		t.Errorf("ports = %v, want none: host networking publishes no ports", compose.Services.App.Ports)
	}
	if _, ok := compose.Services.App.Environment["PUBLISHED_PORT_RANGE"]; ok {
		t.Error("PUBLISHED_PORT_RANGE present in the Linux compose file: host networking has no published range")
	}
}

func splitMapping(s string) (host, container string, ok bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return s, s, false
}

func covers(ranges []PortRange, target PortRange) bool {
	for _, r := range ranges {
		if r.Contains(target.Start) && r.Contains(target.End) {
			return true
		}
	}
	return false
}
