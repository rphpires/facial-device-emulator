package dahua

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/gin-gonic/gin"
)

// Byte-for-byte fidelity tests against a real Dahua-OEM face terminal,
// measured on the bench with curl directly against the device. They cover
// recordFinder.cgi/recordUpdater.cgi (AccessControlCard table) and
// AccessFace.cgi/FaceInfoManager.cgi/AccessUser.cgi.

func newCardTestEmulator() (*Emulator, *fakeStore) {
	store := newFakeStore()
	repo := NewRepository(store, 999)
	e := &Emulator{
		tracer:   trace.NewTracer(),
		repo:     repo,
		stopChan: make(chan struct{}),
	}
	e.countCardsFn = func() (int, error) { return len(store.cards), nil }
	return e, store
}

func mustParseDate(str string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04:05", str)
	return t
}

// ---------------------------------------------------------------------------
// A. recordFinder.cgi, AccessControlCard table
// ---------------------------------------------------------------------------

// A1: find WITHOUT condition.UserID returns the first `count` records of the
// table, IGNORING offset - measured on the bench with offset=0,100,260,500,12000
// all returning found=50 with the same RecNo=15 first.
func TestHandleRecordFinder_FindWithoutUserIDIgnoresOffset(t *testing.T) {
	e, store := newCardTestEmulator()
	for i := 1; i <= 5; i++ {
		store.cards = append(store.cards, Card{
			RecNo: i, CardName: fmt.Sprintf("User%d", i), UserID: i, CardNo: fmt.Sprintf("CARD%d", i),
			ValidDateStart: mustParseDate("2026-01-01 00:00:00"), ValidDateEnd: mustParseDate("2027-01-01 00:00:00"),
		})
	}

	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	for _, offset := range []string{"0", "100", "12000"} {
		url := fmt.Sprintf("/cgi-bin/recordFinder.cgi?action=find&name=AccessControlCard&count=2&offset=%s", offset)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		body := w.Body.String()
		if !strings.Contains(body, "found=2") || !strings.Contains(body, "records[0].RecNo=1") {
			t.Errorf("offset=%s: got %q, want found=2 with RecNo=1 first (offset must be ignored)", offset, body)
		}
	}
}

// A1 (continued): find WITH condition.UserID still goes through FindCard.
func TestHandleRecordFinder_FindWithUserIDFiltersByCard(t *testing.T) {
	e, store := newCardTestEmulator()
	store.cards = append(store.cards,
		Card{RecNo: 1, CardName: "A", UserID: 7, CardNo: "AAA", ValidDateStart: mustParseDate("2026-01-01 00:00:00"), ValidDateEnd: mustParseDate("2027-01-01 00:00:00")},
		Card{RecNo: 2, CardName: "B", UserID: 8, CardNo: "BBB", ValidDateStart: mustParseDate("2026-01-01 00:00:00"), ValidDateEnd: mustParseDate("2027-01-01 00:00:00")},
	)

	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	req := httptest.NewRequest(http.MethodGet,
		"/cgi-bin/recordFinder.cgi?action=find&name=AccessControlCard&condition.UserID=7", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "found=1") || !strings.Contains(body, "records[0].UserID=7") {
		t.Errorf("got %q, want found=1 with UserID=7", body)
	}
	if strings.Contains(body, "UserID=8") {
		t.Errorf("got %q, another UserID's card must not appear", body)
	}
}

