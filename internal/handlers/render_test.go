package handlers

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func renderWith(t *testing.T, page string, status int, data gin.H) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handler{templates: buildTemplateCache(), appVersion: "test"}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	h.renderPage(c, page, status, data)
	return rec
}

// TestRenderPage_Error checks that a failure renders a real error page
// inside the application shell, not a blank 500.
func TestRenderPage_Error(t *testing.T) {
	rec := renderWith(t, "error.html", http.StatusInternalServerError, gin.H{
		"error": "connection refused by the database",
	})

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "connection refused by the database") {
		t.Errorf("the error message did not reach the HTML:\n%s", body)
	}
	if !strings.Contains(body, "Facial Device Emulator") {
		t.Errorf("the error page should be inside the application shell:\n%s", body)
	}
}

// TestBuildTemplateCache_CoversEveryPage ensures no page is missing from
// the startup cache; a missing page would only show up as a nil template
// on first access.
func TestBuildTemplateCache_CoversEveryPage(t *testing.T) {
	cache := buildTemplateCache()

	for _, name := range []string{"devices.html", "error.html"} {
		if cache[name] == nil {
			t.Errorf("template %q missing from cache", name)
		}
	}
}

// TestRenderPage_EscapesHTML locks in html/template. Device names are
// user-supplied and interpolated into the page; text/template would inject
// them raw.
func TestRenderPage_EscapesHTML(t *testing.T) {
	rec := renderWith(t, "error.html", http.StatusInternalServerError, gin.H{
		"error": `<script>alert(1)</script>`,
	})

	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("payload markup was not escaped; html/template is not in use:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the escaped text is missing from the page:\n%s", body)
	}
}

// TestRenderDevices_ModeColumn checks the Mode column header. The per-row
// selector (Dahua only) is drawn by devices.js from the fleet's mode field.
func TestRenderDevices_ModeColumn(t *testing.T) {
	body := renderDevices(t, []deviceView{{ID: 1, Model: "Dahua", Mode: modeStandalone}})

	if !strings.Contains(body, "<th>Mode</th>") {
		t.Errorf("the grid has no Mode column header:\n%s", body)
	}
	if !strings.Contains(body, `"mode":"standalone"`) {
		t.Errorf("the stored mode is missing from the embedded fleet:\n%s", body)
	}
}

// TestRenderDevices_ReachabilityBlock checks that the notice container
// exists and starts hidden. It only has content when /api/reachability
// reports an unreachable device; a visible empty block would be worse than
// none.
func TestRenderDevices_ReachabilityBlock(t *testing.T) {
	body := renderDevices(t, []deviceView{})

	for _, id := range []string{
		`id="reachability-alert-row"`,
		`id="reachability-alert"`,
		`id="reachability-headline"`,
		`id="reachability-reason"`,
		`id="reachability-list"`,
		`id="reachability-toggle"`,
	} {
		if !strings.Contains(body, id) {
			t.Errorf("marker missing from the devices page: %s", id)
		}
	}
	if !strings.Contains(body, `id="reachability-alert-row" hidden`) {
		t.Errorf("the notice block should start hidden")
	}
	// The console has no Bootstrap. A framework class here means old markup
	// was pasted back in.
	for _, banned := range []string{"alert-warning", "btn-link", "text-muted", "bi bi-"} {
		if strings.Contains(body, banned) {
			t.Errorf("Bootstrap class reintroduced (%q); use the console components", banned)
		}
	}
}

// TestRenderDevices_BatchBar checks that the batch actions start hidden
// inside the selection bar. Visible with no selection, they would confirm
// an irreversible operation on nothing; devices.js reveals the bar only
// when a device is selected.
func TestRenderDevices_BatchBar(t *testing.T) {
	body := renderDevices(t, nil)

	re := regexp.MustCompile(`(?s)<div class="bulk-bar" id="bulk-bar"[^>]*hidden>(.*?)</div>`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("batch action bar missing or already visible:\n%s", body)
	}
	for _, action := range []string{"start", "stop", "enable", "disable", "delete"} {
		if !strings.Contains(m[1], `data-batch="`+action+`"`) {
			t.Errorf("batch action %q missing from the selection bar", action)
		}
	}
}
