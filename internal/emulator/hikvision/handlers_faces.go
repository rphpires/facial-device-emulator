package hikvision

// Biometric handlers: FDLib faces and fingerprints.

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (e *Emulator) handlePostFingerprintSetup(c *gin.Context) {
	writeHikvisionXML(c, http.StatusOK, "1", "OK", "ok")
}

func (e *Emulator) handlePostFingerprintUploadAll(c *gin.Context) {
	counts, err := e.repo.CountItems()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
		"FDRecordDataInfo": []gin.H{
			{
				"FDID":             "1",
				"faceLibType":      "blackFD",
				"name":             "",
				"recordDataNumber": counts.Fingerprints,
			},
			{
				"FDID":             "2",
				"faceLibType":      "infraredFD",
				"name":             "",
				"recordDataNumber": 0,
			},
		},
	})
}

func (e *Emulator) handleGetFDLibCount(c *gin.Context) {
	counts, err := e.repo.CountItems()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
		"FDRecordDataInfo": []gin.H{
			{
				"FDID":             "1",
				"faceLibType":      "blackFD",
				"name":             "",
				"recordDataNumber": counts.Faces,
			},
			{
				"FDID":             "2",
				"faceLibType":      "infraredFD",
				"name":             "",
				"recordDataNumber": 0,
			},
		},
	})
}

func (e *Emulator) handlePostFingerprintSearch(c *gin.Context) {
	var payload struct {
		MaxResults           int `json:"maxResults"`
		SearchResultPosition int `json:"searchResultPosition"`
	}

	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	faces, err := e.repo.GetFaces(payload.MaxResults, payload.SearchResultPosition)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Convert to response format
	deviceURL := fmt.Sprintf("http://%s:%d/ISAPI/Intelligent/FDLib/LOCALS/pic/enrlFace", e.device.IPAddress, e.device.Port)
	var faceInfos []FaceInfo
	for _, face := range faces {
		faceInfo := FaceInfo{
			FPID:      strconv.Itoa(face.UserID),
			FaceURL:   fmt.Sprintf("%s/%d", deviceURL, face.UserID),
			ModelData: "",
		}
		faceInfos = append(faceInfos, faceInfo)
	}

	// Get total faces
	counts, _ := e.repo.CountItems()

	c.JSON(http.StatusOK, FaceSearchResponse{
		SearchID:           "1",
		ResponseStatusStrg: "MORE",
		NumOfMatches:       len(faceInfos),
		TotalMatches:       counts.Faces,
		MatchList:          faceInfos,
	})
}

func (e *Emulator) handleGetRemoteFace(c *gin.Context) {
	userID := c.Param("user_id")

	photoData, err := e.repo.GetFace(userID)
	if err != nil {
		c.String(http.StatusNotFound, "Face not found")
		return
	}

	// Decode the image
	imageData, err := base64.StdEncoding.DecodeString(photoData)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to decode image")
		return
	}

	// Send the image as the response
	c.Data(http.StatusOK, "image/jpeg", imageData)
}

func (e *Emulator) handlePutFingerprintDelete(c *gin.Context) {
	var payload struct {
		FPID []struct {
			Value string `json:"value"`
		} `json:"FPID"`
	}

	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(payload.FPID) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No FPID provided"})
		return
	}

	if err := e.repo.DeleteFace(payload.FPID[0].Value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	writeHikvisionXML(c, http.StatusOK, "1", "OK", "ok")
}

