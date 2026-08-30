package beacon

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSizeBudget is the test that protects the central claim of the project.
// If the beacon grows past its budget, the README stops being true and the
// build fails here rather than in front of a judge.
func TestSizeBudget(t *testing.T) {
	got := Size()
	if got > MaxBytes {
		t.Fatalf("beacon.min.js is %d bytes, over the %d byte budget by %d",
			got, MaxBytes, got-MaxBytes)
	}
	t.Logf("beacon.min.js: %d bytes raw, %d under budget", got, MaxBytes-got)
}

func TestGzippedSizeIsReported(t *testing.T) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatalf("gzip writer: %v", err)
	}
	if _, err := zw.Write(Script()); err != nil {
		t.Fatalf("compressing: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing gzip stream: %v", err)
	}

	if buf.Len() >= Size() {
		t.Errorf("gzipped beacon is %d bytes, raw is %d; compression did not help",
			buf.Len(), Size())
	}
	t.Logf("beacon.min.js: %d bytes gzipped", buf.Len())
}

func TestScriptIsNotEmpty(t *testing.T) {
	if len(Script()) == 0 {
		t.Fatal("minified beacon is empty")
	}
	if len(Source()) == 0 {
		t.Fatal("beacon source is empty")
	}
	if len(Source()) <= len(Script()) {
		t.Error("the commented source is not larger than the minified script; they may be swapped")
	}
}

// TestMinifiedIsActuallyMinified guards the one mistake that would silently
// break the size claim: shipping the readable file under the minified name.
func TestMinifiedIsActuallyMinified(t *testing.T) {
	s := string(Script())

	if strings.Contains(s, "/*") || strings.Contains(s, "\n *") {
		t.Error("minified beacon contains block comments")
	}
	if n := strings.Count(s, "\n"); n > 1 {
		t.Errorf("minified beacon has %d newlines, want at most a trailing one", n)
	}
	if strings.Contains(s, "  ") {
		t.Error("minified beacon contains runs of indentation")
	}
}

// TestSourceAndMinifiedAgree checks that the two files describe the same
// program. It cannot prove equivalence without a JavaScript parser, which would
// be a dependency, so it asserts the things that would actually drift: every
// metric key, every observed entry type, and the endpoint.
func TestSourceAndMinifiedAgree(t *testing.T) {
	src := string(Source())
	min := string(Script())

	required := []string{
		// Metric keys written into the payload.
		"ttfb", "fcp", "lcp", "cls", "inp",
		// Entry types observed.
		"navigation", "paint", "largest-contentful-paint", "layout-shift", "event",
		// Transport.
		"/v1/collect", "sendBeacon", "keepalive", "visibilitychange",
		// Payload keys.
		"pathname", "innerWidth",
		// CLS session window constants, in either notation.
		"hadRecentInput",
	}

	for _, token := range required {
		if !strings.Contains(src, token) {
			t.Errorf("beacon.src.js is missing %q", token)
		}
		if !strings.Contains(min, token) {
			t.Errorf("beacon.min.js is missing %q", token)
		}
	}

	// The session window thresholds appear as 1000/5000 in the source and may
	// appear as 1e3/5e3 in the minified file.
	if !strings.Contains(min, "1e3") && !strings.Contains(min, "1000") {
		t.Error("beacon.min.js has no 1s session window threshold")
	}
	if !strings.Contains(min, "5e3") && !strings.Contains(min, "5000") {
		t.Error("beacon.min.js has no 5s session window threshold")
	}
}

// TestNoNetworkReferences asserts the beacon talks to its own origin only. A
// CDN or third-party host here would defeat the whole premise.
func TestNoNetworkReferences(t *testing.T) {
	for name, body := range map[string][]byte{
		"beacon.min.js": Script(),
		"beacon.src.js": Source(),
	} {
		s := string(body)
		for _, bad := range []string{"http://", "https://", "//cdn", "fonts.googleapis"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s references %q; the beacon must only call its own origin", name, bad)
			}
		}
		if !strings.Contains(s, `"/v1/collect"`) && !strings.Contains(s, `'/v1/collect'`) {
			t.Errorf("%s does not post to the relative collect endpoint", name)
		}
	}
}

func TestHandlerServesBeacon(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	tests := []struct {
		name     string
		target   string
		wantBody []byte
	}{
		{"minified script", Path, Script()},
		{"readable source", "/beacon.src.js", Source()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
				t.Errorf("Content-Type = %q, want JavaScript", ct)
			}
			if !bytes.Equal(rec.Body.Bytes(), tt.wantBody) {
				t.Error("served body does not match the embedded file")
			}
			if rec.Header().Get("ETag") == "" {
				t.Error("no ETag on the beacon response")
			}
		})
	}
}

func TestHandlerAdvertisesSizeAndSource(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, Path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Beacon-Source"); got != "/beacon.src.js" {
		t.Errorf("X-Beacon-Source = %q, want /beacon.src.js", got)
	}
	if got := rec.Header().Get("X-Beacon-Bytes"); got == "" {
		t.Error("X-Beacon-Bytes header is missing")
	}
}

func TestHandlerNotFound(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/nope.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
