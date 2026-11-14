package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/models"
	"github.com/rphpires/facial-device-emulator/internal/reachability"

	"github.com/gin-gonic/gin"
)

// TestGetReachability_HTTPContract pins the JSON shape the devices page
// reads. The notice reads "unreachable" and the "devices" list: renaming
// either breaks the page silently.
func TestGetReachability_HTTPContract(t *testing.T) {
	env := reachability.Environment{
		Kind:            reachability.KindDocker,
		PublishedRanges: []reachability.PortRange{{Start: 4000, End: 4099}},
		RangesKnown:     true,
	}
	ports := []reachability.DevicePort{
		{DeviceID: 1, Port: 4001, Started: true},
		{DeviceID: 2, Port: 4200, Started: true},
	}

	h := &Handler{env: env}
	r := gin.New()
	r.GET("/api/reachability", func(c *gin.Context) {
		c.JSON(http.StatusOK, reachability.Analyze(ports, h.env))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/reachability", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var body struct {
		Unreachable int `json:"unreachable"`
		Unknown     int `json:"unknown"`
		Devices     []struct {
			DeviceID int    `json:"device_id"`
			Port     int    `json:"port"`
			Status   string `json:"status"`
			Reason   string `json:"reason"`
		} `json:"devices"`
		Environment struct {
			Kind string `json:"kind"`
		} `json:"environment"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v - body: %s", err, w.Body.String())
	}

	if body.Environment.Kind != "docker" {
		t.Errorf("environment.kind = %q, want \"docker\"", body.Environment.Kind)
	}
	if body.Unreachable != 1 {
		t.Errorf("unreachable = %d, want 1", body.Unreachable)
	}
	if len(body.Devices) != 2 {
		t.Fatalf("len(devices) = %d, want 2", len(body.Devices))
	}
	if body.Devices[0].Status != "ok" {
		t.Errorf("devices[0].status = %q, want \"ok\"", body.Devices[0].Status)
	}
	// devices.js filters on this exact value.
	if body.Devices[1].Status != "unreachable" {
		t.Errorf("devices[1].status = %q, want \"unreachable\"", body.Devices[1].Status)
	}
	if body.Devices[1].Reason == "" {
		t.Error("devices[1].reason is empty, want a human-readable explanation")
	}
}

// TestDevicePorts covers the models.Device to reachability.DevicePort
// mapping, which TestGetReachability_HTTPContract does not reach: that
// test builds DevicePort by hand. A wrong Status or a bind error attached
// to the wrong device would be caught here.
func TestDevicePorts(t *testing.T) {
	devices := []models.Device{
		{ID: 1, Port: 4001, Status: "running"},
		{ID: 2, Port: 4002, Status: "stopped"},
		{ID: 3, Port: 4003, Status: "running"},
	}

	// Only device 3 has a bind error, proving it does not leak to the
	// other two.
	lastError := func(deviceID int) string {
		if deviceID == 3 {
			return "address already in use"
		}
		return ""
	}

	ports := devicePorts(devices, lastError)

	if len(ports) != 3 {
		t.Fatalf("len(ports) = %d, want 3", len(ports))
	}
	if !ports[0].Started {
		t.Errorf("device 1 (running): Started = false, want true")
	}
	if ports[1].Started {
		t.Errorf("device 2 (stopped): Started = true, want false")
	}
	if ports[2].DeviceID != 3 {
		t.Fatalf("ports[2].DeviceID = %d, want 3", ports[2].DeviceID)
	}
	if ports[2].BindError != "address already in use" {
		t.Errorf("device 3: BindError = %q, want \"address already in use\"", ports[2].BindError)
	}
	if ports[0].BindError != "" || ports[1].BindError != "" {
		t.Error("device 3's bind error leaked to another device")
	}
}