func (e *Emulator) handlePostFaceDataRecord(c *gin.Context) {
	// Extract the form data
	faceDataRecord := c.PostForm("FaceDataRecord")
	if faceDataRecord == "" {
		// Without this log a face rejected here vanishes without a trace and the
		// user shows up as having no face.
		e.tracer.Error("[FACE] %s %s: missing FaceDataRecord (CT=%q)",
			c.Request.Method, c.Request.URL.Path, c.Request.Header.Get("Content-Type"))
		c.JSON(http.StatusBadRequest, gin.H{"error": "No FaceDataRecord provided"})
		return
	}

	// Parse the JSON data
	var faceData struct {
		FPID string `json:"FPID"`
	}
	if err := json.Unmarshal([]byte(faceDataRecord), &faceData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get the image file
	fileHeader, err := c.FormFile("FaceImage")
	if err != nil {
		e.tracer.Error("[FACE] %s %s: missing FaceImage for FPID=%s: %v",
			c.Request.Method, c.Request.URL.Path, faceData.FPID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Process the image
	base64Image, err := e.processUploadedImage(fileHeader)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Check whether it already exists
	exists, err := e.repo.CheckIfFaceExists(faceData.FPID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if exists {
		c.JSON(http.StatusBadRequest, gin.H{
			"statusCode":    6,
			"statusString":  "Invalid Content",
			"subStatusCode": "cardNoAlreadyExist",
			"errorCode":     1610637363,
			"errorMsg":      "checkEmployeeNo",
		})
		return
	}

	if err := e.repo.AddFace(faceData.FPID, base64Image); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
	})
}

func (e *Emulator) handlePutFaceSetup(c *gin.Context) {
	// Extract the form data
	faceDataRecord := c.PostForm("FaceDataRecord")
	if faceDataRecord == "" {
		// Without this log a face rejected here vanishes without a trace and the
		// user shows up as having no face.
		e.tracer.Error("[FACE] %s %s: missing FaceDataRecord (CT=%q)",
			c.Request.Method, c.Request.URL.Path, c.Request.Header.Get("Content-Type"))
		c.JSON(http.StatusBadRequest, gin.H{"error": "No FaceDataRecord provided"})
		return
	}

	// Parse the JSON data
	var faceData struct {
		FPID string `json:"FPID"`
	}
	if err := json.Unmarshal([]byte(faceDataRecord), &faceData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get the image file
	fileHeader, err := c.FormFile("FaceImage")
	if err != nil {
		e.tracer.Error("[FACE] %s %s: missing FaceImage for FPID=%s: %v",
			c.Request.Method, c.Request.URL.Path, faceData.FPID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Process the image
	base64Image, err := e.processUploadedImage(fileHeader)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := e.repo.UpdateFace(faceData.FPID, base64Image); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
	})
}

// handlePutFingerPrintDelete handles PUT /ISAPI/AccessControl/FingerPrint/Delete.
// It accepts both the single detail and the list form, because different
// firmwares use either.
func (e *Emulator) handlePutFingerPrintDelete(c *gin.Context) {
	var payload struct {
		FingerPrintDelete struct {
			Mode             string `json:"mode"`
			EmployeeNoDetail struct {
				EmployeeNo string `json:"employeeNo"`
			} `json:"EmployeeNoDetail"`
			EmployeeNoList []struct {
				EmployeeNo string `json:"employeeNo"`
			} `json:"EmployeeNoList"`
		} `json:"FingerPrintDelete"`
	}

	if err := c.BindJSON(&payload); err != nil {
		e.tracer.Error("[FingerPrint/Delete] invalid body: %v", err)
		writeHikvisionXML(c, http.StatusBadRequest, "6", "Error", "Invalid request")
		return
	}

	targets := make([]string, 0, 1+len(payload.FingerPrintDelete.EmployeeNoList))
	if n := payload.FingerPrintDelete.EmployeeNoDetail.EmployeeNo; n != "" {
		targets = append(targets, n)
	}
	for _, item := range payload.FingerPrintDelete.EmployeeNoList {
		if item.EmployeeNo != "" {
			targets = append(targets, item.EmployeeNo)
		}
	}

	total := 0
	for _, employeeNo := range targets {
		n, err := e.deleteFingersFn(employeeNo)
		if err != nil {
			e.tracer.Error("[FingerPrint/Delete] employeeNo=%s: %v", employeeNo, err)
			writeHikvisionXML(c, http.StatusInternalServerError, "6", "Error", err.Error())
			return
		}
		total += n
	}

	e.tracer.Info("[FingerPrint/Delete] %d fingerprint(s) removed", total)
	writeHikvisionXML(c, http.StatusOK, "1", "OK", "ok")
}

// handlePostCaptureFingerPrint handles POST /ISAPI/AccessControl/CaptureFingerPrint.
// Request (XML): <CaptureFingerPrintCond><fingerNo>N</fingerNo></CaptureFingerPrintCond>.
// Response (XML): <CaptureFingerPrint> with fingerData, the only field clients
// read when enrolling a fingerprint. The returned template is synthetic and
// stable: the emulator has no sensor.
func (e *Emulator) handlePostCaptureFingerPrint(c *gin.Context) {
	var cond struct {
		XMLName  xml.Name `xml:"CaptureFingerPrintCond"`
		FingerNo string   `xml:"fingerNo"`
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeHikvisionXML(c, http.StatusBadRequest, "6", "Error", "Invalid body")
		return
	}
	if err := xml.Unmarshal(body, &cond); err != nil {
		e.tracer.Error("[CaptureFingerPrint] invalid XML: %v", err)
		writeHikvisionXML(c, http.StatusBadRequest, "6", "Error", "Invalid XML")
		return
	}
	fingerNo := strings.TrimSpace(cond.FingerNo)
	if fingerNo == "" {
		fingerNo = "1"
	}

	// Synthetic template: 512 deterministic bytes, base64. Clients only pass
	// the string along, they don't interpret it.
	raw := make([]byte, 512)
	for i := range raw {
		raw[i] = byte((i*7 + 13) % 251)
	}
	fingerData := base64.StdEncoding.EncodeToString(raw)

	e.tracer.Info("[CaptureFingerPrint] fingerNo=%s, %d-byte template", fingerNo, len(raw))

	response := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<CaptureFingerPrint version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<fingerNo>%s</fingerNo>
<fingerPrintQuality>80</fingerPrintQuality>
<fingerData>%s</fingerData>
</CaptureFingerPrint>
`, fingerNo, fingerData)

	c.Header("Content-Type", "application/xml")
	c.String(http.StatusOK, response)
}