// A2: doSeekFind pages correctly with offset/count and terminates (found
// below count on the last page, found=0 past the end). GetCards delegates
// this to Postgres LIMIT/OFFSET - here we confirm the handler passes the
// right parameters and the paging behavior is faithful.
func TestHandleRecordFinder_DoSeekFindPaginatesAndTerminates(t *testing.T) {
	e, store := newCardTestEmulator()
	for i := 1; i <= 14; i++ {
		store.cards = append(store.cards, Card{
			RecNo: i, CardName: fmt.Sprintf("Name%d", i), UserID: i, CardNo: fmt.Sprintf("C%d", i),
			ValidDateStart: mustParseDate("2026-01-01 00:00:00"), ValidDateEnd: mustParseDate("2027-01-01 00:00:00"),
		})
	}

	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	cases := []struct {
		offset, count int
		wantFound     int
	}{
		{0, 10, 10},
		{10, 10, 4}, // last partial page
		{14, 10, 0}, // past the end
	}
	for _, c := range cases {
		url := fmt.Sprintf("/cgi-bin/recordFinder.cgi?action=doSeekFind&name=AccessControlCard&offset=%d&count=%d", c.offset, c.count)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		want := fmt.Sprintf("found=%d", c.wantFound)
		if c.wantFound == 0 {
			want = "found=0"
		}
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("offset=%d count=%d: got %q, want to contain %q", c.offset, c.count, w.Body.String(), want)
		}
	}
}

// A3: startFind e stopFind -> 501 "Error\nNot Implemented!" exato.
func TestHandleRecordFinder_StartStopFindNotImplemented(t *testing.T) {
	e, _ := newCardTestEmulator()
	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	for _, action := range []string{"startFind", "stopFind"} {
		req := httptest.NewRequest(http.MethodGet,
			"/cgi-bin/recordFinder.cgi?action="+action+"&name=AccessControlCard", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusNotImplemented {
			t.Errorf("action=%s: status = %d, want 501", action, w.Code)
		}
		if got := w.Body.String(); got != "Error\nNot Implemented!" {
			t.Errorf("action=%s: body = %q, want %q", action, got, "Error\nNot Implemented!")
		}
	}
}

// A4: getQuerySize returns BOTH lines, Size= and count=, with the same value.
func TestHandleRecordFinder_GetQuerySizeHasSizeAndCountLines(t *testing.T) {
	e, store := newCardTestEmulator()
	for i := 1; i <= 3; i++ {
		store.cards = append(store.cards, Card{RecNo: i, UserID: i, CardNo: fmt.Sprintf("C%d", i)})
	}

	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	req := httptest.NewRequest(http.MethodGet,
		"/cgi-bin/recordFinder.cgi?action=getQuerySize&name=AccessControlCard", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if want := "Size=3\ncount=3"; w.Body.String() != want {
		t.Errorf("body = %q, want %q", w.Body.String(), want)
	}
}

// A5: unknown table (measured with name=AccessFace) -> 400 JSON
// {"code":285278242,"message":""}, for both find and getQuerySize.
func TestHandleRecordFinder_UnknownTableReturnsJSONError(t *testing.T) {
	e, _ := newCardTestEmulator()
	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	for _, action := range []string{"find", "getQuerySize"} {
		req := httptest.NewRequest(http.MethodGet,
			"/cgi-bin/recordFinder.cgi?action="+action+"&name=AccessFace", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("action=%s: status = %d, want 400", action, w.Code)
		}
		want := `{"code":285278242,"message":""}`
		if got := w.Body.String(); got != want {
			t.Errorf("action=%s: body = %q, want %q", action, got, want)
		}
	}
}

// A6: find for a UserID with no card -> 200 found=0.
func TestHandleRecordFinder_FindUserWithNoCardsReturnsFoundZero(t *testing.T) {
	e, _ := newCardTestEmulator()
	r := gin.New()
	r.GET("/cgi-bin/recordFinder.cgi", e.handleRecordFinder)

	req := httptest.NewRequest(http.MethodGet,
		"/cgi-bin/recordFinder.cgi?action=find&name=AccessControlCard&condition.UserID=999999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "found=0" {
		t.Errorf("body = %q, want %q", got, "found=0")
	}
}

// ---------------------------------------------------------------------------
// B. recordUpdater.cgi, AccessControlCard table
// ---------------------------------------------------------------------------

func insertCard(r *gin.Engine, userID int, cardNo string) *httptest.ResponseRecorder {
	return insertCardWithName(r, userID, cardNo, "John Doe")
}

func insertCardWithName(r *gin.Engine, userID int, cardNo, cardName string) *httptest.ResponseRecorder {
	q := url.Values{}
	q.Set("action", "insert")
	q.Set("CardName", cardName)
	q.Set("UserID", strconv.Itoa(userID))
	q.Set("CardNo", cardNo)
	q.Set("ValidDateStart", "2026-01-01 00:00:00")
	q.Set("ValidDateEnd", "2027-01-01 00:00:00")

	req := httptest.NewRequest(http.MethodGet, "/cgi-bin/recordUpdater.cgi?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// CardName: the bench-measured limit is 32 UTF-8 BYTES, not runes, and the
// device REJECTS (does not truncate) anything longer - see maxCardNameBytes
// in handlers.go.
func TestHandleRecordUpdater_InsertCardNameByteLimit(t *testing.T) {
	cases := []struct {
		name         string
		cardName     string
		wantAccepted bool
	}{
		{"32 bytes ASCII - accepted", strings.Repeat("A", 32), true},
		{"33 bytes ASCII - rejected", strings.Repeat("A", 33), false},
		{"16x U+00E1 = 32 bytes - accepted", strings.Repeat("\u00e1", 16), true},
		{"32x U+00E1 = 64 bytes - rejected", strings.Repeat("\u00e1", 32), false},
		{"16 'A' + 16 U+00E1 = 48 bytes - rejected", strings.Repeat("A", 16) + strings.Repeat("\u00e1", 16), false},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := newCardTestEmulator()
			r := gin.New()
			r.GET("/cgi-bin/recordUpdater.cgi", e.handleRecordUpdaterGet)

			w := insertCardWithName(r, 1000+i, fmt.Sprintf("CARDLEN%d", i), c.cardName)

			if c.wantAccepted {
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
				}
				if got := w.Body.String(); got != "RecNo=1" {
					t.Errorf("body = %q, want %q", got, "RecNo=1")
				}
			} else {
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
				}
				if got := w.Body.String(); got != "Error\nBad Request!" {
					t.Errorf("body = %q, want %q", got, "Error\nBad Request!")
				}
			}
		})
	}
}

