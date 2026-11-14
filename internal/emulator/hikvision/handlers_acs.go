package hikvision

// Access control handlers: AcsCfg, storage, door control, remote check and
// the AcsEvent history search.

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (e *Emulator) handleGetAcsCfg(c *gin.Context) {
	localAuth, _ := e.repo.GetSetting("LocalAuthentication")
	remoteCheckDoorEnabled := localAuth == ""

	// checkChannelType echoes what the PUT stored.
	checkChannelType, _ := e.repo.GetSetting("CheckChannelType")
	if checkChannelType == "" {
		checkChannelType = "ISAPIListen"
	}

	c.JSON(http.StatusOK, gin.H{
		"AcsCfg": gin.H{
			"uploadCapPic":                        true,
			"saveCapPic":                          true,
			"protocol":                            "Private",
			"voicePrompt":                         false,
			"showPicture":                         false,
			"showEmployeeNo":                      false,
			"showName":                            true,
			"desensitiseEmployeeNo":               true,
			"desensitiseName":                     true,
			"uploadVerificationPic":               true,
			"saveVerificationPic":                 true,
			"saveFacePic":                         false,
			"remoteCheckDoorEnabled":              remoteCheckDoorEnabled,
			"remoteStandaloneEnabled":             true,
			"remoteCheckSet":                      0,
			"checkChannelType":                    checkChannelType,
			"externalCardReaderEnabled":           false,
			"combinationAuthenticationTimeout":    5,
			"combinationAuthenticationLimitOrder": true,
		},
	})
}

