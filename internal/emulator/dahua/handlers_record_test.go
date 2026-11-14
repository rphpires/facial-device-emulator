package dahua

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/gin-gonic/gin"
)

func TestEventDataDetails_OptionalFieldsOmittedWhenUnset(t *testing.T) {
	d := EventDataDetails{ReaderID: "1", UserID: "10"}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, forbidden := range []string{"QRCodeStr", "TrafficCar", "CollectResult"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("%s must not appear in a regular event: %s", forbidden, s)
		}
	}
}

func TestEventDataDetails_QRCodeAndPlateSerialize(t *testing.T) {
	yes := true
	d := EventDataDetails{
		ReaderID:      "1",
		Method:        14, // EventMethod.QRCODE
		QRCodeStr:     "ABC123456",
		TrafficCar:    &TrafficCar{PlateNumber: "ABC1D23"},
		CollectResult: &yes,
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	// Clients read exactly these keys when extracting the credential.
	if !strings.Contains(s, `"QRCodeStr":"ABC123456"`) {
		t.Errorf("QRCodeStr badly serialized: %s", s)
	}
	if !strings.Contains(s, `"TrafficCar":{"PlateNumber":"ABC1D23"}`) {
		t.Errorf("TrafficCar badly serialized: %s", s)
	}
	if !strings.Contains(s, `"CollectResult":true`) {
		t.Errorf("CollectResult badly serialized: %s", s)
	}
}

func TestEventDataDetails_CollectResultFalseIsExplicit(t *testing.T) {
	no := false
	d := EventDataDetails{CollectResult: &no}
	raw, _ := json.Marshal(d)
	// A pointer rather than a bool, precisely so "capture failed" is not
	// dropped from the JSON by omitempty.
	if !strings.Contains(string(raw), `"CollectResult":false`) {
		t.Errorf("CollectResult=false must appear: %s", raw)
	}
}

func newRecordTestEmulator() *Emulator {
	return &Emulator{
		tracer:   trace.NewTracer(),
		stopChan: make(chan struct{}),
		eventLog: newDahuaEventLog(10),
	}
}

func TestHandleRecordFinder_CardRecUsesEventLog(t *testing.T) {
	e := newRecordTestEmulator()
	now := time.Now()
	e.eventLog.Append(CardRecEntry{
		CreateTime: now.Add(-30 * time.Minute),
		CardNo:     "0A0B", UserID: 7, ReaderID: 1, Method: 15,
	})

	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	url := fmt.Sprintf("/cgi-bin/recordFinder.cgi?action=find&name=AccessControlCardRec&StartTime=%d&EndTime=%d",
		now.Add(-time.Hour).Unix(), now.Unix())
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "found=1") || !strings.Contains(body, "records[0].CardNo=0A0B") {
		t.Errorf("offline event did not come from the event log:\n%s", body)
	}
	if strings.Contains(body, "ValidDateStart") {
		t.Error("response carries card fields; the handler is ignoring name")
	}
}

func TestHandleRecordFinder_CardRecOutOfWindowIsEmpty(t *testing.T) {
	e := newRecordTestEmulator()
	e.eventLog.Append(CardRecEntry{CreateTime: time.Now().Add(-48 * time.Hour), CardNo: "0A0B"})

	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	now := time.Now()
	url := fmt.Sprintf("/cgi-bin/recordFinder.cgi?action=find&name=AccessControlCardRec&StartTime=%d&EndTime=%d",
		now.Add(-time.Hour).Unix(), now.Unix())
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), "found=0") {
		t.Errorf("event outside the window must not appear:\n%s", w.Body.String())
	}
}

func TestHandleRecordFinder_GetQuerySizeCards(t *testing.T) {
	e := newRecordTestEmulator()
	e.countCardsFn = func() (int, error) { return 42, nil }

	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	req := httptest.NewRequest(http.MethodGet, "/cgi-bin/recordFinder.cgi?action=getQuerySize&name=AccessControlCard", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Clients accept count=, size= or recnum= for the card count.
	if !strings.Contains(w.Body.String(), "count=42") {
		t.Errorf("getQuerySize: got %q, want a count=42 line", w.Body.String())
	}
}

func TestSetupRoutes_RegistersEventManager(t *testing.T) {
	e := newRecordTestEmulator()
	r := gin.New()
	e.SetupRoutes(r)

	found := false
	for _, ri := range r.Routes() {
		if ri.Method == http.MethodGet && ri.Path == "/cgi-bin/eventManager.cgi" {
			found = true
		}
	}
	if !found {
		t.Error("GET /cgi-bin/eventManager.cgi not registered; standalone mode without snapshots would be silent")
	}
}

func TestHandleEventManager_RejectsUnknownAction(t *testing.T) {
	e := newRecordTestEmulator()
	r := gin.New()
	r.GET("/cgi-bin/eventManager.cgi", e.handleEventManager)

	req := httptest.NewRequest(http.MethodGet, "/cgi-bin/eventManager.cgi?action=detach", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestHandleAccessControl_CaptureFingerprintAccepted(t *testing.T) {
	e := newRecordTestEmulator()
	var requests []string
	e.enrollFn = func(userID string) { requests = append(requests, userID) }

	r := gin.New()
	r.GET("/cgi-bin/accessControl.cgi", e.handleAccessControl)

	req := httptest.NewRequest(http.MethodGet,
		"/cgi-bin/accessControl.cgi?action=captureFingerprint&info.ReaderID=1&info.UserID=42&heartbeat=5&timeout=60", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "OK") {
		t.Errorf("response must be OK; got %q", w.Body.String())
	}
	if len(requests) != 1 || requests[0] != "42" {
		t.Errorf("enroll fired with: %v, want [42]", requests)
	}
}

func TestBuildEnrollEvent_CarriesCollectResult(t *testing.T) {
	ev := buildEnrollEvent("00:11:22:33:44:55", "42", true)
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"CollectResult":true`) {
		t.Errorf("the client diverts the event on the CollectResult key: %s", s)
	}
	if !strings.Contains(s, `"UserID":"42"`) {
		t.Errorf("UserID missing: %s", s)
	}
	if !strings.Contains(s, `"PhysicalAddress":"00:11:22:33:44:55"`) {
		t.Errorf("PhysicalAddress missing: %s", s)
	}
}

func TestPushStreamEvent_DropsWhenNobodyIsListening(t *testing.T) {
	e := newRecordTestEmulator()
	e.pushChan = make(chan []byte, 1)

	// The first fits in the buffer; the second must be dropped without blocking.
	e.pushStreamEvent([]byte("one"))
	e.pushStreamEvent([]byte("two"))

	if n := len(e.pushChan); n != 1 {
		t.Errorf("channel should hold exactly 1 item; got %d", n)
	}
}

func TestBuildStreamTextPart_HasBoundaryAndLength(t *testing.T) {
	part := buildStreamTextPart([]byte(`{"a":1}`))
	s := string(part)
	if !strings.Contains(s, "--myboundary") {
		t.Errorf("boundary missing: %q", s)
	}
	if !strings.Contains(s, "Content-Type: text/plain") {
		t.Errorf("Content-Type missing: %q", s)
	}
	// Clients slice the body by Content-Length; without it a binary or
	// multi-line payload gets truncated.
	if !strings.Contains(s, "Content-Length: 7") {
		t.Errorf("wrong Content-Length: %q", s)
	}
}

// The CardName 32-byte limit tests (maxCardNameBytes: the real device
// REJECTS an overlong CardName, it does not truncate) live in
// handlers_cardface_test.go, with the rest of the
// recordUpdater.cgi/AccessControlCard fidelity tests.