// CardName is neither sanitized nor normalized: accents, apostrophes,
// parentheses, commas, periods and hyphens pass through intact as long as
// they fit the 32-byte limit. Measured byte-for-byte identical on the bench.
func TestHandleRecordUpdater_InsertCardNameRoundTripsAccentedText(t *testing.T) {
	names := []string{"Renee Garcon (test)", "Ren\u00e9e Gar\u00e7on", "Jos\u00e9 D'\u00c1vila-Neto, Jr."}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			e, store := newCardTestEmulator()
			r := gin.New()
			r.GET("/cgi-bin/recordUpdater.cgi", e.handleRecordUpdaterGet)

			if len(name) > maxCardNameBytes {
				t.Fatalf("invalid test case: %q is %d bytes, above the limit", name, len(name))
			}

			w := insertCardWithName(r, 42, "RTRIP1", name)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
			}

			if len(store.cards) != 1 || store.cards[0].CardName != name {
				t.Errorf("persisted CardName = %q, want %q (no sanitization)", store.cards[0].CardName, name)
			}
		})
	}
}

// B1: successful insert -> 200 "RecNo=<n>".
func TestHandleRecordUpdater_InsertSuccess(t *testing.T) {
	e, _ := newCardTestEmulator()
	r := gin.New()
	r.GET("/cgi-bin/recordUpdater.cgi", e.handleRecordUpdaterGet)

	w := insertCard(r, 1, "AAA111")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "RecNo=1" {
		t.Errorf("body = %q, want %q", got, "RecNo=1")
	}
}

