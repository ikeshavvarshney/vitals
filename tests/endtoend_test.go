// Package tests holds the black-box tests: they drive the server the binary
// serves, over HTTP, through its public surface only.
//
// Unit tests live beside the code they cover, which is where Go looks for them
// and where they can reach the unexported parsers and arithmetic that carry
// most of the risk. See README.md in this directory.
package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vitals/src/server"
)

// newServer returns a live test server running the real route table over a
// temporary data directory.
func newServer(t *testing.T) (*httptest.Server, *server.Server) {
	t.Helper()

	app, err := server.Open(t.TempDir())
	if err != nil {
		t.Fatalf("server.Open: %v", err)
	}
	t.Cleanup(func() { app.Close() })

	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	return srv, app
}

// TestEndToEnd is the test that proves the product works: a beacon payload is
// posted the way a browser posts it, and the number comes back out of the API.
func TestEndToEnd(t *testing.T) {
	srv, app := newServer(t)

	payload := `{"u":"/pricing","t":1756500000000,"w":1440,` +
		`"m":{"lcp":1834.2,"cls":0.06,"inp":142,"ttfb":210.5,"fcp":903.1}}`

	// sendBeacon posts text/plain, which is what makes the request CORS-simple.
	resp, err := http.Post(srv.URL+"/v1/collect", "text/plain", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("posting payload: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("collect status = %d, want 204", resp.StatusCode)
	}
	if app.Records() != 1 {
		t.Fatalf("store holds %d records, want 1", app.Records())
	}

	// The measurement must now be visible through the dashboard's own API.
	var summary struct {
		Samples int `json:"samples"`
		Metrics []struct {
			Metric  string   `json:"metric"`
			Value   *float64 `json:"value"`
			Band    string   `json:"band"`
			Samples uint64   `json:"samples"`
		} `json:"metrics"`
		Ingest struct {
			Accepted uint64 `json:"accepted"`
		} `json:"ingest"`
	}
	getJSON(t, srv.URL+"/api/summary?from=24h", &summary)

	if summary.Samples != 1 {
		t.Errorf("summary samples = %d, want 1", summary.Samples)
	}
	if summary.Ingest.Accepted != 1 {
		t.Errorf("accepted = %d, want 1", summary.Ingest.Accepted)
	}

	found := false
	for _, m := range summary.Metrics {
		if m.Metric != "lcp" {
			continue
		}
		found = true
		if m.Value == nil {
			t.Fatal("lcp value is null after ingesting a measurement")
		}
		// 1834.2 lands within one bucket of itself.
		if *m.Value < 1834.2*0.95 || *m.Value > 1834.2*1.05 {
			t.Errorf("lcp value = %v, want about 1834.2", *m.Value)
		}
		if m.Band != "good" {
			t.Errorf("lcp band = %q, want good", m.Band)
		}
	}
	if !found {
		t.Error("no lcp entry in the summary")
	}

	// The route breakdown must show the page the beacon reported.
	var routes struct {
		Rows []struct {
			Key     string `json:"key"`
			Samples uint64 `json:"samples"`
		} `json:"rows"`
	}
	getJSON(t, srv.URL+"/api/routes?from=24h&metric=lcp", &routes)

	if len(routes.Rows) != 1 || routes.Rows[0].Key != "/pricing" {
		t.Errorf("routes = %+v, want one row for /pricing", routes.Rows)
	}
}

func TestEveryRouteIsReachable(t *testing.T) {
	srv, _ := newServer(t)

	tests := []struct {
		path     string
		wantType string
		contains string
	}{
		{"/", "text/html; charset=utf-8", "<title>vitals</title>"},
		{"/dash.css", "text/css; charset=utf-8", "--good"},
		{"/dash.js", "text/javascript; charset=utf-8", "renderScorecard"},
		{"/snapshot.html", "text/html; charset=utf-8", "Snapshot receiver"},
		{"/snapshot.js", "text/javascript; charset=utf-8", "/v1/collect"},
		{server.BeaconPath, "text/javascript; charset=utf-8", "PerformanceObserver"},
		{"/beacon.src.js", "text/javascript; charset=utf-8", "vitals beacon"},
		{"/demo/", "text/html; charset=utf-8", "A fast page"},
		{"/demo/heavy.html", "text/html; charset=utf-8", "heavy hero image"},
		{"/demo/shifty.html", "text/html; charset=utf-8", "moves under you"},
		{"/demo/blocking.html", "text/html; charset=utf-8", "blocks when you interact"},
		{"/demo/demo.css", "text/css; charset=utf-8", "--lcp"},
		{"/healthz", "text/plain; charset=utf-8", "ok"},
		{"/api/summary", "application/json; charset=utf-8", `"metrics"`},
		{"/api/series", "application/json; charset=utf-8", `"buckets"`},
		{"/api/routes", "application/json; charset=utf-8", `"rows"`},
		{"/api/devices", "application/json; charset=utf-8", `"rows"`},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tt.path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}

			body := readBody(t, resp)
			if !strings.Contains(body, tt.contains) {
				t.Errorf("body does not contain %q", tt.contains)
			}
		})
	}
}

