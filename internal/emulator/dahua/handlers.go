package dahua

import (
	"bufio"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/devicelog"

	"github.com/gin-gonic/gin"
)

// crlf is the HTTP line terminator.
const crlf = "\r\n"

// SetupRoutes registers every Dahua-specific route.
func (e *Emulator) SetupRoutes(router *gin.Engine) {
	// Emulator status endpoint
	router.GET("/emulator/get-status", e.handleGetStatus)

	// ======================== Global ========================
	router.GET("/cgi-bin/global.cgi", e.handleGlobal)

	// ======================== MagicBox ========================
	router.GET("/cgi-bin/magicBox.cgi", e.handleMagicBox)

	// ======================== ConfigManager ========================
	router.GET("/cgi-bin/configManager.cgi", e.handleConfigManager)

	// ======================== AccessControl ========================
	router.GET("/cgi-bin/accessControl.cgi", e.handleAccessControl)

	// ======================== FaceInfoManager ========================
	router.GET("/cgi-bin/FaceInfoManager.cgi", e.handleFaceInfoManagerGet)
	router.POST("/cgi-bin/FaceInfoManager.cgi", e.handleFaceInfoManagerPost)

	// ======================== RecordFinder ========================
	router.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	// ======================== RecordUpdater ========================
	router.GET("/cgi-bin/recordUpdater.cgi", e.handleRecordUpdaterGet)
	router.POST("/cgi-bin/recordUpdater.cgi", e.handleRecordUpdaterPost)

	// ======================== SnapManager (Streaming) ========================
	router.GET("/cgi-bin/snapManager.cgi", e.handleSnapManager)

	// ======================== EventManager (Streaming without snapshot) ========================
	router.GET("/cgi-bin/eventManager.cgi", e.handleEventManager)

	// ======================== AccessFace ========================
	router.GET("/cgi-bin/AccessFace.cgi", e.handleAccessFaceGet)
	router.POST("/cgi-bin/AccessFace.cgi", e.handleAccessFacePost)

	// ======================== AccessUser ========================
	router.GET("/cgi-bin/AccessUser.cgi", e.handleAccessUserGet)

	// NOTE: /cgi-bin/AccessFingerprint.cgi (insertMulti/removeMulti/get) is
	// deliberately not implemented — out of scope, rarely used.
}

// ====================== STATUS HANDLERS ======================

func (e *Emulator) handleGetStatus(c *gin.Context) {
	e.tracer.Info("/emulator/get-status: connect")

	currentTime := time.Now().In(deviceLocation()).Format("2006-01-02 15:04:05")
	count, _ := e.repo.GetTotalUsers()

	c.JSON(http.StatusOK, gin.H{
		"CurrentDatetime": currentTime,
		"TotalUsers":      count,
		"Status":          e.GetStatus(),
		"Model":           "Dahua",
		"MacAddress":      e.macAddress,
		"Version":         "1.0.0",
	})
}

// ====================== GLOBAL HANDLERS ======================

