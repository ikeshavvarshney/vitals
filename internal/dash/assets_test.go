package dash

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// assetBody fetches one embedded asset and returns its body.
func assetBody(t *testing.T, target string) string {
	t.Helper()

	h, err := Assets()
	if err != nil {
		t.Fatalf("Assets: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", target, rec.Code)
	}
	return rec.Body.String()
}

func TestAssetsServed(t *testing.T) {
	tests := []struct {
		target   string
		wantType string
		contains string
	}{
		{"/", "text/html; charset=utf-8", "<title>vitals</title>"},
		{"/index.html", "text/html; charset=utf-8", "scorecard"},
		{"/dash.css", "text/css; charset=utf-8", "--good"},
		{"/dash.js", "text/javascript; charset=utf-8", "renderScorecard"},
	}

	h, err := Assets()
	if err != nil {
		t.Fatalf("Assets: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if !strings.Contains(rec.Body.String(), tt.contains) {
				t.Errorf("body does not contain %q", tt.contains)
			}
			if rec.Header().Get("ETag") == "" {
				t.Error("no ETag")
			}
		})
	}
}

// TestNoRemoteReferences is the check that would catch the single mistake most
// likely to sink this submission: a CDN link or a web font in the dashboard.
// tools/check enforces this repository-wide; this asserts it for the assets
// that actually ship inside the binary.
func TestNoRemoteReferences(t *testing.T) {
	forbidden := []string{
		"http://", "https://",
		"fonts.googleapis.com", "fonts.gstatic.com",
		"cdn.", "unpkg.com", "jsdelivr",
		"@import url(",
	}

	for _, name := range []string{"/index.html", "/dash.css", "/dash.js"} {
		body := assetBody(t, name)
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				// The SVG favicon carries an XML namespace URL, which is an
				// identifier rather than a fetched resource.
				if bad == "http://" && strings.Contains(body, "www.w3.org/2000/svg") {
					continue
				}
				t.Errorf("%s contains %q", name, bad)
			}
		}
	}
}

func TestNoWebFonts(t *testing.T) {
	css := assetBody(t, "/dash.css")

	if strings.Contains(css, "@font-face") {
		t.Error("dash.css declares @font-face; the system font stack is the only option")
	}
	for _, want := range []string{"system-ui", "ui-monospace"} {
		if !strings.Contains(css, want) {
			t.Errorf("dash.css does not use %q in its font stack", want)
		}
	}
}

// TestAccessibilityFloor asserts the non-negotiable floor from the design
// notes. These are checked as text because there is no DOM here, and a missing
// viewport tag or focus style is exactly the kind of thing that regresses
// silently.
func TestAccessibilityFloor(t *testing.T) {
	html := assetBody(t, "/index.html")
	css := assetBody(t, "/dash.css")

	htmlChecks := map[string]string{
		`<html lang="en">`:   "no language declared",
		`name="viewport"`:    "no viewport meta tag, so mobile will not scale",
		`class="skip"`:       "no skip link",
		`role="status"`:      "status region is not announced",
		`aria-live="polite"`: "status region does not announce updates",
		`aria-labelledby`:    "sections are not labelled",
	}
	for token, complaint := range htmlChecks {
		if !strings.Contains(html, token) {
			t.Errorf("index.html: %s (missing %q)", complaint, token)
		}
	}

	cssChecks := map[string]string{
		":focus-visible":             "no visible focus style",
		"prefers-reduced-motion":     "motion preference not respected",
		"@media (max-width: 560px)":  "no small-screen layout",
		"prefers-color-scheme: dark": "no dark scheme",
	}
	for token, complaint := range cssChecks {
		if !strings.Contains(css, token) {
			t.Errorf("dash.css: %s (missing %q)", complaint, token)
		}
	}
}

// TestHonestyNotesArePresent guards the disclosures. If someone removes the
// paragraph explaining that INP is approximated, the dashboard starts making a
// claim the code does not support.
func TestHonestyNotesArePresent(t *testing.T) {
	html := assetBody(t, "/index.html")

	required := []string{
		"Percentiles are approximate",
		"INP is approximated",
		"4.9%",
		"Buffered writes",
	}
	for _, token := range required {
		if !strings.Contains(html, token) {
			t.Errorf("index.html no longer discloses %q", token)
		}
	}
}

func TestChartsUseInlineSVG(t *testing.T) {
	js := assetBody(t, "/dash.js")

	if !strings.Contains(js, "createElementNS") {
		t.Error("dash.js does not build SVG elements; charts must be inline SVG")
	}
	if strings.Contains(js, "getContext") {
		t.Error("dash.js uses a canvas context; the design calls for SVG")
	}
	for _, forbidden := range []string{"Chart(", "d3.", "import ", "require("} {
		if strings.Contains(js, forbidden) {
			t.Errorf("dash.js references %q, which implies a library", forbidden)
		}
	}
}

// TestExportControlsAreWired guards the ids the export section shares between
// the markup and the script. A rename in one file alone leaves a dead button
// that fails silently in the browser.
func TestExportControlsAreWired(t *testing.T) {
	html := assetBody(t, "/index.html")
	js := assetBody(t, "/dash.js")

	for _, id := range []string{"copy-json", "download-json", "copy-prompt", "export-status", "export-text"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("index.html has no element with id %q", id)
		}
		if !strings.Contains(js, `'`+id+`'`) {
			t.Errorf("dash.js never looks up id %q", id)
		}
	}

	if !strings.Contains(js, "/api/report") {
		t.Error("dash.js does not read /api/report; the export must not restitch the on-screen figures")
	}
}
