package hikvision

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestBuildDoorEvent_UsesISAPIShape(t *testing.T) {
	e := newTestEmulator(t)
	e.macAddress = "02:1a:2b:3c:4d:5e"

	ev := e.buildDoorEvent(MinorDoorOpenNormal)

	if ev.EventType != "AccessControllerEvent" {
		t.Errorf("eventType: got %q, want AccessControllerEvent", ev.EventType)
	}
	if ev.MacAddress != "02:1a:2b:3c:4d:5e" {
		t.Errorf("macAddress: got %q, want the emulator MAC", ev.MacAddress)
	}
	if ev.DateTime == "" {
		t.Error("dateTime must be set; clients parse it as ISO8601 + offset")
	}
	if ev.AccessControllerEvent.MajorEventType != 5 {
		t.Errorf("majorEventType: got %d, want 5", ev.AccessControllerEvent.MajorEventType)
	}
	if ev.AccessControllerEvent.SubEventType != MinorDoorOpenNormal {
		t.Errorf("subEventType: got %d, want %d", ev.AccessControllerEvent.SubEventType, MinorDoorOpenNormal)
	}
	if ev.AccessControllerEvent.CardNo != "" {
		t.Errorf("cardNo must be empty on a door event; got %q", ev.AccessControllerEvent.CardNo)
	}
	if ev.AccessControllerEvent.FrontSerialNo == nil {
		t.Error("frontSerialNo must be present on a door event (it is already decided)")
	}

	// The body must be serializable and contain the fields clients read first.
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]interface{}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"dateTime", "macAddress", "AccessControllerEvent"} {
		if _, ok := back[k]; !ok {
			t.Errorf("field %q missing from event JSON", k)
		}
	}
	if _, ok := back["Events"]; ok {
		t.Error("JSON carries the Dahua \"Events\" key; the Dahua format regressed")
	}
}

func TestBuildDoorEvent_SerialNoIsMonotonic(t *testing.T) {
	e := newTestEmulator(t)

	first := e.buildDoorEvent(MinorDoorOpenNormal)
	second := e.buildDoorEvent(MinorDoorCloseNormal)

	if second.AccessControllerEvent.SerialNo <= first.AccessControllerEvent.SerialNo {
		t.Errorf("serialNo must increase: %d after %d",
			second.AccessControllerEvent.SerialNo, first.AccessControllerEvent.SerialNo)
	}
	if *second.AccessControllerEvent.FrontSerialNo != second.AccessControllerEvent.SerialNo-1 {
		t.Errorf("frontSerialNo must be the previous serialNo: front=%d serial=%d",
			*second.AccessControllerEvent.FrontSerialNo, second.AccessControllerEvent.SerialNo)
	}
}

func TestHandlePutCardDelete_ParsesEmployeeNoList(t *testing.T) {
	e := newTestEmulator(t)
	var deleted []string
	e.deleteCardsFn = func(employeeNo string) (int, error) {
		deleted = append(deleted, employeeNo)
		return 1, nil
	}

	r := gin.New()
	r.PUT("/ISAPI/AccessControl/CardInfo/Delete", e.handlePutCardDelete)

	body := `{"CardInfoDelCond":{"EmployeeNoList":[{"employeeNo":"1001"},{"employeeNo":"1002"}]}}`
	req := httptest.NewRequest(http.MethodPut, "/ISAPI/AccessControl/CardInfo/Delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if len(deleted) != 2 || deleted[0] != "1001" || deleted[1] != "1002" {
		t.Errorf("deleted employeeNos: got %v, want [1001 1002]", deleted)
	}
	if !strings.Contains(w.Body.String(), "<statusCode>1</statusCode>") {
		t.Errorf("response must be a success ResponseStatus; got:\n%s", w.Body.String())
	}
}

