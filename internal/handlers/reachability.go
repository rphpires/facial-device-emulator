package handlers

import (
	"net/http"

	"github.com/rphpires/facial-device-emulator/internal/models"
	"github.com/rphpires/facial-device-emulator/internal/reachability"

	"github.com/gin-gonic/gin"
)

// getReachability reports which devices will not be reachable by the
// access control system under test in this environment (for example,
// ports not published by Docker): the console shows them green while the
// client sees them offline.
func (h *Handler) getReachability(c *gin.Context) {
	devices, err := h.manager.ListDevices()
	if err != nil {
		h.tracer.Error("Failed to list devices for reachability: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Could not check the device ports",
		})
		return
	}

	ports := devicePorts(devices, h.manager.LastStartError)

	c.JSON(http.StatusOK, reachability.Analyze(ports, h.env))
}

// devicePorts maps the device list to the input of reachability.Analyze.
// It is separate from the handler so it can be tested without a Manager or
// a database: this is where a mapping mistake would slip through.
func devicePorts(devices []models.Device, lastError func(int) string) []reachability.DevicePort {
	ports := make([]reachability.DevicePort, 0, len(devices))
	for _, d := range devices {
		ports = append(ports, reachability.DevicePort{
			DeviceID:  d.ID,
			Port:      d.Port,
			Started:   d.Status == "running",
			BindError: lastError(d.ID),
		})
	}
	return ports
}
