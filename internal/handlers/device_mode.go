package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/rphpires/facial-device-emulator/internal/emulator"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v4"
)

// The two operating modes of a device, stored in emulator.device_settings
// under the LocalAuthentication key:
//
//	online     (0) - the access control server validates the access and replies
//	standalone (1) - the device validates on its own and emits the event
const (
	modeOnline     = "online"
	modeStandalone = "standalone"
)

// modeSettingKey is the cfg_id used in emulator.device_settings. It is the
// same name the Dahua repository reads and writes.
const modeSettingKey = "LocalAuthentication"

// errInvalidMode separates a client error (unknown mode) from an
// infrastructure failure. Without it, a database outage would be reported
// as the caller's fault.
var errInvalidMode = errors.New("invalid mode")

func modeToDB(mode string) (string, error) {
	switch mode {
	case modeOnline:
		return "0", nil
	case modeStandalone:
		return "1", nil
	default:
		return "", fmt.Errorf("%w: %q", errInvalidMode, mode)
	}
}

func modeFromDB(value string) string {
	if value == "1" {
		return modeStandalone
	}
	return modeOnline
}

// getDeviceMode reads the current mode of a device. A device without a
// mode row is standalone (the same default as Repository.GetSetting: the
// console must say what the emulator actually does); a device that does not
// exist is emulator.ErrDeviceNotFound.
func (h *Handler) getDeviceMode(ctx context.Context, deviceID int) (string, error) {
	var value string
	err := h.serviceDB.QueryRow(ctx,
		`SELECT COALESCE(s.value, '1')
		   FROM service.devices d
		   LEFT JOIN emulator.device_settings s
		          ON s.device_id = d.id AND s.cfg_id = $2
		  WHERE d.id = $1`, deviceID, modeSettingKey).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: %d", emulator.ErrDeviceNotFound, deviceID)
	}
	if err != nil {
		return "", err
	}
	return modeFromDB(value), nil
}

// getDeviceModes reads the mode of many devices in a single query; one
// query per device would be hundreds of round trips per page load. A device
// without a row is absent from the map, and callers fall back to
// standalone, like getDeviceMode.
func (h *Handler) getDeviceModes(ctx context.Context, ids []int32) (map[int]string, error) {
	modes := make(map[int]string, len(ids))
	if len(ids) == 0 {
		return modes, nil
	}

	rows, err := h.serviceDB.Query(ctx,
		`SELECT device_id, value FROM emulator.device_settings
		  WHERE cfg_id = $1 AND device_id = ANY($2)`, modeSettingKey, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var value string
		if err := rows.Scan(&id, &value); err != nil {
			return nil, err
		}
		modes[id] = modeFromDB(value)
	}
	return modes, rows.Err()
}

// setDeviceMode stores the mode of a device.
func (h *Handler) setDeviceMode(ctx context.Context, deviceID int, mode string) error {
	value, err := modeToDB(mode)
	if err != nil {
		return err
	}

	// The per-device row usually does not exist yet (the seed only has the
	// global device_id = 0 row), so a plain UPDATE would write nothing.
	// Same UPSERT as Repository.SetSetting, but sourced from service.devices
	// so an unknown ID writes no orphan row: zero rows affected means the
	// device does not exist.
	tag, err := h.serviceDB.Exec(ctx,
		`INSERT INTO emulator.device_settings (device_id, cfg_id, value)
		 SELECT id, $2, $3 FROM service.devices WHERE id = $1
		 ON CONFLICT (device_id, cfg_id) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		deviceID, modeSettingKey, value)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %d", emulator.ErrDeviceNotFound, deviceID)
	}
	return nil
}

// apiGetDeviceMode returns the current mode of the device.
func (h *Handler) apiGetDeviceMode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	mode, err := h.getDeviceMode(c.Request.Context(), id)
	if errors.Is(err, emulator.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		h.tracer.Error("Failed to read device mode for %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not read the device mode"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"mode": mode})
}

// apiSetDeviceMode changes the mode of the device.
func (h *Handler) apiSetDeviceMode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	var body struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if err := h.setDeviceMode(c.Request.Context(), id, body.Mode); err != nil {
		if errors.Is(err, errInvalidMode) {
			c.JSON(http.StatusBadRequest, gin.H{"error": `Invalid mode. Use "online" or "standalone".`})
			return
		}
		if errors.Is(err, emulator.ErrDeviceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		h.tracer.Error("Failed to set device mode for %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not change the device mode"})
		return
	}
	if h.manager != nil {
		h.manager.NotifyChanged(id)
	}
	c.JSON(http.StatusOK, gin.H{"mode": body.Mode})
}
