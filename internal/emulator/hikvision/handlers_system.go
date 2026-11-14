package hikvision

// System handlers: emulator status, device time, device info, outputs.

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/devicetime"

	"github.com/gin-gonic/gin"
)

func (e *Emulator) handleGetStatus(c *gin.Context) {
	e.tracer.Info("[STATUS] Status request received")

	currentTime := time.Now().In(e.location()).Format("2006-01-02 15:04:05")
	count, err := e.repo.GetTotalUsers()

	uptime := int64(0)
	if started := e.startedAt.Load(); started != 0 {
		uptime = int64(time.Since(time.Unix(0, started)).Seconds())
	}

	response := gin.H{
		"device_id":      e.device.ID,
		"device_name":    e.device.Name,
		"port":           e.device.Port,
		"model":          "Hikvision",
		"status":         e.GetStatus(),
		"is_running":     e.IsRunning(),
		"total_users":    count,
		"uptime_seconds": uptime,
		"current_time":   currentTime,
		"mac_address":    e.macAddress,
		"version":        "1.0.0",
		"event_interval": e.device.EventInterval,
	}

	if err != nil {
		response["total_users_error"] = err.Error()
		e.tracer.Error("[STATUS] Failed to get total users: %v", err)
	}

	c.JSON(http.StatusOK, response)
}

func (e *Emulator) handleGetDateTime(c *gin.Context) {
	e.tracer.Info("Polling message received")
	now := time.Now().In(e.location())
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Time version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<timeMode>manual</timeMode>
<localTime>%s</localTime>
<timeZone>%s</timeZone>
</Time>
`, now.Format("2006-01-02T15:04:05-07:00"), devicetime.FormatHikvisionTimeZone(now))
	c.Header("Content-Type", "application/xml")
	c.String(http.StatusOK, body)
}

// handleSetDateTime accepts PUT /ISAPI/System/time. The device clock itself is
// not emulated (it always follows the host clock), but the <timeZone> the
// client sets is kept, in memory, and used from then on to stamp events.
func (e *Emulator) handleSetDateTime(c *gin.Context) {
	var payload struct {
		TimeZone string `xml:"timeZone"`
	}
	if body, err := io.ReadAll(c.Request.Body); err == nil && len(body) > 0 {
		if err := xml.Unmarshal(body, &payload); err != nil {
			e.tracer.Warning("[System/time] unreadable body: %v", err)
		}
	}
	if payload.TimeZone != "" {
		loc, err := devicetime.ParseHikvisionTimeZone(payload.TimeZone)
		if err != nil {
			e.tracer.Warning("[System/time] %v", err)
		} else {
			e.timeZone.Store(loc)
			e.tracer.Info("[System/time] time zone set to %s (UTC%s)", payload.TimeZone, loc.String())
		}
	}
	writeHikvisionXML(c, http.StatusOK, "1", "OK", "ok")
}

// Serial number parts. A Hikvision serial is model + "-" + region + build
// date (yyyymmdd) + "V" + firmware + batch code + unit number. The emulator
// keeps that shape, so clients that parse or validate it accept it, but
// zeroes the date and firmware and uses "EM"/"EMU" for region and batch, so
// it never matches a real unit or firmware release.
const (
	serialModel = "DS-K1T673DX"
	serialBuild = "EM00000000V000000" // region "EM", build date and firmware zeroed
	serialBatch = "EMU"
)

// serialNumber returns the device's synthetic serial, for example
// DS-K1T673DX-EM00000000V000000EMU0000042. The unit number is the emulated
// device's ID, so each emulator is unique.
func (e *Emulator) serialNumber() string {
	return fmt.Sprintf("%s-%s%s%07d", serialModel, serialBuild, serialBatch, e.device.ID)
}

func (e *Emulator) handleGetDeviceInfo(c *gin.Context) {
	xmlContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DeviceInfo version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
    <deviceName>subdoorOne</deviceName>
    <deviceID>255</deviceID>
    <model>DS-K1T673DX-BR</model>
    <serialNumber>%s</serialNumber>
    <macAddress>%s</macAddress>
    <firmwareVersion>V3.18.0</firmwareVersion>
    <firmwareReleasedDate>build 240206</firmwareReleasedDate>
    <encoderVersion>V2.7</encoderVersion>
    <encoderReleasedDate>build 240122</encoderReleasedDate>
    <deviceType>ACS</deviceType>
    <subDeviceType>accessControlTerminal</subDeviceType>
    <telecontrolID>1</telecontrolID>
    <localZoneNum>2</localZoneNum>
    <alarmOutNum>1</alarmOutNum>
    <relayNum>2</relayNum>
    <electroLockNum>1</electroLockNum>
    <RS485Num>1</RS485Num>
    <manufacturer>Go Emulator</manufacturer>
    <OEMCode>1</OEMCode>
    <customizedInfo>DZP20240101001</customizedInfo>
    <bspVersion>V1.17.0.642101 build 2023-11-29</bspVersion>
    <dspVersion>V2.7</dspVersion>
    <marketType>2</marketType>
    <productionDate>2023-04-28</productionDate>
</DeviceInfo>`, e.serialNumber(), strings.ToUpper(e.macAddress))

	c.Header("Content-Type", "application/xml")
	c.String(http.StatusOK, xmlContent)
}

func (e *Emulator) handleCommandOutput(c *gin.Context) {
	outputID := c.Param("output_id")
	e.tracer.Info("Receiving command for output: %s", outputID)
	writeHikvisionXML(c, http.StatusOK, "1", "OK", "ok")
}
