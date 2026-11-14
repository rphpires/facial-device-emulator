package dahua

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// configStore keeps the device configuration in memory, so values written
// with setConfig (CardStoreFormat, SensorEnable, ...) can be read back with
// getConfig.
type configStore struct {
	mu     sync.RWMutex
	values map[string]string
	mac    string
}

// knownTables are the `name` values getConfig accepts. Any other returns
// 400, like a real device that lacks that table.
var knownTables = []string{
	"AccessControl",
	"AccessControlGeneral",
	"AccessTimeSchedule",
	"Intelbras_ModeCfg",
	"Network",
	"PictureHttpUpload",
	"PrivacySet",
}

func newConfigStore(mac string) *configStore {
	s := &configStore{values: map[string]string{}, mac: mac}

	// Defaults observed on a Dahua/Intelbras access controller.
	defaults := map[string]string{
		"AccessControl[0].CardStoreFormat":     "0", // 0 HEXADECIMAL, 1 DECIMAL
		"AccessControl[0].UnlockHoldInterval":  "3000",
		"AccessControl[0].SensorEnable":        "false",
		"AccessControl[0].CloseTimeout":        "60",
		"AccessControl[0].State":               "Normal",
		"AccessControl[0].UnlockReloadEnable":  "true",
		"AccessControlGeneral.SnapshotUpload":  "false",
		"PrivacySet.IDPrivacyEnable":           "false",
		"PrivacySet.NamePrivacyEnable":         "false",
		"PrivacySet.NumPrivacyEnable":          "false",
		"Intelbras_ModeCfg.DeviceMode":         "0",
		"Intelbras_ModeCfg.KeepAlive.Enable":   "false",
		"Intelbras_ModeCfg.KeepAlive.Interval": "5",
		"Intelbras_ModeCfg.KeepAlive.Path":     "/keepalive",
		"Intelbras_ModeCfg.KeepAlive.TimeOut":  "2000",
		"AccessTimeSchedule[0].Enable":         "true",
		"AccessTimeSchedule[0].Name":           "AllDay",
	}
	for k, v := range defaults {
		s.values[k] = v
	}
	return s
}

func (s *configStore) Set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
}

func (s *configStore) Get(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.values[key]
}

// SetAllFromQuery stores every parameter of a setConfig, skipping the CGI's
// own control parameters. It returns how many keys were stored.
func (s *configStore) SetAllFromQuery(q url.Values) int {
	control := map[string]bool{"action": true, "name": true, "channel": true}

	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	for key, vals := range q {
		if control[key] || len(vals) == 0 {
			continue
		}
		s.values[key] = vals[0]
		n++
	}
	return n
}

// Render returns the table as CRLF-separated `table.<Name>...=value` lines,
// the format the access-control client's table parser expects.
func (s *configStore) Render(table string) (string, bool) {
	if strings.EqualFold(table, "Network") {
		return s.renderNetwork(), true
	}

	known := false
	for _, t := range knownTables {
		if strings.EqualFold(t, table) {
			table = t
			known = true
			break
		}
	}
	if !known {
		return "", false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var lines []string
	for key, value := range s.values {
		if key == table || strings.HasPrefix(key, table+"[") || strings.HasPrefix(key, table+".") {
			lines = append(lines, fmt.Sprintf("table.%s=%s", key, value))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\r\n"), true
}

// renderNetwork returns the Network table; clients extract the MAC by slicing
// between "PhysicalAddress=" and "\r\ntable.Network.eth0.SubnetMask=".
func (s *configStore) renderNetwork() string {
	return fmt.Sprintf("table.Network.DefaultInterface=eth0\r\n"+
		"table.Network.Domain=dahua\r\n"+
		"table.Network.Hostname=FaceTerminal\r\n"+

		"table.Network.eth0.DefaultGateway=192.168.0.1\r\n"+
		"table.Network.eth0.DhcpEnable=false\r\n"+
		"table.Network.eth0.DnsServers[0]=8.8.8.8\r\n"+
		"table.Network.eth0.DnsServers[1]=8.8.4.4\r\n"+
		"table.Network.eth0.EnableDhcpReservedIP=false\r\n"+
		"table.Network.eth0.IPAddress=192.168.0.100\r\n"+
		"table.Network.eth0.MTU=1500\r\n"+
		"table.Network.eth0.PhysicalAddress=%s\r\n"+
		"table.Network.eth0.SubnetMask=255.255.255.0\r\n"+
		"table.Network.eth2.DefaultGateway=192.168.0.1\r\n"+
		"table.Network.eth2.DhcpEnable=true\r\n"+
		"table.Network.eth2.DnsServers[0]=8.8.8.8\r\n"+
		"table.Network.eth2.DnsServers[1]=8.8.4.4\r\n"+
		"table.Network.eth2.EnableDhcpReservedIP=false\r\n"+
		"table.Network.eth2.IPAddress=192.168.0.101\r\n"+
		"table.Network.eth2.MTU=1500\r\n"+
		"table.Network.eth2.PhysicalAddress=00:00:00:00:00:00\r\n"+
		"table.Network.eth2.SubnetMask=255.255.0.0", s.mac)
}