// handleGetAccessControlCapabilities returns the capabilities XML. The only
// field clients typically consume is isSupportRemoteCheck, which enables
// online mode.
func (e *Emulator) handleGetAccessControlCapabilities(c *gin.Context) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<AccessControl version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<isSupportRemoteCheck>true</isSupportRemoteCheck>
</AccessControl>
`
	c.Header("Content-Type", "application/xml")
	c.String(http.StatusOK, body)
}

func (e *Emulator) handlePutAcsCfg(c *gin.Context) {
	var payload struct {
		AcsCfg struct {
			RemoteCheckDoorEnabled bool   `json:"remoteCheckDoorEnabled"`
			CheckChannelType       string `json:"checkChannelType"`
		} `json:"AcsCfg"`
	}

	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"statusCode":    6,
			"statusString":  "Error",
			"subStatusCode": "Invalid request",
		})
		return
	}

	e.tracer.Info("Setting online mode: %v", payload.AcsCfg.RemoteCheckDoorEnabled)
	localAuthValue := "0"
	if !payload.AcsCfg.RemoteCheckDoorEnabled {
		localAuthValue = "1"
	}

	if err := e.repo.SetSetting("LocalAuthentication", localAuthValue); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"statusCode":    6,
			"statusString":  "Error",
			"subStatusCode": err.Error(),
		})
		return
	}

	if payload.AcsCfg.CheckChannelType != "" {
		if err := e.repo.SetSetting("CheckChannelType", payload.AcsCfg.CheckChannelType); err != nil {
			e.tracer.Error("AcsCfg: SetSetting CheckChannelType failed: %v", err)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
	})
}

// handlePutStorageCfg handles PUT /ISAPI/AccessControl/AcsEvent/StorageCfg.
// With mode="time" the client asks the device to delete events older than
// checkTime, after having collected them. With mode="regular" it is just
// configuration.
func (e *Emulator) handlePutStorageCfg(c *gin.Context) {
	var payload struct {
		EventStorageCfg struct {
			Mode      string `json:"mode"`
			Period    int    `json:"period"`
			CheckTime string `json:"checkTime"`
		} `json:"EventStorageCfg"`
	}
	if err := c.BindJSON(&payload); err != nil {
		e.tracer.Error("[StorageCfg] invalid body: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"statusCode":    6,
			"statusString":  "Error",
			"subStatusCode": "Invalid request",
		})
		return
	}

	cfg := payload.EventStorageCfg
	if cfg.Mode == "time" && cfg.CheckTime != "" && e.eventLog != nil {
		// checkTime has no time zone: it is the client's local time. It must be
		// parsed in the same zone the emulator stamps events with, otherwise the
		// purge window shifts and nothing gets deleted.
		cutoff, err := time.ParseInLocation("2006-01-02 15:04:05", cfg.CheckTime, e.location())
		if err != nil {
			e.tracer.Warning("[StorageCfg] unreadable checkTime %q: %v", cfg.CheckTime, err)
		} else {
			n := e.eventLog.PurgeBefore(cutoff)
			e.tracer.Info("[StorageCfg] %d event(s) purged before %s", n, cfg.CheckTime)
		}
	} else {
		e.tracer.Info("[StorageCfg] mode=%s period=%d", cfg.Mode, cfg.Period)
	}

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
	})
}

func (e *Emulator) handleSetDoorParameters(c *gin.Context) {
	c.XML(http.StatusOK, successXMLResponse())
}

func (e *Emulator) handleCommandDoor(c *gin.Context) {
	outputID := c.Param("output_id")
	e.tracer.Info("New command received to output= %s", outputID)
	c.XML(http.StatusOK, successXMLResponse())
}

// handleRemoteCheck receives the server's access decision in online mode. After
// processing the remote-check request emitted by the emulator (an event with
// serialNo and WITHOUT frontSerialNo), the server answers with
// PUT /ISAPI/AccessControl/remoteCheck carrying
// { "RemoteCheck": { "serialNo", "checkResult", "info" } }. This is where the
// authorization actually happens: the real device would open the door on
// checkResult=="success".
func (e *Emulator) handleRemoteCheck(c *gin.Context) {
	var payload struct {
		RemoteCheck struct {
			SerialNo    int    `json:"serialNo"`
			CheckResult string `json:"checkResult"`
			Info        string `json:"info"`
		} `json:"RemoteCheck"`
	}

	if err := c.BindJSON(&payload); err != nil {
		e.tracer.Error("[remoteCheck] invalid body: %v", err)
		writeHikvisionXML(c, http.StatusBadRequest, "6", "Error", "Invalid request")
		return
	}

	rc := payload.RemoteCheck
	e.tracer.Info("[remoteCheck] server decision: serialNo=%d checkResult=%q info=%q", rc.SerialNo, rc.CheckResult, rc.Info)

	// Success => the device would open the door. Simulate the door events to
	// reflect the granted access (non-blocking).
	if strings.EqualFold(rc.CheckResult, "success") {
		e.tracer.Info("[remoteCheck] access granted (serialNo=%d) -> simulating door open", rc.SerialNo)
		go e.simulateDoorEvents()
	}

	writeHikvisionXML(c, http.StatusOK, "1", "OK", "ok")
}

// handlePostAcsEvent answers the offline event search
// (POST /ISAPI/AccessControl/AcsEvent) from the device's event history.
// Clients page 30 at a time and stop when responseStatusStrg == "NO MATCH".
func (e *Emulator) handlePostAcsEvent(c *gin.Context) {
	var payload struct {
		AcsEventCond struct {
			SearchID             string `json:"searchID"`
			SearchResultPosition int    `json:"searchResultPosition"`
			MaxResults           int    `json:"maxResults"`
			Major                int    `json:"major"`
			Minor                int    `json:"minor"`
		} `json:"AcsEventCond"`
	}
	if err := c.BindJSON(&payload); err != nil {
		e.tracer.Error("[AcsEvent] invalid body: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"statusCode":    6,
			"statusString":  "Error",
			"subStatusCode": "Invalid request",
		})
		return
	}

	cond := payload.AcsEventCond
	if cond.MaxResults <= 0 {
		cond.MaxResults = 30
	}

	var recs []AcsEventRecord
	total := 0
	if e.eventLog != nil {
		recs, total = e.eventLog.Page(cond.SearchResultPosition, cond.MaxResults)
	}

	if len(recs) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"AcsEvent": gin.H{
				"searchID":           cond.SearchID,
				"responseStatusStrg": "NO MATCH",
				"numOfMatches":       0,
				"totalMatches":       total,
				"InfoList":           []interface{}{},
			},
		})
		return
	}

	loc := e.location()
	info := make([]gin.H, 0, len(recs))
	for _, r := range recs {
		info = append(info, gin.H{
			"major":             r.Major,
			"minor":             r.Minor,
			"time":              r.Time.In(loc).Format("2006-01-02T15:04:05-07:00"),
			"cardNo":            r.CardNo,
			"cardReaderNo":      r.CardReaderNo,
			"doorNo":            r.DoorNo,
			"name":              r.Name,
			"employeeNoString":  r.EmployeeNo,
			"serialNo":          r.SerialNo,
			"currentVerifyMode": "faceOrFpOrCardOrPw",
		})
	}

	e.tracer.Info("[AcsEvent] offset=%d max=%d -> %d of %d",
		cond.SearchResultPosition, cond.MaxResults, len(info), total)

	c.JSON(http.StatusOK, gin.H{
		"AcsEvent": gin.H{
			"searchID":           cond.SearchID,
			"responseStatusStrg": "OK",
			"numOfMatches":       len(info),
			"totalMatches":       total,
			"InfoList":           info,
		},
	})
}