// B2: a second insert for a UserID that already has a card -> 400 JSON
// {"code":286064929,"message":""}, even with a different CardNo.
func TestHandleRecordUpdater_InsertDuplicateUserIDRejected(t *testing.T) {
	e, _ := newCardTestEmulator()
	r := gin.New()
	r.GET("/cgi-bin/recordUpdater.cgi", e.handleRecordUpdaterGet)

	if w := insertCard(r, 1, "AAA111"); w.Code != http.StatusOK {
		t.Fatalf("initial insert failed: %d %s", w.Code, w.Body.String())
	}

	w := insertCard(r, 1, "ZZZ999")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	want := `{"code":286064929,"message":""}`
	if got := w.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// Inserting a CardNo that already belongs to another UserID -> 400 JSON
// {"code":286064930,"message":""}.
func TestHandleRecordUpdater_InsertDuplicateCardNoRejected(t *testing.T) {
	e, _ := newCardTestEmulator()
	r := gin.New()
	r.GET("/cgi-bin/recordUpdater.cgi", e.handleRecordUpdaterGet)

	if w := insertCard(r, 1, "AAA111"); w.Code != http.StatusOK {
		t.Fatalf("initial insert failed: %d %s", w.Code, w.Body.String())
	}

	w := insertCard(r, 2, "AAA111")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	want := `{"code":286064930,"message":""}`
	if got := w.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// B4: remove is IDEMPOTENT - a nonexistent RecNo returns 200 OK, not an
// error and not 501.
func TestHandleRecordUpdater_RemoveIsIdempotent(t *testing.T) {
	e, _ := newCardTestEmulator()
	r := gin.New()
	r.GET("/cgi-bin/recordUpdater.cgi", e.handleRecordUpdaterGet)

	req := httptest.NewRequest(http.MethodGet, "/cgi-bin/recordUpdater.cgi?action=remove&recno=99999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "OK" {
		t.Errorf("body = %q, want %q", got, "OK")
	}
}

// ---------------------------------------------------------------------------
// C. Faces
// ---------------------------------------------------------------------------

func newFaceRouterEmulator() (*Emulator, *fakeStore) {
	e, store := newCardTestEmulator()
	return e, store
}

// fakePhoto builds n bytes of fake "JPEG" payload (the emulator does not
// validate the format, only the size) and returns it as base64.
func fakePhoto(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 256)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// C1: successful POST AccessFace.cgi?action=insertMulti -> 200 "OK".
func TestHandleAccessFace_InsertMultiSuccess(t *testing.T) {
	e, store := newFaceRouterEmulator()
	r := gin.New()
	r.POST("/cgi-bin/AccessFace.cgi", e.handleAccessFacePost)

	body := fmt.Sprintf(`{"FaceList":[{"UserID":"42","PhotoData":["%s"]}]}`, fakePhoto(200*1024))
	req := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "OK" {
		t.Errorf("body = %q, want %q", got, "OK")
	}
	if _, ok := store.faces[42]; !ok {
		t.Error("face of UserID=42 was not persisted")
	}
}

// C2: re-inserting a face for a UserID that already has one -> 400 with FailCodes
// [286064926].
func TestHandleAccessFace_InsertMultiDuplicateRejected(t *testing.T) {
	e, _ := newFaceRouterEmulator()
	r := gin.New()
	r.POST("/cgi-bin/AccessFace.cgi", e.handleAccessFacePost)

	body := fmt.Sprintf(`{"FaceList":[{"UserID":"42","PhotoData":["%s"]}]}`, fakePhoto(200*1024))

	// The first one succeeds.
	req := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first insert failed: %d %s", w.Code, w.Body.String())
	}

	// The second one for the same UserID is rejected.
	req2 := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(body))
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w2.Code, w2.Body.String())
	}
	want := `{"code":268632336,"detail":{"FailCodes":[286064926],"FailCount":1},"message":"Batch Process Error"}`
	if got := w2.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// C3: obviously faceless photo (the 5,425-byte flat gray measured on the
