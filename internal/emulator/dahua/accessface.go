package dahua

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// This file covers AccessFace.cgi and AccessUser.cgi - endpoints measured
// against a real Dahua-OEM face terminal that the emulator used to ignore:
// every POST to AccessFace.cgi?action=insertMulti fell through to NoRoute as a
// 404, so the client could never enroll a new face through this path.

const (
	// faceMinBytesApprox does NOT emulate "detectable face" - to be blunt:
	// 288686087 is the code the real device returns when its actual face
	// modeling finds no face in the photo; the emulator has no face
	// modeling and only looks at the SIZE in bytes. Size is a poor proxy for
	// "has a face", and the measurements below prove it - no single threshold
	// separates the observed cases correctly.
	//
	// Measurements on the bench and in the integration test harness, smallest
	// to largest, all against the real device:
	//   - 5,425 bytes   (flat gray 640x480 photo, no face at all)       -> 288686087
	//   - 43,530 bytes  (1600x1600 photo with an 80x80 face)            -> 288686087
	//   - 46,352 bytes  (harness fixture, face_UT-001.jpg, portrait)    -> 200 OK
	//   - 100,063 bytes (real photo measured on the bench)              -> 200 OK
	//   - sample of real enrollment photos (40/502): 83,611-131,874 bytes, median 105,273
	//
	// The 43,530 case proves size is not detectability: it is almost the same
	// size as the 46,352-byte fixture the device accepts, yet it was rejected
	// because the face inside the photo is too small, not because the file
	// is. No value between 43,530 and 46,352 resolves this; the
	// deciding feature is not file size.
	//
	// So this threshold only catches the OBVIOUS "this is not even a photo of
	// a person" case (the 5,425-byte flat gray) and lets through anything that
	// could be a real portrait - including the harness's 46,352-byte fixture,
	// which this threshold wrongly rejected back when it was 64 KiB.
	//
	// Known, documented divergence rather than one hidden behind a "tuned"
	// value: at 16 KiB the emulator ACCEPTS the 43,530-byte case (small face
	// on a large canvas) that the real device REJECTS. That divergence is not
	// emulated and cannot be without real face modeling - any attempt to
	// "fix" it by size alone goes back to rejecting valid portraits, as the
	// harness fixture proved.
	faceMinBytesApprox = 16 * 1024

	// faceMaxBytes is the bench-measured limit for the raw JPEG (already
	// decoded from base64) of AccessFace.cgi?action=insertMulti: 384,515
	// bytes WERE accepted; 423,728 WERE rejected with 286064923 (also
	// rejected: 468,303, 504,901, 522,840, 653,998). The value here,
	// 393,216 = 384 KiB, is the largest N such that ceil(N/3)*4 <= 524,288 -
	// i.e. the device appears to cap the base64 STRING at ~512 KiB:
	// ceil(384,515/3)*4 = 512,688 chars (accepted) and ceil(423,728/3)*4 =
	// 564,972 chars (rejected) match that formula exactly.
	faceMaxBytes = 384 * 1024

	// faceHardBadRequestBytes is the SECOND measured limit, distinct from the
	// one above: a 957,047-byte payload did not return the JSON error
	// envelope but a plain 400 "Error\nBad Request!" - a sign that the device
	// has a much larger parsing/request limit on a different code path than
	// faceMaxBytes. Where exactly that second limit starts was NOT measured -
	// we only know 653,998 still gets the JSON envelope and 957,047 does not.
	// 800 KiB sits in the middle of the unknown range; it is a documented
	// approximation, not a measured value.
	faceHardBadRequestBytes = 800 * 1024

	// FailCodes of the insertMulti error envelope, all measured on the bench.
	faceCodeNoFaceDetected  = 288686087 // no detectable face in the photo
	faceCodeTooLarge        = 286064923 // JPEG larger than faceMaxBytes
	faceCodeAlreadyEnrolled = 286064926 // UserID already has a face enrolled

	// faceBatchErrorCode and faceBatchErrorMessage are the outer
	// "code"/"message" of the envelope - constant across all measured
	// examples, regardless of which FailCode fired.
	faceBatchErrorCode    = 268632336
	faceBatchErrorMessage = "Batch Process Error"
)

