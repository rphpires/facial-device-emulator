package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRenderEveryPage renders each page with a representative context and
// checks that the HTML closes. A template execution error (missing field,
// wrong type in a helper) breaks neither the build nor the parse: it aborts
// rendering halfway and the page arrives truncated.
func TestRenderEveryPage(t *testing.T) {
	cases := []struct {
		page string
		data gin.H
	}{
		{"devices.html", gin.H{
			"fleet": []deviceView{{
				ID: 123, Name: "North Gate", Model: "Hikvision", Port: 7070,
				LogEnabled: 1, Status: "running", Interval: 30, TotalUsers: 412,
				Mode: modeOnline,
			}},
			"counter_cards": FleetCounts{Total: 3, Running: 1, Stopped: 1, Disabled: 1}.toMap(),
		}},
		{"error.html", gin.H{"error": "connection refused"}},
	}

	for _, tc := range cases {
		t.Run(tc.page, func(t *testing.T) {
			body := renderWith(t, tc.page, http.StatusOK, tc.data).Body.String()
			if !strings.Contains(body, "</html>") {
				t.Fatalf("render of %s was truncated; the HTML does not close:\n%s", tc.page, lastBytes(body, 800))
			}
			if !strings.Contains(body, "fleet-meter") {
				t.Errorf("%s: the header did not render", tc.page)
			}
			if !strings.Contains(body, `<html lang="en">`) {
				t.Errorf("%s: want lang=\"en\"", tc.page)
			}
		})
	}
}

// lastBytes returns the tail of s, so a failure shows where rendering
// stopped.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// renderDevices renders devices.html with the minimum context it needs.
func renderDevices(t *testing.T, fleet []deviceView) string {
	t.Helper()
	return renderWith(t, "devices.html", http.StatusOK, gin.H{
		"fleet":         fleet,
		"counter_cards": FleetCounts{}.toMap(),
	}).Body.String()
}

// The table is drawn client-side; what the server guarantees is the fleet
// embedded as valid JSON for the first paint.
func TestDevicesHTMLEmbedsFleetAsJSON(t *testing.T) {
	html := renderDevices(t, []deviceView{{
		ID: 900001, Name: "lab </script> 4000", Model: "Dahua", Port: 4000,
		Status: "stopped", Enabled: 1, Interval: 10, Mode: modeStandalone,
	}})

	re := regexp.MustCompile(`(?s)<script type="application/json" id="fleet-initial">(.*?)</script>`)
	m := re.FindStringSubmatch(html)
	if m == nil {
		t.Fatal("<script id=\"fleet-initial\"> not found")
	}

	var fleet []deviceView
	if err := json.Unmarshal([]byte(m[1]), &fleet); err != nil {
		t.Fatalf("embedded fleet is not valid JSON: %v\n%s", err, m[1])
	}
	if len(fleet) != 1 || fleet[0].ID != 900001 {
		t.Errorf("embedded fleet = %+v", fleet)
	}
	if fleet[0].Name != "lab </script> 4000" {
		t.Errorf("name with </script> did not survive escaping: %q", fleet[0].Name)
	}
}

func TestDevicesHTMLHasCreateControls(t *testing.T) {
	html := renderDevices(t, nil)

	for _, id := range []string{
		"new-emulator", "new-emulator-range", "emulator-form-modal",
		"device-rows", "bulk-bar", "filter-q", "pager-pages",
	} {
		if !strings.Contains(html, id) {
			t.Errorf("want element %q on the page", id)
		}
	}
}