// bench) -> 400 with FailCodes [288686087]. This is the only case
// faceMinBytesApprox still catches correctly - see the constant's comment in
// accessface.go for the full measurement list and the known divergence
// (43,530 bytes).
func TestHandleAccessFace_InsertMultiNoFaceDetected(t *testing.T) {
	e, _ := newFaceRouterEmulator()
	r := gin.New()
	r.POST("/cgi-bin/AccessFace.cgi", e.handleAccessFacePost)

	body := fmt.Sprintf(`{"FaceList":[{"UserID":"7","PhotoData":["%s"]}]}`, fakePhoto(5425))
	req := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	want := `{"code":268632336,"detail":{"FailCodes":[288686087],"FailCount":1},"message":"Batch Process Error"}`
	if got := w.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// C3 (real harness fixture): face_UT-001.jpg is 46,352 bytes and the real
// device ACCEPTS it (it is a real portrait). With faceMinBytesApprox at
// 64 KiB the emulator wrongly rejected this fixture; at 16 KiB it is
// accepted, as it should be.
func TestHandleAccessFace_InsertMultiAcceptsHarnessFixtureSize(t *testing.T) {
	e, store := newFaceRouterEmulator()
	r := gin.New()
	r.POST("/cgi-bin/AccessFace.cgi", e.handleAccessFacePost)

	body := fmt.Sprintf(`{"FaceList":[{"UserID":"55","PhotoData":["%s"]}]}`, fakePhoto(46352))
	req := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "OK" {
		t.Errorf("body = %q, want %q", got, "OK")
	}
	if _, ok := store.faces[55]; !ok {
		t.Error("face of UserID=55 was not persisted")
	}
}

// Known, documented divergence (see faceMinBytesApprox): the 43,530-byte
// case (small face on a large canvas) is rejected by the real device but
// ACCEPTED by the emulator, because the emulator only looks at size, not
// content. This test pins that known behavior - if it starts failing
// because someone "fixed" the threshold, the fix probably reintroduced the
// rejection of valid portraits (exactly what the 46,352-byte fixture proved).
func TestHandleAccessFace_InsertMultiSmallFaceInBigCanvasIsAcceptedDivergenceFromRealDevice(t *testing.T) {
	e, store := newFaceRouterEmulator()
	r := gin.New()
	r.POST("/cgi-bin/AccessFace.cgi", e.handleAccessFacePost)

	body := fmt.Sprintf(`{"FaceList":[{"UserID":"56","PhotoData":["%s"]}]}`, fakePhoto(43530))
	req := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (known divergence from the real device); body=%s", w.Code, w.Body.String())
	}
	if _, ok := store.faces[56]; !ok {
		t.Error("face of UserID=56 was not persisted")
	}
}

// Real driver sequence (harness): insertMulti succeeds, a second insertMulti
// for the same UserID is rejected with 286064926, and the driver resends it
// as updateMulti - which the real device accepts with 200 OK.
func TestHandleAccessFace_UpdateMultiAfterDuplicateSucceeds(t *testing.T) {
	e, store := newFaceRouterEmulator()
	r := gin.New()
	r.POST("/cgi-bin/AccessFace.cgi", e.handleAccessFacePost)

	originalBody := fmt.Sprintf(`{"FaceList":[{"UserID":"77","PhotoData":["%s"]}]}`, fakePhoto(200*1024))

	req1 := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(originalBody))
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("initial insertMulti failed: %d %s", w1.Code, w1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(originalBody))
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	wantDup := `{"code":268632336,"detail":{"FailCodes":[286064926],"FailCount":1},"message":"Batch Process Error"}`
	if w2.Code != http.StatusBadRequest || w2.Body.String() != wantDup {
		t.Fatalf("second insertMulti: status=%d body=%q, want 400 %q", w2.Code, w2.Body.String(), wantDup)
	}

	updatedBody := fmt.Sprintf(`{"FaceList":[{"UserID":"77","PhotoData":["%s"]}]}`, fakePhoto(210*1024))
	req3 := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=updateMulti", strings.NewReader(updatedBody))
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("updateMulti: status = %d, want 200; body=%s", w3.Code, w3.Body.String())
	}
	if got := w3.Body.String(); got != "OK" {
		t.Errorf("updateMulti: body = %q, want %q", got, "OK")
	}
	if _, ok := store.faces[77]; !ok {
		t.Error("face of UserID=77 should still be persisted after updateMulti")
	}
}

