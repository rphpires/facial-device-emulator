package dahua

import (
	"net/url"
	"strings"
	"testing"
)

func TestConfigStore_RenderAccessControlDefaults(t *testing.T) {
	s := newConfigStore("00:11:22:33:44:55")

	out, ok := s.Render("AccessControl")
	if !ok {
		t.Fatal("AccessControl must be a known table")
	}
	// The client's table parser strips the "table.AccessControl" prefix and
	// then requires the key to start with "[0]." or ".".
	for _, required := range []string{
		"table.AccessControl[0].CardStoreFormat=",
		"table.AccessControl[0].UnlockHoldInterval=",
		"table.AccessControl[0].SensorEnable=",
		"table.AccessControl[0].CloseTimeout=",
		"table.AccessControl[0].State=",
	} {
		if !strings.Contains(out, required) {
			t.Errorf("missing %q in response:\n%s", required, out)
		}
	}
	if !strings.Contains(out, "\r\n") {
		t.Error("lines must be CRLF-separated, like the real device")
	}
	// AccessControlGeneral must not leak into AccessControl: the prefix is
	// matched as a string and a naive match would pull both in.
	if strings.Contains(out, "AccessControlGeneral") {
		t.Errorf("AccessControlGeneral leaked into the AccessControl table:\n%s", out)
	}
}

func TestConfigStore_SetThenRenderRoundTrip(t *testing.T) {
	s := newConfigStore("00:11:22:33:44:55")
	s.Set("AccessControl[0].CardStoreFormat", "1")

	out, _ := s.Render("AccessControl")
	if !strings.Contains(out, "table.AccessControl[0].CardStoreFormat=1") {
		t.Errorf("value set was not returned by getConfig:\n%s", out)
	}
}

func TestConfigStore_SetAllFromQuerySkipsControlParams(t *testing.T) {
	s := newConfigStore("00:11:22:33:44:55")
	q := url.Values{}
	q.Set("action", "setConfig")
	q.Set("AccessControl[0].SensorEnable", "true")
	q.Set("Intelbras_ModeCfg.DeviceMode", "2")

	n := s.SetAllFromQuery(q)
	if n != 2 {
		t.Errorf("stored keys: got %d, want 2 (action does not count)", n)
	}
	if s.Get("Intelbras_ModeCfg.DeviceMode") != "2" {
		t.Errorf("DeviceMode: got %q, want 2", s.Get("Intelbras_ModeCfg.DeviceMode"))
	}
	if s.Get("action") != "" {
		t.Error("action must not become configuration")
	}
}

func TestConfigStore_RenderUnknownTable(t *testing.T) {
	s := newConfigStore("00:11:22:33:44:55")
	if _, ok := s.Render("DoesNotExist"); ok {
		t.Error("unknown table must return ok=false")
	}
}

func TestConfigStore_RenderNetworkKeepsMacDelimiters(t *testing.T) {
	s := newConfigStore("00:11:22:33:44:55")
	out, ok := s.Render("Network")
	if !ok {
		t.Fatal("Network must be known")
	}
	// The client extracts the MAC between "PhysicalAddress=" and the delimiter below.
	if !strings.Contains(out, "table.Network.eth0.PhysicalAddress=00:11:22:33:44:55") {
		t.Errorf("MAC missing:\n%s", out)
	}
	if !strings.Contains(out, "\r\ntable.Network.eth0.SubnetMask=") {
		t.Error("the delimiter the client uses to extract the MAC is gone")
	}

}