// TestDemoPagesLoadTheBeacon is the check that the demo is actually
// instrumented. A demo site that does not load the beacon produces no data and
// makes the whole tool look broken.
func TestDemoPagesLoadTheBeacon(t *testing.T) {
	srv, _ := newServer(t)

	pages := []string{"/demo/", "/demo/heavy.html", "/demo/shifty.html", "/demo/blocking.html"}
	for _, page := range pages {
		resp, err := http.Get(srv.URL + page)
		if err != nil {
			t.Fatalf("GET %s: %v", page, err)
		}
		body := readBody(t, resp)
		resp.Body.Close()

		// Either beacon counts here. Which page carries which is asserted in
		// attribution_test.go; this test only cares that none is uninstrumented.
		if !strings.Contains(body, `src="/b.js"`) && !strings.Contains(body, `src="/b-full.js"`) {
			t.Errorf("%s does not load the beacon", page)
		}
		// Same-origin only. A CDN reference here would defeat the premise.
		for _, bad := range []string{"http://", "https://"} {
			if strings.Contains(body, bad) && !strings.Contains(body, "www.w3.org/2000/svg") {
				t.Errorf("%s references an absolute URL (%s)", page, bad)
			}
		}
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	srv, _ := newServer(t)

	for _, path := range []string{"/nope", "/demo/nope.html", "/api/nope"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestMalformedPayloadStillReturns204(t *testing.T) {
	srv, app := newServer(t)

	for _, body := range []string{"", "not json", `{"u":"/"}`, `{"m":{"lcp":1}}`} {
		resp, err := http.Post(srv.URL+"/v1/collect", "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatalf("posting %q: %v", body, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("payload %q: status = %d, want 204", body, resp.StatusCode)
		}
	}
	if app.Records() != 0 {
		t.Errorf("store holds %d records, want 0", app.Records())
	}
}

func TestMeasurementSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	app, err := server.Open(dir)
	if err != nil {
		t.Fatalf("server.Open: %v", err)
	}
	srv := httptest.NewServer(app.Handler())

	payload := `{"u":"/","m":{"lcp":1500}}`
	resp, err := http.Post(srv.URL+"/v1/collect", "text/plain", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("posting: %v", err)
	}
	resp.Body.Close()

	srv.Close()
	if err := app.Close(); err != nil {
		t.Fatalf("closing server: %v", err)
	}

	// Restart against the same directory, the way the binary would.
	reopened, err := server.Open(dir)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer reopened.Close()

	if reopened.Skipped() != 0 {
		t.Errorf("skipped = %d, want 0", reopened.Skipped())
	}
	if reopened.Records() != 1 {
		t.Errorf("after restart the store holds %d records, want 1", reopened.Records())
	}

	// And the measurement is visible through the API of the restarted server,
	// not merely present in memory.
	restarted := httptest.NewServer(reopened.Handler())
	defer restarted.Close()

	var summary struct {
		Samples int `json:"samples"`
	}
	getJSON(t, restarted.URL+"/api/summary?from=24h", &summary)
	if summary.Samples != 1 {
		t.Errorf("restarted summary reports %d samples, want 1", summary.Samples)
	}
}

func TestBeaconIsUnderBudgetAsServed(t *testing.T) {
	srv, _ := newServer(t)

	resp, err := http.Get(srv.URL + server.BeaconPath)
	if err != nil {
		t.Fatalf("GET %s: %v", server.BeaconPath, err)
	}
	defer resp.Body.Close()

	body := readBody(t, resp)
	if len(body) > server.BeaconMaxBytes {
		t.Errorf("served beacon is %d bytes, over the %d byte budget", len(body), server.BeaconMaxBytes)
	}
	t.Logf("served beacon: %d bytes", len(body))
}

// getJSON fetches a URL and decodes the JSON body into v.
func getJSON(t *testing.T, url string, v any) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decoding %s: %v", url, err)
	}
}

// readBody returns the response body as a string.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	var sb strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}