// C4: photo too large -> 400 with FailCodes [286064923]. Measured on the
// bench: 384,515 bytes accepted, 423,728 rejected.
func TestHandleAccessFace_InsertMultiTooLargeRejected(t *testing.T) {
	e, _ := newFaceRouterEmulator()
	r := gin.New()
	r.POST("/cgi-bin/AccessFace.cgi", e.handleAccessFacePost)

	body := fmt.Sprintf(`{"FaceList":[{"UserID":"9","PhotoData":["%s"]}]}`, fakePhoto(423728))
	req := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	want := `{"code":268632336,"detail":{"FailCodes":[286064923],"FailCount":1},"message":"Batch Process Error"}`
	if got := w.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// C4 (second limit): a VERY large payload (957,047 bytes measured on the
// bench) does not return the JSON envelope - it returns a plain 400
// "Error\nBad Request!". Reproduced here with faceHardBadRequestBytes+1.
func TestHandleAccessFace_InsertMultiHardLimitReturnsPlainBadRequest(t *testing.T) {
	e, _ := newFaceRouterEmulator()
	r := gin.New()
	r.POST("/cgi-bin/AccessFace.cgi", e.handleAccessFacePost)

	body := fmt.Sprintf(`{"FaceList":[{"UserID":"9","PhotoData":["%s"]}]}`, fakePhoto(faceHardBadRequestBytes+1))
	req := httptest.NewRequest(http.MethodPost, "/cgi-bin/AccessFace.cgi?action=insertMulti", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "Error\nBad Request!" {
		t.Errorf("body = %q, want %q", got, "Error\nBad Request!")
	}
}

// C6: FaceInfoManager.cgi?action=remove is IDEMPOTENT - 200 OK even when the
// UserID has no face enrolled.
func TestHandleFaceInfoManagerGet_RemoveIsIdempotent(t *testing.T) {
	e, _ := newFaceRouterEmulator()
	r := gin.New()
	r.GET("/cgi-bin/FaceInfoManager.cgi", e.handleFaceInfoManagerGet)

	req := httptest.NewRequest(http.MethodGet, "/cgi-bin/FaceInfoManager.cgi?action=remove&UserID=12345", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "OK" {
		t.Errorf("body = %q, want %q", got, "OK")
	}
}

// startFindTotal extracts the Total field from the JSON body of
// FaceInfoManager.cgi?action=startFind.
func startFindTotal(t *testing.T, w *httptest.ResponseRecorder) int {
	t.Helper()
	var resp struct{ Total int }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("startFind response is not valid JSON: %v; body=%s", err, w.Body.String())
	}
	return resp.Total
}

// startFind&Condition.UserID=<id> must return Total = 0 or 1 FOR THAT
// UserID, never the device's global face count. Measured on the bench: the
// real device returns Total=0 for a UserID without a face and Total=1 for a
// UserID with one. Clients use this Total to decide HasFace (vendor spec,
// section 12.3.4).
func TestHandleFaceInfoManagerGet_StartFindTotalIsPerUser(t *testing.T) {
	e, store := newFaceRouterEmulator()
	r := gin.New()
	r.GET("/cgi-bin/FaceInfoManager.cgi", e.handleFaceInfoManagerGet)

	// Noise: another UserID with an enrolled face, to prove Total is not the
	// global count.
	if err := e.repo.AddFace(999, "noise"); err != nil {
		t.Fatalf("setup: AddFace(999) failed: %v", err)
	}

	const userID = "9000077"

	// UserID without a face -> Total=0.
	req1 := httptest.NewRequest(http.MethodGet,
		"/cgi-bin/FaceInfoManager.cgi?action=startFind&Condition.UserID="+userID, nil)
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("startFind (absent): status = %d, want 200; body=%s", w1.Code, w1.Body.String())
	}
	if total := startFindTotal(t, w1); total != 0 {
		t.Errorf("startFind (absent): Total = %d, want 0", total)
	}

	// Enroll this UserID's face -> Total=1.
	if err := e.repo.AddFace(9000077, "hash"); err != nil {
		t.Fatalf("setup: AddFace(9000077) failed: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodGet,
		"/cgi-bin/FaceInfoManager.cgi?action=startFind&Condition.UserID="+userID, nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if total := startFindTotal(t, w2); total != 1 {
		t.Errorf("startFind (with face): Total = %d, want 1", total)
	}

	// Remove this UserID's face -> Total goes back to 0 (and 999's face
	// stays intact, proving remove/query did not leak across users).
	reqRemove := httptest.NewRequest(http.MethodGet,
		"/cgi-bin/FaceInfoManager.cgi?action=remove&UserID="+userID, nil)
	wRemove := httptest.NewRecorder()
	r.ServeHTTP(wRemove, reqRemove)
	if wRemove.Code != http.StatusOK || wRemove.Body.String() != "OK" {
		t.Fatalf("remove: status=%d body=%q, want 200 OK", wRemove.Code, wRemove.Body.String())
	}

	req3 := httptest.NewRequest(http.MethodGet,
		"/cgi-bin/FaceInfoManager.cgi?action=startFind&Condition.UserID="+userID, nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if total := startFindTotal(t, w3); total != 0 {
		t.Errorf("startFind (after remove): Total = %d, want 0", total)
	}
	if _, ok := store.faces[999]; !ok {
		t.Error("another UserID's face (999) must not be affected")
	}
}

// C7: the five combinations below return 501 on the real device.
func TestNotImplementedEndpoints(t *testing.T) {
	e, _ := newFaceRouterEmulator()
	r := gin.New()
	e.SetupRoutes(r)

	cases := []struct {
		method, url string
	}{
		{http.MethodGet, "/cgi-bin/AccessFace.cgi?action=get"},
		{http.MethodGet, "/cgi-bin/FaceInfoManager.cgi?action=list"},
		{http.MethodGet, "/cgi-bin/FaceInfoManager.cgi?action=find"},
		{http.MethodGet, "/cgi-bin/FaceInfoManager.cgi?action=getCollect"},
		{http.MethodGet, "/cgi-bin/AccessUser.cgi?action=remove"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.url, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s: status = %d, want 501; body=%s", c.url, w.Code, w.Body.String())
		}
		if got := w.Body.String(); got != "Error\nNot Implemented!" {
			t.Errorf("%s: body = %q, want %q", c.url, got, "Error\nNot Implemented!")
		}
	}
}

// C8: AccessFace.cgi?action=list -> 400 "Error\nBad Request!" both via GET
// (query params) and via POST (any body tested on the bench).
func TestHandleAccessFace_ListAlwaysBadRequest(t *testing.T) {
	e, _ := newFaceRouterEmulator()
	r := gin.New()
	e.SetupRoutes(r)

	cases := []struct {
		method, url, body string
	}{
		{http.MethodGet, "/cgi-bin/AccessFace.cgi?action=list&count=10&offset=0", ""},
		{http.MethodPost, "/cgi-bin/AccessFace.cgi?action=list", `{"UserIDList":[1,2,3]}`},
		{http.MethodPost, "/cgi-bin/AccessFace.cgi?action=list", `{}`},
	}
	for _, c := range cases {
		var req *http.Request
		if c.body != "" {
			req = httptest.NewRequest(c.method, c.url, strings.NewReader(c.body))
		} else {
			req = httptest.NewRequest(c.method, c.url, nil)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status = %d, want 400; body=%s", c.method, c.url, w.Code, w.Body.String())
		}
		if got := w.Body.String(); got != "Error\nBad Request!" {
			t.Errorf("%s %s: body = %q, want %q", c.method, c.url, got, "Error\nBad Request!")
		}
	}
}

// Checks that the AccessFace.cgi GET/POST and AccessUser.cgi GET routes are
// registered - otherwise everything falls through to NoRoute as a 404.
func TestSetupRoutes_RegistersAccessFaceAndAccessUser(t *testing.T) {
	e, _ := newFaceRouterEmulator()
	r := gin.New()
	e.SetupRoutes(r)

	expected := map[string]bool{
		"GET /cgi-bin/AccessFace.cgi":  false,
		"POST /cgi-bin/AccessFace.cgi": false,
		"GET /cgi-bin/AccessUser.cgi":  false,
	}
	for _, ri := range r.Routes() {
		key := ri.Method + " " + ri.Path
		if _, ok := expected[key]; ok {
			expected[key] = true
		}
	}
	for route, found := range expected {
		if !found {
			t.Errorf("route not registered: %s", route)
		}
	}
}
