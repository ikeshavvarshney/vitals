package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vitals/internal/beacon"
	"vitals/internal/store"
)

// newServer returns a live test server running the real route table over a
// temporary data directory.
func newServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()

	db, _, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	handler, err := routes(db)
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, db
}

// TestEndToEnd is the test that proves the product works: a beacon payload is
// posted the way a browser posts it, and the number comes back out of the API.
func TestEndToEnd(t *testing.T) {
	srv, db := newServer(t)

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
	if db.Count() != 1 {
		t.Fatalf("store holds %d records, want 1", db.Count())
	}

	// The measurement must now be visible through the dashboard's own API.
	var summary struct {
		Samples int `json:"samples"`
		Metrics []struct {
			Metric  string   `json:"metric"`
			P75     *float64 `json:"p75"`
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
		if m.P75 == nil {
			t.Fatal("lcp p75 is null after ingesting a measurement")
		}
		// 1834.2 lands within one bucket of itself.
		if *m.P75 < 1834.2*0.95 || *m.P75 > 1834.2*1.05 {
			t.Errorf("lcp p75 = %v, want about 1834.2", *m.P75)
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
		{beacon.Path, "text/javascript; charset=utf-8", "PerformanceObserver"},
		{"/beacon.src.js", "text/javascript; charset=utf-8", "vitals beacon"},
		{"/demo/", "text/html; charset=utf-8", "A fast page"},
		{"/demo/heavy.html", "text/html; charset=utf-8", "heavy hero image"},
		{"/demo/shifty.html", "text/html; charset=utf-8", "moves under you"},
		{"/demo/blocking.html", "text/html; charset=utf-8", "blocks when you interact"},
		{"/demo/demo.css", "text/css; charset=utf-8", "--accent"},
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

		if !strings.Contains(body, `src="/b.js"`) {
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
	srv, db := newServer(t)

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
	if db.Count() != 0 {
		t.Errorf("store holds %d records, want 0", db.Count())
	}
}

func TestMeasurementSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	db, _, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	handler, err := routes(db)
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	srv := httptest.NewServer(handler)

	payload := `{"u":"/","m":{"lcp":1500}}`
	resp, err := http.Post(srv.URL+"/v1/collect", "text/plain", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("posting: %v", err)
	}
	resp.Body.Close()

	srv.Close()
	if err := db.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}

	// Restart against the same directory.
	reopened, skipped, err := store.Open(dir)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer reopened.Close()

	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if reopened.Count() != 1 {
		t.Errorf("after restart the store holds %d records, want 1", reopened.Count())
	}
}

func TestBeaconIsUnderBudgetAsServed(t *testing.T) {
	srv, _ := newServer(t)

	resp, err := http.Get(srv.URL + beacon.Path)
	if err != nil {
		t.Fatalf("GET %s: %v", beacon.Path, err)
	}
	defer resp.Body.Close()

	body := readBody(t, resp)
	if len(body) > beacon.MaxBytes {
		t.Errorf("served beacon is %d bytes, over the %d byte budget", len(body), beacon.MaxBytes)
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
