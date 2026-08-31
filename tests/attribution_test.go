package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"vitals/src/server"
)

// post sends a payload the way sendBeacon does and fails on anything but 204.
func post(t *testing.T, url, payload string) {
	t.Helper()

	resp, err := http.Post(url+"/v1/collect", "text/plain", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("posting payload: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("collect status = %d, want 204", resp.StatusCode)
	}
}

// reportDoc is the subset of GET /api/report this file asserts on.
type reportDoc struct {
	PageViews  int `json:"pageViews"`
	Navigation []struct {
		Type    string `json:"type"`
		Samples uint64 `json:"samples"`
	} `json:"navigation"`
	Ingest struct {
		Accepted  uint64 `json:"accepted"`
		Duplicate uint64 `json:"duplicate"`
	} `json:"ingest"`
	Metrics []struct {
		Metric    string `json:"metric"`
		Offenders []struct {
			Selector string `json:"selector"`
			Samples  uint64 `json:"samples"`
			Poor     uint64 `json:"poor"`
		} `json:"offenders"`
	} `json:"metrics"`
}

// fetchReport reads GET /api/report over HTTP.
func fetchReport(t *testing.T, url string) reportDoc {
	t.Helper()

	resp, err := http.Get(url + "/api/report?from=24h")
	if err != nil {
		t.Fatalf("GET /api/report: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report status = %d, want 200", resp.StatusCode)
	}

	var doc reportDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decoding report: %v", err)
	}
	return doc
}

// offendersOf returns one metric's offenders from a report document.
func offendersOf(t *testing.T, doc reportDoc, metric string) []struct {
	Selector string `json:"selector"`
	Samples  uint64 `json:"samples"`
	Poor     uint64 `json:"poor"`
} {
	t.Helper()

	for _, m := range doc.Metrics {
		if m.Metric == metric {
			return m.Offenders
		}
	}
	t.Fatalf("report has no entry for %s", metric)
	return nil
}

// TestFullBeaconPayloadReachesTheReport is the end-to-end test for everything
// the full beacon adds: the element blamed for a metric, the navigation type,
// and the page-view identifier, posted the way a browser posts them and read
// back out of the API.
func TestFullBeaconPayloadReachesTheReport(t *testing.T) {
	srv, app := newServer(t)

	post(t, srv.URL, `{"u":"/checkout","t":1756500000000,"w":390,`+
		`"i":"k3f9a1b2mfz7q","n":"soft-navigation",`+
		`"m":{"lcp":6000,"cls":0.4,"inp":700},`+
		`"a":{"lcp":"img.hero","cls":"div#promo","inp":"button.add-to-cart"}}`)

	if app.Records() != 1 {
		t.Fatalf("store holds %d records, want 1", app.Records())
	}

	doc := fetchReport(t, srv.URL)

	for metric, want := range map[string]string{
		"lcp": "img.hero",
		"cls": "div#promo",
		"inp": "button.add-to-cart",
	} {
		got := offendersOf(t, doc, metric)
		if len(got) != 1 {
			t.Errorf("%s: got %d offenders, want 1: %+v", metric, len(got), got)
			continue
		}
		if got[0].Selector != want {
			t.Errorf("%s: offender = %q, want %q", metric, got[0].Selector, want)
		}
		// Every value posted is in the poor band, so the poor count is the
		// sample count. A zero here would mean the band was never consulted.
		if got[0].Poor != 1 {
			t.Errorf("%s: poor = %d, want 1", metric, got[0].Poor)
		}
	}

	if len(doc.Navigation) != 1 || doc.Navigation[0].Type != "soft-navigation" {
		t.Errorf("Navigation = %+v, want one soft-navigation", doc.Navigation)
	}
}

// TestSmallBeaconPayloadReportsNoAttribution keeps the two beacons honest about
// each other: the small one sends none of these fields and the report must show
// their absence rather than inventing a value.
func TestSmallBeaconPayloadReportsNoAttribution(t *testing.T) {
	srv, _ := newServer(t)

	post(t, srv.URL, `{"u":"/","t":1756500000000,"w":1440,"m":{"lcp":6000}}`)

	doc := fetchReport(t, srv.URL)
	if doc.PageViews != 1 {
		t.Fatalf("pageViews = %d, want 1", doc.PageViews)
	}
	if got := offendersOf(t, doc, "lcp"); len(got) != 0 {
		t.Errorf("offenders = %+v, want none from the small beacon", got)
	}
	if len(doc.Navigation) != 0 {
		t.Errorf("Navigation = %+v, want none from the small beacon", doc.Navigation)
	}
}

// TestDuplicatePayloadIsStoredOnce covers the race the page-view identifier
// exists for: sendBeacon and the keepalive fetch fallback both arriving.
func TestDuplicatePayloadIsStoredOnce(t *testing.T) {
	srv, app := newServer(t)

	payload := `{"u":"/","t":1756500000000,"w":1440,"i":"abc123def","m":{"lcp":1000}}`
	post(t, srv.URL, payload)
	post(t, srv.URL, payload)

	if app.Records() != 1 {
		t.Fatalf("store holds %d records, want 1", app.Records())
	}

	doc := fetchReport(t, srv.URL)
	if doc.Ingest.Accepted != 1 {
		t.Errorf("accepted = %d, want 1", doc.Ingest.Accepted)
	}
	if doc.Ingest.Duplicate != 1 {
		t.Errorf("duplicate = %d, want 1", doc.Ingest.Duplicate)
	}
}

// TestBothBeaconsAreServedWithinBudget checks the size claim against what is
// actually served rather than against the file on disk.
func TestBothBeaconsAreServedWithinBudget(t *testing.T) {
	srv, _ := newServer(t)

	tests := []struct {
		name   string
		path   string
		max    int
		expect string
	}{
		{"small beacon", server.BeaconPath, server.BeaconMaxBytes, "PerformanceObserver"},
		{"full beacon", server.FullBeaconPath, server.FullBeaconMaxBytes, "interactionId"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tt.path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading %s: %v", tt.path, err)
			}
			if len(body) > tt.max {
				t.Fatalf("served %d bytes, over the %d byte budget", len(body), tt.max)
			}
			if !strings.Contains(string(body), tt.expect) {
				t.Errorf("served script does not contain %q", tt.expect)
			}
		})
	}

	if server.FullBeaconBytes() <= server.BeaconBytes() {
		t.Errorf("full beacon is %d bytes and the small one is %d; the full build should be larger",
			server.FullBeaconBytes(), server.BeaconBytes())
	}
}

// TestFullBeaconIsNotOnTheDefaultDemoPage documents the demo's own split: the
// control page carries the small beacon, the three broken pages carry the full
// one, and a judge clicking through sees both halves of the claim.
func TestFullBeaconIsNotOnTheDefaultDemoPage(t *testing.T) {
	srv, _ := newServer(t)

	tests := []struct {
		page string
		want string
	}{
		{"/demo/", server.BeaconPath},
		{"/demo/heavy.html", server.FullBeaconPath},
		{"/demo/shifty.html", server.FullBeaconPath},
		{"/demo/blocking.html", server.FullBeaconPath},
	}

	for _, tt := range tests {
		t.Run(tt.page, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tt.page)
			if err != nil {
				t.Fatalf("GET %s: %v", tt.page, err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading %s: %v", tt.page, err)
			}

			if !strings.Contains(string(body), `src="`+tt.want+`"`) {
				t.Errorf("%s does not load %s", tt.page, tt.want)
			}
		})
	}
}
