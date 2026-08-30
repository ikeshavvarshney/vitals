package dash

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The bookmarklet and its receiver page are asserted here rather than in a
// browser test, because the browser test lives outside this repository and a
// broken bookmarklet is otherwise silent: it fails on someone else's page.
func TestSnapshotAssetsServed(t *testing.T) {
	tests := []struct {
		target   string
		wantType string
		contains string
	}{
		{"/snapshot.html", "text/html; charset=utf-8", "Snapshot receiver"},
		{"/snapshot.js", "text/javascript; charset=utf-8", "/v1/collect"},
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
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if !strings.Contains(rec.Body.String(), tt.contains) {
				t.Errorf("body does not contain %q", tt.contains)
			}
		})
	}
}

// The bookmarklet program must observe every entry type the beacon does, or a
// snapshot silently reports fewer metrics than an installed beacon would.
func TestBookmarkletObservesEveryMetric(t *testing.T) {
	js := assetBody(t, "/dash.js")

	start := strings.Index(js, "function snapshotProgram(")
	if start < 0 {
		t.Fatal("dash.js no longer defines snapshotProgram")
	}
	program := js[start:]

	for _, entryType := range []string{
		"navigation", "paint", "largest-contentful-paint", "layout-shift", "event",
	} {
		if !strings.Contains(program, "'"+entryType+"'") {
			t.Errorf("bookmarklet does not observe %q", entryType)
		}
	}
	for _, key := range []string{"lcp", "inp", "cls", "fcp", "ttfb"} {
		if !strings.Contains(program, "m."+key) {
			t.Errorf("bookmarklet never sets %q", key)
		}
	}
}

// The handoff is the whole reason this feature works: Chrome refuses a request
// from a public page to a loopback address, but not a top-level navigation.
// A change from window.open back to fetch would pass every Go test and fail on
// every real site, so it is asserted directly.
func TestBookmarkletHandsOffByNavigation(t *testing.T) {
	js := assetBody(t, "/dash.js")

	if !strings.Contains(js, "window.open(base + '/snapshot.html#'") {
		t.Error("bookmarklet no longer hands off by navigating to the receiver page")
	}
	if strings.Contains(js[strings.Index(js, "function snapshotProgram("):], "fetch(") {
		t.Error("bookmarklet posts directly, which Chrome blocks from a public page to loopback")
	}
}

// The route must carry the host, or every measured site collapses into the
// same handful of paths in the dashboard.
func TestBookmarkletReportsHostQualifiedRoute(t *testing.T) {
	js := assetBody(t, "/dash.js")
	if !strings.Contains(js, "location.host + location.pathname") {
		t.Error("bookmarklet does not report a host-qualified route")
	}
}

// The receiver clears the fragment after recording, so a reload does not store
// the same measurement again.
func TestSnapshotReceiverClearsFragment(t *testing.T) {
	js := assetBody(t, "/snapshot.js")
	if !strings.Contains(js, "history.replaceState") {
		t.Error("receiver does not clear the fragment after recording")
	}
}