func TestHandlePutCardDelete_RejectsGarbage(t *testing.T) {
	e := newTestEmulator(t)
	e.deleteCardsFn = func(string) (int, error) { return 0, nil }

	r := gin.New()
	r.PUT("/ISAPI/AccessControl/CardInfo/Delete", e.handlePutCardDelete)

	req := httptest.NewRequest(http.MethodPut, "/ISAPI/AccessControl/CardInfo/Delete", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestHandlePutFingerPrintDelete_ParsesCondition(t *testing.T) {
	e := newTestEmulator(t)
	var deleted []string
	e.deleteFingersFn = func(employeeNo string) (int, error) {
		deleted = append(deleted, employeeNo)
		return 2, nil
	}

	r := gin.New()
	r.PUT("/ISAPI/AccessControl/FingerPrint/Delete", e.handlePutFingerPrintDelete)

	body := `{"FingerPrintDelete":{"mode":"byEmployeeNo","EmployeeNoDetail":{"employeeNo":"1001"}}}`
	req := httptest.NewRequest(http.MethodPut, "/ISAPI/AccessControl/FingerPrint/Delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if len(deleted) != 1 || deleted[0] != "1001" {
		t.Errorf("deleted employeeNos: got %v, want [1001]", deleted)
	}
	if !strings.Contains(w.Body.String(), "<statusCode>1</statusCode>") {
		t.Errorf("response must be a success ResponseStatus; got:\n%s", w.Body.String())
	}
}

func TestHandlePutFingerPrintDelete_AcceptsList(t *testing.T) {
	e := newTestEmulator(t)
	var deleted []string
	e.deleteFingersFn = func(employeeNo string) (int, error) {
		deleted = append(deleted, employeeNo)
		return 1, nil
	}

	r := gin.New()
	r.PUT("/ISAPI/AccessControl/FingerPrint/Delete", e.handlePutFingerPrintDelete)

	body := `{"FingerPrintDelete":{"EmployeeNoList":[{"employeeNo":"7"},{"employeeNo":"8"}]}}`
	req := httptest.NewRequest(http.MethodPut, "/ISAPI/AccessControl/FingerPrint/Delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(deleted) != 2 {
		t.Errorf("deleted: got %v, want two employeeNos", deleted)
	}
}

func TestHandlePostCaptureFingerPrint_ReturnsFingerData(t *testing.T) {
	e := newTestEmulator(t)
	r := gin.New()
	r.POST("/ISAPI/AccessControl/CaptureFingerPrint", e.handlePostCaptureFingerPrint)

	body := `<CaptureFingerPrintCond version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
    <fingerNo>3</fingerNo>
</CaptureFingerPrintCond>`
	req := httptest.NewRequest(http.MethodPost, "/ISAPI/AccessControl/CaptureFingerPrint", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("Content-Type: got %q, want application/xml", ct)
	}
	got := w.Body.String()
	// Clients read CaptureFingerPrint/fingerData.
	if !strings.Contains(got, "<CaptureFingerPrint") {
		t.Errorf("missing CaptureFingerPrint root element:\n%s", got)
	}
	if !strings.Contains(got, "<fingerNo>3</fingerNo>") {
		t.Errorf("fingerNo must echo the request:\n%s", got)
	}
	if !strings.Contains(got, "<fingerData>") {
		t.Errorf("missing fingerData, the only field clients read:\n%s", got)
	}
	if !strings.Contains(got, "<fingerPrintQuality>") {
		t.Errorf("missing fingerPrintQuality:\n%s", got)
	}
}

func TestHandlePostAcsEvent_ReturnsStoredEvents(t *testing.T) {
	e := newTestEmulator(t)
	e.eventLog = newEventLog(10)
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	e.eventLog.Append(AcsEventRecord{
		Time: base, Major: 5, Minor: MinorFaceVerifyPass,
		CardNo: "12345", Name: "John Doe", EmployeeNo: "1001", CardReaderNo: 1, DoorNo: 1, SerialNo: 10,
	})

	r := gin.New()
	r.POST("/ISAPI/AccessControl/AcsEvent", e.handlePostAcsEvent)

	body := `{"AcsEventCond":{"searchID":"x","searchResultPosition":0,"maxResults":30,"major":0,"minor":0}}`
	req := httptest.NewRequest(http.MethodPost, "/ISAPI/AccessControl/AcsEvent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp struct {
		AcsEvent struct {
			ResponseStatusStrg string `json:"responseStatusStrg"`
			NumOfMatches       int    `json:"numOfMatches"`
			TotalMatches       int    `json:"totalMatches"`
			InfoList           []struct {
				Major        int    `json:"major"`
				Minor        int    `json:"minor"`
				CardNo       string `json:"cardNo"`
				CardReaderNo int    `json:"cardReaderNo"`
				Time         string `json:"time"`
			} `json:"InfoList"`
		} `json:"AcsEvent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
	}
	if resp.AcsEvent.ResponseStatusStrg != "OK" {
		t.Errorf("responseStatusStrg: got %q, want OK", resp.AcsEvent.ResponseStatusStrg)
	}
	if len(resp.AcsEvent.InfoList) != 1 {
		t.Fatalf("InfoList: got %d items, want 1", len(resp.AcsEvent.InfoList))
	}
	it := resp.AcsEvent.InfoList[0]
	if it.Major != 5 || it.Minor != MinorFaceVerifyPass || it.CardNo != "12345" || it.CardReaderNo != 1 {
		t.Errorf("event badly serialized: %+v", it)
	}
	// Clients strip the 6-char offset and parse "YYYY-MM-DD HH:MM:SS".
	if len(it.Time) < 25 || it.Time[10] != 'T' {
		t.Errorf("time must be ISO8601 with offset; got %q", it.Time)
	}
}

func TestHandlePostAcsEvent_EmptyLogReturnsNoMatch(t *testing.T) {
	e := newTestEmulator(t)
	e.eventLog = newEventLog(10)

	r := gin.New()
	r.POST("/ISAPI/AccessControl/AcsEvent", e.handlePostAcsEvent)

	body := `{"AcsEventCond":{"searchResultPosition":0,"maxResults":30}}`
	req := httptest.NewRequest(http.MethodPost, "/ISAPI/AccessControl/AcsEvent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `"NO MATCH"`) {
		t.Errorf("empty log must answer NO MATCH; got:\n%s", w.Body.String())
	}
}

func TestHandlePutStorageCfg_PurgesByCheckTime(t *testing.T) {
	e := newTestEmulator(t)
	e.eventLog = newEventLog(10)
	e.eventLog.Append(AcsEventRecord{Time: time.Now().Add(-2 * time.Hour)})
	e.eventLog.Append(AcsEventRecord{Time: time.Now().Add(time.Hour)})

	r := gin.New()
	r.PUT("/ISAPI/AccessControl/AcsEvent/StorageCfg", e.handlePutStorageCfg)

	// checkTime is wall-clock time in the device time zone, so the cutoff
	// must be formatted there too; otherwise the test depends on the host TZ.
	cutoff := time.Now().In(e.location()).Format("2006-01-02 15:04:05")
	body := `{"EventStorageCfg":{"mode":"time","checkTime":"` + cutoff + `"}}`
	req := httptest.NewRequest(http.MethodPut, "/ISAPI/AccessControl/AcsEvent/StorageCfg", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if _, total := e.eventLog.Page(0, 10); total != 1 {
		t.Errorf("1 event should remain after the purge; got %d", total)
	}
}

func TestHandlePutStorageCfg_RegularModeKeepsEvents(t *testing.T) {
	e := newTestEmulator(t)
	e.eventLog = newEventLog(10)
	e.eventLog.Append(AcsEventRecord{Time: time.Now().Add(-2 * time.Hour)})

	r := gin.New()
	r.PUT("/ISAPI/AccessControl/AcsEvent/StorageCfg", e.handlePutStorageCfg)

	body := `{"EventStorageCfg":{"mode":"regular","period":10}}`
	req := httptest.NewRequest(http.MethodPut, "/ISAPI/AccessControl/AcsEvent/StorageCfg", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if _, total := e.eventLog.Page(0, 10); total != 1 {
		t.Errorf("mode=regular must not delete anything; %d remained", total)
	}
}

// Regression: clients send checkTime as wall-clock time in the device time
// zone. Parsed in any other zone (e.g. the host's time.Local) the window
// shifted by the zone difference and StorageCfg never deleted anything. A
// device zone far from UTC makes the test meaningful on any host TZ.
func TestHandlePutStorageCfg_CheckTimeUsesDeviceLocation(t *testing.T) {
	e := newTestEmulator(t)
	e.eventLog = newEventLog(10)
	e.timeZone.Store(time.FixedZone("-03:00", -3*3600))

	loc := e.location()
	now := time.Now().In(loc)

	// Event from 10 minutes ago, stamped the way the emulator stamps events.
	e.eventLog.Append(AcsEventRecord{Time: now.Add(-10 * time.Minute)})

	r := gin.New()
	r.PUT("/ISAPI/AccessControl/AcsEvent/StorageCfg", e.handlePutStorageCfg)

	// checkTime = now, written in the device time zone WITHOUT offset, which is
	// the format clients produce.
	cutoff := now.Format("2006-01-02 15:04:05")
	body := `{"EventStorageCfg":{"mode":"time","checkTime":"` + cutoff + `"}}`
	req := httptest.NewRequest(http.MethodPut, "/ISAPI/AccessControl/AcsEvent/StorageCfg", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if _, total := e.eventLog.Page(0, 10); total != 0 {
		t.Errorf("event older than checkTime should have been purged; %d remained", total)
	}
}
