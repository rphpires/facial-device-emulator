package hikvision

// This file holds the route table and the response helpers shared by the
// handlers, which are split by area: handlers_system.go, handlers_acs.go,
// handlers_users.go, handlers_cards.go, handlers_faces.go and
// handlers_events.go.

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"

	"github.com/gin-gonic/gin"
)

// XMLResponseStatus is the standard Hikvision XML response.
type XMLResponseStatus struct {
	XMLName       xml.Name `xml:"ResponseStatus"`
	Version       string   `xml:"version,attr"`
	Xmlns         string   `xml:"xmlns,attr"`
	RequestURL    string   `xml:"requestURL"`
	StatusCode    string   `xml:"statusCode"`
	StatusString  string   `xml:"statusString"`
	SubStatusCode string   `xml:"subStatusCode"`
}

// SetupRoutes registers all Hikvision-specific routes.
func (e *Emulator) SetupRoutes(router *gin.Engine) {
	// Emulator status endpoint
	router.GET("/emulator/get-status", e.handleGetStatus)

	// ======================== AccessControl ========================
	acURL := "/ISAPI/AccessControl"

	// AcsCfg
	router.GET(acURL+"/AcsCfg", e.handleGetAcsCfg)
	router.PUT(acURL+"/AcsCfg", e.handlePutAcsCfg)
	router.PUT(acURL+"/AcsEvent/StorageCfg", e.handlePutStorageCfg)
	router.POST(acURL+"/AcsEvent", e.handlePostAcsEvent)
	router.PUT(acURL+"/Door/param/1", e.handleSetDoorParameters)
	router.PUT(acURL+"/RemoteControl/door/:output_id", e.handleCommandDoor)

	// remoteCheck: in online mode the server answers the remote verification
	// request with a PUT carrying serialNo + checkResult (success/failed).
	router.PUT(acURL+"/remoteCheck", e.handleRemoteCheck)

	// Capabilities (used by the client to detect online access support)
	router.GET(acURL+"/capabilities", e.handleGetAccessControlCapabilities)

	// UserInfo
	router.GET(acURL+"/UserInfo/Count", e.handleGetUserCount)
	router.POST(acURL+"/UserInfo/Search", e.handlePostUserSearch)
	router.POST(acURL+"/UserInfo/Record", e.handlePostUserRecord)
	router.PUT(acURL+"/UserInfo/Modify", e.handlePutUserModify)

	// UserInfoDetail
	router.GET(acURL+"/UserInfoDetail/DeleteProcess", e.handleGetUserDeleteProcess)
	router.PUT(acURL+"/UserInfoDetail/Delete", e.handlePutUserDelete)

	// CardInfo
	router.GET(acURL+"/CardInfo/Count", e.handleGetCardCount)
	router.POST(acURL+"/CardInfo/Search", e.handlePostCardSearch)
	router.POST(acURL+"/CardInfo/Record", e.handlePostCardRecord)
	router.PUT(acURL+"/CardInfo/Delete", e.handlePutCardDelete)

	// FingerPrint
	router.POST(acURL+"/FingerPrint/SetUp", e.handlePostFingerprintSetup)
	router.POST(acURL+"/FingerPrintUploadAll", e.handlePostFingerprintUploadAll)
	router.PUT(acURL+"/FingerPrint/Delete", e.handlePutFingerPrintDelete)
	router.POST(acURL+"/CaptureFingerPrint", e.handlePostCaptureFingerPrint)

	// ========================= Intelligent =========================
	intelliURL := "/ISAPI/Intelligent/FDLib"

	router.GET(intelliURL+"/Count", e.handleGetFDLibCount)
	router.POST(intelliURL+"/FDSearch", e.handlePostFingerprintSearch)
	router.GET(intelliURL+"/LOCALS/pic/enrlFace/:user_id", e.handleGetRemoteFace)
	router.PUT(intelliURL+"/FDSearch/Delete", e.handlePutFingerprintDelete)
	router.POST(intelliURL+"/FaceDataRecord", e.handlePostFaceDataRecord)
	router.PUT(intelliURL+"/FDSetUp", e.handlePutFaceSetup)

	// =========================== System ============================
	systemURL := "/ISAPI/System"

	router.GET(systemURL+"/time", e.handleGetDateTime)
	router.PUT(systemURL+"/time", e.handleSetDateTime)
	router.GET(systemURL+"/deviceInfo", e.handleGetDeviceInfo)
	router.PUT(systemURL+"/IO/outputs/:output_id/trigger", e.handleCommandOutput)

	// =========================== Events ============================
	eventURL := "/ISAPI/Event/notification"

	router.GET(eventURL+"/httpHosts", e.handleGetHttpHosts)
	router.PUT(eventURL+"/httpHosts", e.handlePutHttpHosts)
	router.GET(eventURL+"/alertStream", e.handleGetAlertStream)

	// =========================== Status (Custom) ====================
	router.GET("/status", e.handleGetStatus)
	router.GET("/health", e.handleGetStatus) // Alias
}

// writeHikvisionXML writes the response in the exact format a real Hikvision
// device returns (XML prolog and line breaks), avoiding xml.Marshal
// incompatibilities with parsers that expect the firmware's literal text.
func writeHikvisionXML(c *gin.Context, status int, statusCode, statusString, subStatusCode string) {
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ResponseStatus version="1.0" xmlns="http://www.hikvision.com/ver10/XMLSchema">
<requestURL></requestURL>
<statusCode>%s</statusCode>
<statusString>%s</statusString>
<subStatusCode>%s</subStatusCode>
</ResponseStatus>
`, statusCode, statusString, subStatusCode)
	c.Header("Content-Type", "application/xml")
	c.String(status, body)
}

func (e *Emulator) processUploadedImage(fileHeader *multipart.FileHeader) (string, error) {
	// Open the file
	file, err := fileHeader.Open()
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Read the file contents
	imageData, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}

	// Encode as base64
	return base64.StdEncoding.EncodeToString(imageData), nil
}

// Helpers that build XML responses
func successXMLResponse() XMLResponseStatus {
	return XMLResponseStatus{
		Version:       "1.0",
		Xmlns:         "http://www.hikvision.com/ver10/XMLSchema",
		RequestURL:    "",
		StatusCode:    "1",
		StatusString:  "OK",
		SubStatusCode: "ok",
	}
}

func errorXMLResponse(message string) XMLResponseStatus {
	return XMLResponseStatus{
		Version:       "1.0",
		Xmlns:         "http://www.hikvision.com/ver10/XMLSchema",
		RequestURL:    "",
		StatusCode:    "6",
		StatusString:  "Error",
		SubStatusCode: message,
	}
}
