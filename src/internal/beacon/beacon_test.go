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

// TestFullSizeBudget protects the full build's separate budget. It is allowed
// to be bigger than the default beacon, but not unbounded: the point of the
// project is that a hand-written script beats the package it replaces on size
// as well as on dependencies.
func TestFullSizeBudget(t *testing.T) {
	got := FullSize()
	if got > MaxFullBytes {
		t.Fatalf("beacon.full.min.js is %d bytes, over the %d byte budget by %d",
			got, MaxFullBytes, got-MaxFullBytes)
	}
	if got <= Size() {
		t.Errorf("full beacon is %d bytes and the default is %d; the full build "+
			"should be the larger of the two", got, Size())
	}
	t.Logf("beacon.full.min.js: %d bytes raw, %d under budget", got, MaxFullBytes-got)
}

func TestFullScriptIsNotEmpty(t *testing.T) {
	if len(FullScript()) == 0 {
		t.Fatal("minified full beacon is empty")
	}
	if len(FullSource()) == 0 {
		t.Fatal("full beacon source is empty")
	}
	if len(FullSource()) <= len(FullScript()) {
		t.Error("the commented source is not larger than the minified script; they may be swapped")
	}
}

func TestFullMinifiedIsActuallyMinified(t *testing.T) {
	s := string(FullScript())

	if strings.Contains(s, "/*") || strings.Contains(s, "\n *") {
		t.Error("minified full beacon contains block comments")
	}
	if n := strings.Count(s, "\n"); n > 1 {
		t.Errorf("minified full beacon has %d newlines, want at most a trailing one", n)
	}
	if strings.Contains(s, "  ") {
		t.Error("minified full beacon contains runs of indentation")
	}
}

// TestFullSourceAndMinifiedAgree is [TestSourceAndMinifiedAgree] for the full
// build. It cannot prove equivalence without a JavaScript parser, which would
// be a dependency, so it asserts the identifiers that would actually drift:
// every feature this build exists to add.
func TestFullSourceAndMinifiedAgree(t *testing.T) {
	src := string(FullSource())
	min := string(FullScript())

	required := []string{
		// Metric keys written into the payload.
		"ttfb", "fcp", "lcp", "cls", "inp",
		// Entry types observed.
		"navigation", "paint", "largest-contentful-paint", "layout-shift", "event",
		// Transport and payload keys.
		"/v1/collect", "sendBeacon", "keepalive", "visibilitychange",
		"pathname", "innerWidth", "hadRecentInput",
		// Real INP: interactions are grouped rather than maxed.
		"interactionId", "duration",
		// Back-forward cache.
		"pageshow", "persisted",
		// Soft navigations.
		"pushState", "replaceState", "popstate", "soft-navigation",
		// Prerender correction.
		"activationStart", "prerender",
		// Attribution.
		"sources", "element", "target", "nodeType",
		// Page-view identity and the first-hidden discard.
		"visibilityState", "pagehide",
	}

	for _, token := range required {
		if !strings.Contains(src, token) {
			t.Errorf("beacon.full.src.js is missing %q", token)
		}
		if !strings.Contains(min, token) {
			t.Errorf("beacon.full.min.js is missing %q", token)
		}
	}

	// The INP percentile constants: ten interactions retained, one discarded
	// per fifty. They may be inlined as bare numbers in the minified file.
	for _, want := range []string{"10", "50"} {
		if !strings.Contains(min, want) {
			t.Errorf("beacon.full.min.js has no %s constant for the INP percentile", want)
		}
	}
}

func TestFullNoNetworkReferences(t *testing.T) {
	for name, body := range map[string][]byte{
		"beacon.full.min.js": FullScript(),
		"beacon.full.src.js": FullSource(),
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

func TestHandlerServesFullBeacon(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	tests := []struct {
		name       string
		target     string
		wantBody   []byte
		wantSource string
	}{
		{"minified full script", FullPath, FullScript(), "/beacon.full.src.js"},
		{"readable full source", "/beacon.full.src.js", FullSource(), "/beacon.full.src.js"},
		{"default script keeps its own headers", Path, Script(), "/beacon.src.js"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !bytes.Equal(rec.Body.Bytes(), tt.wantBody) {
				t.Error("served body does not match the embedded file")
			}
			if got := rec.Header().Get("X-Beacon-Source"); got != tt.wantSource {
				t.Errorf("X-Beacon-Source = %q, want %q", got, tt.wantSource)
			}
		})
	}
}

// TestBuildsDescribeWhatIsServed keeps the listing the dashboard and the size
// tool read from drifting away from the files actually embedded.
func TestBuildsDescribeWhatIsServed(t *testing.T) {
	builds := Builds()
	if len(builds) != 2 {
		t.Fatalf("Builds() returned %d entries, want 2", len(builds))
	}

	want := []struct {
		path  string
		bytes int
		max   int
	}{
		{Path, Size(), MaxBytes},
		{FullPath, FullSize(), MaxFullBytes},
	}

	for i, w := range want {
		got := builds[i]
		if got.Path != w.path || got.Bytes != w.bytes || got.MaxBytes != w.max {
			t.Errorf("Builds()[%d] = %+v, want path %q, %d bytes, budget %d",
				i, got, w.path, w.bytes, w.max)
		}
		if got.Summary == "" {
			t.Errorf("Builds()[%d] has no summary", i)
		}
	}

	if builds[0].Bytes >= builds[1].Bytes {
		t.Error("Builds() is not ordered smallest first")
	}
}