// insertMultiRequest is the body of POST
// AccessFace.cgi?action=insertMulti: {"FaceList":[{"UserID":...,
// "PhotoData":[...]}]}.
type insertMultiRequest struct {
	FaceList []faceListItem `json:"FaceList"`
}

type faceListItem struct {
	UserID    flexInt     `json:"UserID"`
	PhotoData flexStrings `json:"PhotoData"`
}

// handleAccessFaceGet serves GET /cgi-bin/AccessFace.cgi.
func (e *Emulator) handleAccessFaceGet(c *gin.Context) {
	switch c.Query("action") {
	case "get":
		// Measured on the bench: 501.
		e.handleResponse(c, "Error\nNot Implemented!", http.StatusNotImplemented, 50)
	case "list":
		e.handleResponse(c, "Error\nBad Request!", http.StatusBadRequest, 50)
	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// handleAccessFacePost serves POST /cgi-bin/AccessFace.cgi.
func (e *Emulator) handleAccessFacePost(c *gin.Context) {
	switch c.Query("action") {
	case "insertMulti":
		e.handleAccessFaceInsertMulti(c)
	case "updateMulti":
		e.handleAccessFaceUpdateMulti(c)
	case "list":
		// Measured on the bench: EVERY body attempt (FaceList/UserIDList/
		// UserID/empty, with or without count/offset) returned a plain 400.
		// No documented body works - the action exists, but no tested format
		// satisfies it.
		e.handleResponse(c, "Error\nBad Request!", http.StatusBadRequest, 50)
	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// handleAccessUserGet serves GET /cgi-bin/AccessUser.cgi. It currently only
// exists to reproduce the 501 for action=remove measured on the bench.
func (e *Emulator) handleAccessUserGet(c *gin.Context) {
	switch c.Query("action") {
	case "remove":
		e.handleResponse(c, "Error\nNot Implemented!", http.StatusNotImplemented, 50)
	default:
		e.handleResponse(c, "Unknown action", http.StatusBadRequest, 50)
	}
}

// handleAccessFaceInsertMulti implements POST
// AccessFace.cgi?action=insertMulti - the biggest gap found on the bench:
// before this route existed, every POST here became a NoRoute 404 and the
// client could not enroll any face through this path.
func (e *Emulator) handleAccessFaceInsertMulti(c *gin.Context) {
	e.handleAccessFaceBatch(c, false)
}

// handleAccessFaceUpdateMulti implements POST
// AccessFace.cgi?action=updateMulti. Measured via the integration test
// harness: when insertMulti is rejected with 286064926 (UserID already has a
// face), the driver resends the SAME photo as updateMulti, and the real
// device accepts it with 200 OK - that is how the driver replaces the photo
// of a user who already has a face enrolled. The only difference from
// insertMulti is that updateMulti upserts (no duplicate rejection); the
// other two rejection reasons (too large, no face) still apply.
func (e *Emulator) handleAccessFaceUpdateMulti(c *gin.Context) {
	e.handleAccessFaceBatch(c, true)
}

// handleAccessFaceBatch is the shared implementation of insertMulti and
// updateMulti - body, parsing and size limits are identical; only the
// duplicate check differs (see validateFace).
func (e *Emulator) handleAccessFaceBatch(c *gin.Context, isUpdate bool) {
	var req insertMultiRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		e.tracer.Warning("[AccessFace] bind failed: %v", err)
		e.handleResponse(c, "Error\nBad Request!", http.StatusBadRequest, 50)
		return
	}

	if len(req.FaceList) == 0 {
		e.handleResponse(c, "Error\nBad Request!", http.StatusBadRequest, 50)
		return
	}

	type entry struct {
		userID int
		photo  []byte
	}
	entries := make([]entry, 0, len(req.FaceList))

	for _, item := range req.FaceList {
		var photoB64 string
		if len(item.PhotoData) > 0 {
			photoB64 = item.PhotoData[0]
		}

		// The second measured limit (faceHardBadRequestBytes) applies to the
		// raw JPEG size; estimate it from the base64 length so a huge payload
		// does not have to be decoded just to find out it will be rejected.
		if base64.StdEncoding.DecodedLen(len(photoB64)) > faceHardBadRequestBytes {
			e.tracer.Warning("[AccessFace] PhotoData above the hard limit (%d bytes estimated)",
				base64.StdEncoding.DecodedLen(len(photoB64)))
			e.handleResponse(c, "Error\nBad Request!", http.StatusBadRequest, 50)
			return
		}

		photo, err := base64.StdEncoding.DecodeString(photoB64)
		if err != nil {
			e.tracer.Warning("[AccessFace] PhotoData is not valid base64 (UserID=%d): %v", int(item.UserID), err)
			e.handleResponse(c, "Error\nBad Request!", http.StatusBadRequest, 50)
			return
		}
		entries = append(entries, entry{userID: int(item.UserID), photo: photo})
	}

	var failures []int
	for _, en := range entries {
		code, err := e.validateFace(en.userID, en.photo, isUpdate)
		if err != nil {
			e.tracer.Error("[AccessFace] validateFace UserID=%d: %v", en.userID, err)
			e.handleResponse(c, "Error", http.StatusInternalServerError, 50)
			return
		}
		if code != 0 {
			failures = append(failures, code)
		}
	}

	if len(failures) > 0 {
		e.writeFaceBatchError(c, failures)
		return
	}

	for _, en := range entries {
		hash := md5.Sum(en.photo)
		if err := e.repo.AddFace(en.userID, fmt.Sprintf("%X", hash)); err != nil {
			e.tracer.Error("[AccessFace] AddFace UserID=%d: %v", en.userID, err)
			e.handleResponse(c, "Error", http.StatusInternalServerError, 50)
			return
		}
	}

	e.handleResponse(c, "OK", http.StatusOK, 50)
}

// validateFace decides whether a FaceList entry should be accepted. It
// returns failCode=0 when the photo passes every check. isUpdate
// distinguishes insertMulti (rejects duplicates) from updateMulti (upsert -
// never rejects for an existing face).
//
// The ORDER below (too large -> duplicate -> no face) is a design decision,
// not a measurement: the three cases were only measured in isolation on the
// bench, never combined, so the device's real check order is unknown.
//
// 286064922 ("blurry photo") is NOT emulated: there is no way to tell
// "blurry" from "sharp" without real image-quality detection, and faking it
// with yet another size threshold would invent behavior with no basis in
// measurement. Documented as a gap, not faked.
func (e *Emulator) validateFace(userID int, photo []byte, isUpdate bool) (failCode int, err error) {
	if len(photo) > faceMaxBytes {
		return faceCodeTooLarge, nil
	}

	if !isUpdate {
		exists, err := e.repo.CheckIfFaceExists(userID)
		if err != nil {
			return 0, err
		}
		if exists {
			return faceCodeAlreadyEnrolled, nil
		}
	}

	if len(photo) < faceMinBytesApprox {
		return faceCodeNoFaceDetected, nil
	}

	return 0, nil
}

// writeErrorCode writes the real device's simple error envelope -
// {"code":N,"message":""}, with no trailing newline. Used for "unknown
// table" in recordFinder (285278242) and for the two rejection reasons of
// recordUpdater.insert (286064929 / 286064930).
func (e *Emulator) writeErrorCode(c *gin.Context, status, code int) {
	body := struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: ""}

	raw, err := json.Marshal(body)
	if err != nil {
		e.tracer.Error("[writeErrorCode] marshal failed: %v", err)
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Data(status, "application/json; charset=utf-8", raw)
}

// writeFaceBatchError writes the error envelope of
// AccessFace.cgi?action=insertMulti. codes are the FailCodes in the order the
// FaceList faces failed; FailCount is len(codes). Format measured on the
// bench in three distinct scenarios (duplicate, no face, size), always with
// the same outer code/message.
func (e *Emulator) writeFaceBatchError(c *gin.Context, codes []int) {
	body := struct {
		Code   int `json:"code"`
		Detail struct {
			FailCodes []int `json:"FailCodes"`
			FailCount int   `json:"FailCount"`
		} `json:"detail"`
		Message string `json:"message"`
	}{Code: faceBatchErrorCode, Message: faceBatchErrorMessage}
	body.Detail.FailCodes = codes
	body.Detail.FailCount = len(codes)

	raw, err := json.Marshal(body)
	if err != nil {
		e.tracer.Error("[writeFaceBatchError] marshal failed: %v", err)

		c.Status(http.StatusInternalServerError)
		return
	}
	c.Data(http.StatusBadRequest, "application/json; charset=utf-8", raw)
}