func (e *Emulator) handleGlobal(c *gin.Context) {
	action := c.Query("action")
	timeParam := c.Query("time")
	e.tracer.Info("[CONCURRENT] Global.cgi called: action=%s | time=%s | RemoteAddr=%s", action, timeParam, c.Request.RemoteAddr)

	switch action {
	case "getCurrentTime":
		currentTime := time.Now().In(deviceLocation()).Format("2006-01-02 15:04:05")
		e.handleResponse(c, fmt.Sprintf("result=%s", currentTime), http.StatusOK, 50)
	case "setCurrentTime":
		e.handleResponse(c, "OK", http.StatusOK, 50)
	case "setConfig":
		e.handleResponse(c, "OK", http.StatusOK, 50)
	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// ====================== MAGICBOX HANDLERS ======================

func (e *Emulator) handleMagicBox(c *gin.Context) {
	action := c.Query("action")

	switch action {
	case "getSoftwareVersion":
		e.tracer.Info("Get Software Version: emulator v1.0")
		e.handleResponse(c, "version=Emulator v1.0", http.StatusOK, 80)
	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// ====================== CONFIG MANAGER HANDLERS ======================

func (e *Emulator) handleConfigManager(c *gin.Context) {
	action := c.Query("action")
	e.tracer.Info("[CONCURRENT] ConfigManager called: action=%s | RemoteAddr=%s", action, c.Request.RemoteAddr)

	switch action {
	case "getConfig":
		name := c.Query("name")
		out, ok := e.configs.Render(name)
		if !ok {
			e.tracer.Warning("[ConfigManager] unknown table: %q", name)
			e.handleResponse(c, "Error\r\nUnknown config name", http.StatusBadRequest, 50)
			return
		}
		c.Header("Content-Type", "text/plain; charset=utf-8")
		time.Sleep(450 * time.Millisecond)
		c.String(http.StatusOK, out)

	case "setConfig":
		n := e.configs.SetAllFromQuery(c.Request.URL.Query())
		e.tracer.Info("[ConfigManager] setConfig stored %d key(s): %+v", n, c.Request.URL.Query())
		e.applyNetworkConfig()
		e.handleResponse(c, "OK", http.StatusOK, 50)

	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// applyNetworkConfig applies the setConfig keys that change behavior to the
// emulator state: the online push target and the operating mode
// (PictureHttpUpload.* and Intelbras_ModeCfg.DeviceMode).
func (e *Emulator) applyNetworkConfig() {
	if e.repo == nil || e.configs == nil {
		return
	}

	addr := e.configs.Get("PictureHttpUpload.UploadServerList[0].Address")
	if addr != "" {
		_ = e.repo.SetSetting("RemoteServer", addr)
	}
	port := e.configs.Get("PictureHttpUpload.UploadServerList[0].Port")
	if port != "" {
		_ = e.repo.SetSetting("RemotePort", port)
	}
	e.setRemote(addr, port)

	if path := e.configs.Get("PictureHttpUpload.UploadServerList[0].Uploadpath"); path != "" {
		_ = e.repo.SetSetting("RemoteUploadPath", path)
	}

	// Operating mode. PictureHttpUpload.Enable is the legacy source;
	// Intelbras_ModeCfg.DeviceMode (0 OFFLINE, 1 POST_EVENTS, 2 ONLINE) takes
	// precedence when present, since that is what newer clients use.
	localAuth := ""
	switch strings.ToLower(e.configs.Get("PictureHttpUpload.Enable")) {
	case "true", "1":
		localAuth = "0"
	case "false", "0":
		localAuth = "1"
	}
	switch e.configs.Get("Intelbras_ModeCfg.DeviceMode") {
	case "2":
		localAuth = "0"
	case "0":
		localAuth = "1"
	}
	if localAuth != "" {
		e.tracer.Info("[ConfigManager] LocalAuthentication=%s", localAuth)
		_ = e.repo.SetSetting("LocalAuthentication", localAuth)
		e.localAuth.Store(&localAuth)
	}
}

// ====================== ACCESS CONTROL HANDLERS ======================

func (e *Emulator) handleAccessControl(c *gin.Context) {
	action := c.Query("action")
	channel := c.Query("channel")

	switch action {
	case "openDoor":
		e.tracer.Info("Command openDoor output: %s", channel)
		e.handleResponse(c, "OK", http.StatusOK, 80)
	case "closeDoor":
		e.tracer.Info("Command closeDoor output: %s", channel)
		e.handleResponse(c, "OK", http.StatusOK, 80)
	case "captureFingerprint":
		// The device answers OK immediately and, seconds later, pushes an
		// event with CollectResult on the stream — that event is what
		// unblocks the client's fingerprint enrollment.
		userID := c.Query("info.UserID")
		e.tracer.Info("Command captureFingerprint: readerID=%s userID=%s timeout=%s",
			c.Query("info.ReaderID"), userID, c.Query("timeout"))
		if e.enrollFn != nil {
			e.enrollFn(userID)
		}
		e.handleResponse(c, "OK", http.StatusOK, 80)
	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// ====================== FACE INFO MANAGER HANDLERS ======================

func (e *Emulator) handleFaceInfoManagerGet(c *gin.Context) {
	action := c.Query("action")

	switch action {
	case "startFind":
		time.Sleep(500 * time.Millisecond)

		// Measured on the bench: with Condition.UserID present, Total is 0
		// or 1 FOR THAT UserID - not the device's global face count.
		// Clients use Total to decide HasFace (vendor spec, section 12.3.4:
		// "Total ... return 0 if not found"); returning the global count
		// here would make HasFace never false, even after a remove.
		// Without Condition.UserID the global count is correct (there is
		// no UserID to filter by).
		var response *FindFaceResponse
		var err error
		if userIDStr := c.Query("Condition.UserID"); userIDStr != "" {
			userID, _ := strconv.Atoi(userIDStr)
			response, err = e.repo.FindRemoteFacesByUserID(userID)
		} else {
			response, err = e.repo.FindRemoteFaces()
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, response)

	case "doFind":
		time.Sleep(50 * time.Millisecond)
		count, _ := strconv.Atoi(c.Query("Count"))
		offset, _ := strconv.Atoi(c.Query("Offset"))

		response, err := e.repo.GetRemoteFaces(count, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, response)

	case "stopFind":
		e.handleResponse(c, "OK", http.StatusOK, 50)

	case "remove":
		// Idempotent on the real device: a UserID without a face returns
		// 200 OK, not an error. RemoveFace is a plain DELETE - zero affected
		// rows is not an error in Postgres, so no special handling needed.
		time.Sleep(50 * time.Millisecond)
		userID, _ := strconv.Atoi(c.Query("UserID"))
		err := e.repo.RemoveFace(userID)
		if err != nil {
			e.handleResponse(c, "Error", http.StatusInternalServerError, 50)
			return
		}
		e.handleResponse(c, "OK", http.StatusOK, 50)

	case "list", "find", "getCollect":
		// Measured on the bench: all three return 501 on the real device.
		e.handleResponse(c, "Error\nNot Implemented!", http.StatusNotImplemented, 50)

	default:
		e.handleResponse(c, "Invalid action", http.StatusBadRequest, 50)
	}
}

func (e *Emulator) handleFaceInfoManagerPost(c *gin.Context) {
	action := c.Query("action")

	var requestData struct {
		UserID flexInt `json:"UserID"`
		Info   struct {
			PhotoData flexStrings `json:"PhotoData"`
		} `json:"Info"`
	}

	if err := c.ShouldBindJSON(&requestData); err != nil {
		// Without this log, a face rejected for its payload format vanishes
		// without a trace and the UI shows the user as having no face.
		e.tracer.Error("[FACE] bind failed: action=%s CT=%q err=%v",
			action, c.Request.Header.Get("Content-Type"), err)
		e.handleResponse(c, "Invalid JSON", http.StatusBadRequest, 50)
		return
	}

	userID := int(requestData.UserID)
	var photoData string
	if len(requestData.Info.PhotoData) > 0 {
		photoData = requestData.Info.PhotoData[0]
	}

	switch action {
	case "add":
		// Compute the image MD5
		var md5Hash string
		if photoData != "" {
			imageBytes, err := base64.StdEncoding.DecodeString(photoData)
			if err == nil {
				hash := md5.Sum(imageBytes)
				md5Hash = fmt.Sprintf("%X", hash)
			}
		}

		e.tracer.Info("Add Face: UserID=%d, md5=%s", userID, md5Hash)
		err := e.repo.AddFace(userID, md5Hash)
		if err != nil {
			e.tracer.Error("[FACE] AddFace failed: UserID=%d err=%v", userID, err)
			e.handleResponse(c, "Error", http.StatusInternalServerError, 550)
			return
		}
		e.handleResponse(c, "OK", http.StatusOK, 550)

	case "update":
		// Compute the image MD5
		var md5Hash string
		if photoData != "" {
			imageBytes, err := base64.StdEncoding.DecodeString(photoData)
			if err == nil {
				hash := md5.Sum(imageBytes)
				md5Hash = fmt.Sprintf("%X", hash)
			}
		}

		e.tracer.Info("Update Face: UserID=%d, md5=%s", userID, md5Hash)

		// Remove and add again
		_ = e.repo.RemoveFace(userID) // a missing face is fine: AddFace follows
		err := e.repo.AddFace(userID, md5Hash)
		if err != nil {
			e.tracer.Error("[FACE] UpdateFace failed: UserID=%d err=%v", userID, err)
			e.handleResponse(c, "Error", http.StatusInternalServerError, 600)
			return
		}
		e.handleResponse(c, "OK", http.StatusOK, 600)

	default:
		e.tracer.Error("[FACE] unknown action=%q (UserID=%d, photo=%d bytes)",
			action, userID, len(photoData))
		e.handleResponse(c, "Invalid action", http.StatusBadRequest, 50)
	}
}

// ====================== RECORD FINDER HANDLERS ======================

// errCodeUnknownTable is the code the real device returns when `name` is not
// a table it knows. Measured on the bench with name=AccessFace.
const errCodeUnknownTable = 285278242

func (e *Emulator) handleRecordFinder(c *gin.Context) {
	action := c.Query("action")
	name := c.Query("name")

	e.tracer.Info("RecordFinder: action=%s, name=%s", action, name)

	// AccessControlCardRec is the access history; AccessControlCard is the
	// card table. Serving cards for both would make clients read them as
	// events, producing phantom offline events on every scan.
	if name == "AccessControlCardRec" {
		e.handleRecordFinderCardRec(c, action)
		return
	}

	if name != "AccessControlCard" {
		// Measured on the bench (name=AccessFace): the real device does not
		// return a plain-text "Unknown action"/400 for an unknown table -
		// it returns this specific JSON envelope, for both find and
		// getQuerySize.
		e.tracer.Warning("[RecordFinder] unknown table: %q", name)
		e.writeErrorCode(c, http.StatusBadRequest, errCodeUnknownTable)
		return
	}

	switch action {
	case "find":
		if userIDStr := c.Query("condition.UserID"); userIDStr != "" {
			userID, _ := strconv.Atoi(userIDStr)
			response, err := e.repo.FindCard(userID)
			if err != nil {
				e.handleResponse(c, "Error", http.StatusInternalServerError, 60)
				return
			}
			e.handleResponse(c, response, http.StatusOK, 60)
			return
		}

		// Without condition.UserID: measured on the bench, offset=0, 100,
		// 260, 500 and 12000 ALL return found=50 with the same first record
		// (RecNo=15) - i.e. the real device IGNORES offset for this action
		// and only applies count from the start of the table.
		count, _ := strconv.Atoi(c.Query("count"))
		response, err := e.repo.GetCards(count, 0)
		if err != nil {
			e.handleResponse(c, "Error", http.StatusInternalServerError, 60)
			return
		}
		e.handleResponse(c, response, http.StatusOK, 60)

	case "doSeekFind":
		// doSeekFind pages correctly with offset/count - unlike find, which
		// ignores offset (see above). GetCards uses Postgres LIMIT/OFFSET,
		// which naturally ends on the last partial page and returns found=0
		// past the end of the table.
		offset, _ := strconv.Atoi(c.Query("offset"))
		count, _ := strconv.Atoi(c.Query("count"))
		response, err := e.repo.GetCards(count, offset)
		if err != nil {
			e.handleResponse(c, "Error", http.StatusInternalServerError, 350)
			return
		}
		e.handleResponse(c, response, http.StatusOK, 350)

	case "getQuerySize":
		if e.countCardsFn == nil {
			e.handleResponse(c, "Error", http.StatusInternalServerError, 50)
			return
		}
		n, err := e.countCardsFn()
		if err != nil {
			e.handleResponse(c, "Error", http.StatusInternalServerError, 50)
			return
		}
		// Measured on the bench: BOTH lines come in the body (Size= followed
		// by count=), not just count=.
		e.handleResponse(c, fmt.Sprintf("Size=%d\ncount=%d", n, n), http.StatusOK, 50)

	case "startFind", "stopFind":
		// Measured on the bench: plain 501, not 400. AccessControlCard does
		// not support these actions' stateful paging - only doSeekFind/find.
		e.handleResponse(c, "Error\nNot Implemented!", http.StatusNotImplemented, 50)

	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// handleRecordFinderCardRec serves queries on the access history
// (AccessControlCardRec table). Clients send StartTime/EndTime as epoch
// seconds and read found=N followed by records[i].*.
func (e *Emulator) handleRecordFinderCardRec(c *gin.Context, action string) {
	if e.eventLog == nil {
		e.handleResponse(c, "found=0", http.StatusOK, 50)
		return
	}

	switch action {
	case "find", "doSeekFind":
		startEpoch, _ := strconv.ParseInt(c.Query("StartTime"), 10, 64)
		endEpoch, _ := strconv.ParseInt(c.Query("EndTime"), 10, 64)
		if endEpoch == 0 {
			endEpoch = time.Now().Unix()
		}
		recs := e.eventLog.Between(time.Unix(startEpoch, 0), time.Unix(endEpoch, 0))
		e.tracer.Info("RecordFinder[CardRec]: %d event(s) between %d and %d", len(recs), startEpoch, endEpoch)
		e.handleResponse(c, e.eventLog.Render(recs), http.StatusOK, 60)

	case "getQuerySize":
		e.handleResponse(c, fmt.Sprintf("count=%d", e.eventLog.Count()), http.StatusOK, 50)

	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// ====================== RECORD UPDATER HANDLERS ======================

func (e *Emulator) handleRecordUpdaterGet(c *gin.Context) {
	e.tracer.Info("[GET] /recordUpdater.cgi: %+v", c.Request.URL.Query())
	action := c.Query("action")

	switch action {
	case "remove":
		recNo, _ := strconv.Atoi(c.Query("recno"))

		// The real device deletes the face together with the card — which
		// is why clients proactively resend the face after a card update
		// (delete+insert).
		if userID, err := e.repo.GetCardUserIDByRecNo(recNo); err == nil && userID != 0 {
			if err := e.repo.RemoveFace(userID); err != nil {
				e.tracer.Warning("[recordUpdater.remove] face of UserID=%d not removed: %v", userID, err)
			} else {
				e.tracer.Info("[recordUpdater.remove] face of UserID=%d removed along with the card", userID)
			}
		}

		if err := e.repo.RemoveCard(recNo); err != nil {
			e.handleResponse(c, "Error", http.StatusInternalServerError, 350)
			return
		}
		e.handleResponse(c, "OK", http.StatusOK, 350)

	case "insert":
		e.handleCardInsert(c)

	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

func (e *Emulator) handleRecordUpdaterPost(c *gin.Context) {
	e.tracer.Info("[POST] /recordUpdater.cgi: %+v", c.Request.URL.Query())
	action := c.Query("action")

	switch action {
	case "insert":
		e.handleCardInsert(c)
	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// maxCardNameBytes is the bench-measured limit for CardName, in UTF-8 BYTES -
// not runes - and the device REJECTS an overlong name rather than truncating:
//
//	32 x 'A' (32 bytes)             -> accepted, RecNo=<n>
//	33 x 'A' (33 bytes)             -> 400 "Error\nBad Request!"
//	16 x U+00E1 (32 bytes, 2B/rune) -> accepted
//	32 x U+00E1 (64 bytes)          -> 400
//	16 'A' + 16 U+00E1 (48 bytes)   -> 400
//
// Counting runes or truncating would both diverge from the real device:
// with truncation the emulator would answer 200 where the device answers 400,
// so a client bug producing an overlong name would pass against the emulator
// and fail in the field.
//
// The field is raw UTF-8 and is not sanitized/normalized: plain ASCII names,
// accented names and names with apostrophes, parentheses, commas, periods and
// hyphens all round-tripped byte-for-byte on the bench, as long as they fit
// the byte limit.
const maxCardNameBytes = 32

func (e *Emulator) handleCardInsert(c *gin.Context) {
	cardName := c.Query("CardName")
	userID, _ := strconv.Atoi(c.Query("UserID"))
	cardNo := c.Query("CardNo")
	validDateStart := c.Query("ValidDateStart")
	validDateEnd := c.Query("ValidDateEnd")

	e.tracer.Info("recordUpdater..insert: CardName=%s, UserID=%d, CardNo=%s", cardName, userID, cardNo)

	if len(cardName) > maxCardNameBytes {
		// Measured on the bench: the device rejects, it does not truncate -
		// see maxCardNameBytes.
		e.handleResponse(c, "Error\nBad Request!", http.StatusBadRequest, 100)
		return
	}

	// Parse dates
	startTime, _ := time.Parse("2006-01-02 15:04:05", validDateStart)
	endTime, _ := time.Parse("2006-01-02 15:04:05", validDateEnd)

	recNo, err := e.repo.AddCard(cardName, userID, cardNo, startTime, endTime)
	if err != nil {
		switch {
		case errors.Is(err, ErrCardUserIDTaken):
			// Measured on the bench: the real device accepts only ONE
			// AccessControlCard row per UserID.
			e.writeErrorCode(c, http.StatusBadRequest, 286064929)
		case errors.Is(err, ErrCardNoTaken):
			// Measured on the bench: CardNo already belongs to another UserID.
			e.writeErrorCode(c, http.StatusBadRequest, 286064930)
		default:
			e.handleResponse(c, "Error\nBad Request!", http.StatusBadRequest, 100)
		}
		return
	}

	msg := fmt.Sprintf("RecNo=%d", recNo)
	e.handleResponse(c, msg, http.StatusOK, 100)
}

// ====================== SNAP MANAGER (STREAMING) ======================

func (e *Emulator) handleSnapManager(c *gin.Context) {
	e.tracer.Info("=== [GET] /cgi-bin/snapManager.cgi - CLIENT CONNECTED ===")
	e.tracer.Info("Client IP: %s", c.ClientIP())
	e.tracer.Info("User-Agent: %s", c.Request.UserAgent())
	e.tracer.Info("Remote Addr: %s", c.Request.RemoteAddr)
	e.tracer.Info("HTTP Proto: %s", c.Request.Proto)
	e.tracer.Info("Connection header: %s", c.Request.Header.Get("Connection"))

	// Hijack the connection to avoid Transfer-Encoding: chunked;
	// a real Dahua device does not use chunked encoding
	hijacker, ok := c.Writer.(http.Hijacker)
	if !ok {
		e.tracer.Error("Hijacking not supported")
		c.String(http.StatusInternalServerError, "Hijacking not supported")
		return
	}

	conn, bufrw, err := hijacker.Hijack()
	if err != nil {
		e.tracer.Error("Hijack failed: %v", err)
		c.String(http.StatusInternalServerError, "Hijack failed")
		return
	}
	defer conn.Close()
	devicelog.MarkStream(c)
	remote := conn.RemoteAddr().String()
	e.dlog.StreamOpen(remote, c.Request.URL.RequestURI())
	defer e.dlog.StreamClose(remote)

	e.tracer.Info("Connection hijacked successfully, writing raw HTTP response...")

	// Write the raw HTTP response (like a real Dahua device)
	httpResponse := "HTTP/1.1 200 OK\r\n" +
		"Cache-Control: no-cache\r\n" +
		"Pragma: no-cache\r\n" +
		"Connection: Keep-Alive\r\n" +
		"Content-Type: multipart/x-mixed-replace; boundary=myboundary\r\n" +
		"\r\n"

	if err := writeFlush(bufrw, []byte(httpResponse)); err != nil {
		e.tracer.Error("Failed to write HTTP headers: %v", err)
		return
	}

	e.tracer.Info("Headers sent, starting event stream handler...")
	e.handleEventStream(conn, bufrw, true)
	e.tracer.Info("=== Event stream handler finished ===")
}

// handleEventManager serves GET /cgi-bin/eventManager.cgi?action=attach.
// It is the stream clients use when they are NOT saving snapshots. Same
// transport as snapManager - multipart with boundary "myboundary" over a
// hijacked connection, no chunked encoding - but the client only expects the
// text/plain parts.
func (e *Emulator) handleEventManager(c *gin.Context) {
	action := c.Query("action")
	if action != "attach" {
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
		return
	}

	e.tracer.Info("=== [GET] /cgi-bin/eventManager.cgi - CLIENT CONNECTED (codes=%s) ===", c.Query("codes"))

	hijacker, ok := c.Writer.(http.Hijacker)
	if !ok {
		e.tracer.Error("Hijacking not supported")
		c.String(http.StatusInternalServerError, "Hijacking not supported")
		return
	}
	conn, bufrw, err := hijacker.Hijack()
	if err != nil {
		e.tracer.Error("Hijack failed: %v", err)
		return
	}
	defer conn.Close()
	devicelog.MarkStream(c)
	remote := conn.RemoteAddr().String()
	e.dlog.StreamOpen(remote, c.Request.URL.RequestURI())
	defer e.dlog.StreamClose(remote)

	httpResponse := "HTTP/1.1 200 OK" + crlf +
		"Cache-Control: no-cache" + crlf +
		"Pragma: no-cache" + crlf +
		"Connection: Keep-Alive" + crlf +
		"Content-Type: multipart/x-mixed-replace; boundary=myboundary" + crlf +
		crlf
	if err := writeFlush(bufrw, []byte(httpResponse)); err != nil {
		e.tracer.Error("Failed to write HTTP headers: %v", err)
		return
	}

	e.handleEventStream(conn, bufrw, false)
	e.tracer.Info("=== eventManager stream finished ===")
}

// Stream loop timing. The heartbeat goes out after this much idle time; the
// LocalAuthentication setting is re-read on the slower refresh tick (the
// console can change it in the database without telling the emulator).
const (
	streamHeartbeatInterval = 10 * time.Second
	streamModeRefresh       = 5 * time.Second
)

// writeFlush writes one part to the hijacked connection and flushes it. An
// error means the client is gone.
func writeFlush(w *bufio.ReadWriter, data []byte) error {
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Flush()
}

// streamHeartbeat is the real device's idle part.
func streamHeartbeat() []byte {
	const text = "Heartbeat"
	return []byte(fmt.Sprintf("\r\n--myboundary\r\nContent-Type: text/plain\r\nContent-Length:%d\r\n\r\n%s\r\n\r\n",
		len(text), text))
}

// handleEventStream writes the multipart parts to the hijacked socket.
// withImage distinguishes the two consumers: snapManager.cgi expects the
// text/plain + image/jpeg pair; eventManager.cgi (used when the client does
// NOT save snapshots) expects only the text part.
//
// The loop blocks on its channels: the run's stop channel, pushed parts
// (the fingerprint capture event), the event interval, the heartbeat and the
// mode refresh. It returns when the emulator stops, when the device leaves
// standalone mode, or when a write or flush fails (client disconnected).
func (e *Emulator) handleEventStream(conn net.Conn, bufrw *bufio.ReadWriter, withImage bool) {
	stop := e.runStop()
	remote := conn.RemoteAddr().String()
	e.tracer.Info("Event stream started (interval %ds, image=%v)", e.device.EventInterval, withImage)

	e.refreshLocalAuth()

	heartbeat := time.NewTicker(streamHeartbeatInterval)
	defer heartbeat.Stop()
	modeRefresh := time.NewTicker(streamModeRefresh)
	defer modeRefresh.Stop()

	var eventTick <-chan time.Time
	if e.device.EventInterval > 0 {
		t := time.NewTicker(time.Duration(e.device.EventInterval) * time.Second)
		defer t.Stop()
		eventTick = t.C
	}

	for {
		select {
		case <-stop:
			e.tracer.Info("Event stream stopped due to emulator shutdown")
			return

		case part := <-e.pushChan:
			if err := writeFlush(bufrw, part); err != nil {
				e.tracer.Error("Failed to write pushed part: %v", err)
				return
			}
			e.dlog.StreamEvent(remote, devicelog.KeyFields("", part, nil), len(part))
			heartbeat.Reset(streamHeartbeatInterval)

		case <-eventTick:
			textPart, imagePart, err := e.generateRandomEventParts()
			if err != nil {
				e.tracer.Error("Failed to generate random event: %v", err)
				continue
			}
			if textPart == nil || imagePart == nil {
				continue // not in standalone mode
			}
			if err := writeFlush(bufrw, textPart); err != nil {
				e.tracer.Error("Failed to write text part: %v", err)
				return
			}
			size := len(textPart)
			if withImage {
				if err := writeFlush(bufrw, imagePart); err != nil {
					e.tracer.Error("Failed to write image part: %v", err)
					return
				}
				size += len(imagePart)
			}
			e.dlog.StreamEvent(remote, devicelog.KeyFields("", textPart, nil), size)
			heartbeat.Reset(streamHeartbeatInterval)

		case <-heartbeat.C:
			if err := writeFlush(bufrw, streamHeartbeat()); err != nil {
				e.tracer.Error("Failed to write heartbeat: %v", err)
				return
			}

		case <-modeRefresh.C:
			e.refreshLocalAuth()
			if e.cachedLocalAuth() == "0" {
				e.tracer.Info("Local authentication disabled, stopping event stream")
				return
			}
		}
	}
}

// ====================== HELPER FUNCTIONS ======================

// handleResponse writes a plain-text response after a simulated latency.
func (e *Emulator) handleResponse(c *gin.Context, content string, statusCode int, latencySleep int) {
	c.Header("Content-Type", "text/plain; charset=utf-8")

	// Simulate device latency
	time.Sleep(time.Duration(latencySleep) * time.Millisecond)

	c.String(statusCode, content)
}
