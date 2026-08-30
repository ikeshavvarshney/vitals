package dash

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vitals/internal/ingest"
	"vitals/internal/stats"
	"vitals/internal/store"
)

// newTestAPI returns an API over a store seeded by seed, with a fixed clock.
func newTestAPI(t *testing.T, seed func(*store.Store)) *API {
	t.Helper()

	s, _, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if seed != nil {
		seed(s)
	}

	a := NewAPI(s, func() ingest.Counters {
		return ingest.Counters{Accepted: 42, Malformed: 3}
	})
	a.now = func() time.Time { return refNow }
	return a
}

// add appends one record.
func add(t *testing.T, s *store.Store, at time.Time, route string, width int, values map[stats.Metric]float64) {
	t.Helper()
	err := s.Append(store.Record{
		At:      at,
		Route:   route,
		Session: "sess0001",
		Width:   width,
		Values:  values,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// call issues a GET and decodes the JSON body into v.
func call(t *testing.T, a *API, target string, v any) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	a.Register(mux)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if v != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("decoding %s: %v\nbody: %s", target, err, rec.Body.String())
		}
	}
	return rec
}

func TestSummaryEmptyStore(t *testing.T) {
	a := newTestAPI(t, nil)

	var resp summaryResponse
	rec := call(t, a, "/api/summary", &resp)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if resp.Samples != 0 {
		t.Errorf("Samples = %d, want 0", resp.Samples)
	}
	if len(resp.Metrics) != len(stats.Metrics) {
		t.Fatalf("got %d metrics, want %d", len(resp.Metrics), len(stats.Metrics))
	}
	for _, m := range resp.Metrics {
		if m.P75 != nil {
			t.Errorf("%s: P75 = %v, want null on an empty store", m.Metric, *m.P75)
		}
		if m.Band != "" {
			t.Errorf("%s: Band = %q, want empty when there is no value", m.Metric, m.Band)
		}
	}
	if resp.Coverage == nil || resp.Coverage.Total != 0 {
		t.Error("coverage should report an empty store")
	}
}

func TestSummary(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		// 100 samples, LCP 1..100 seconds worth of milliseconds so p75 is
		// predictable, plus a CLS value on each.
		for i := 1; i <= 100; i++ {
			add(t, s, refNow.Add(-time.Duration(i)*time.Minute), "/", 1440, map[stats.Metric]float64{
				stats.LCP: float64(i) * 30,
				stats.CLS: 0.05,
			})
		}
	})

	var resp summaryResponse
	call(t, a, "/api/summary?from=24h&to="+refNow.Format(time.RFC3339), &resp)

	if resp.Samples != 100 {
		t.Errorf("Samples = %d, want 100", resp.Samples)
	}

	byMetric := map[stats.Metric]metricSummary{}
	for _, m := range resp.Metrics {
		byMetric[m.Metric] = m
	}

	lcp := byMetric[stats.LCP]
	if lcp.P75 == nil {
		t.Fatal("lcp p75 is null")
	}
	// Exact p75 of 30..3000 in steps of 30 is 2250. Allow the bucket error.
	if got := *lcp.P75; got < 2250*0.95 || got > 2250*1.05 {
		t.Errorf("lcp p75 = %v, want about 2250", got)
	}
	if lcp.Band != "good" {
		t.Errorf("lcp band = %q, want good (2250 is under the 2500 threshold)", lcp.Band)
	}
	if lcp.Unit != "ms" {
		t.Errorf("lcp unit = %q, want ms", lcp.Unit)
	}
	if lcp.Good != 2500 || lcp.NeedsImprovement != 4000 {
		t.Errorf("lcp thresholds = %v/%v, want 2500/4000", lcp.Good, lcp.NeedsImprovement)
	}
	if lcp.Samples != 100 {
		t.Errorf("lcp samples = %d, want 100", lcp.Samples)
	}

	cls := byMetric[stats.CLS]
	if cls.Unit != "" {
		t.Errorf("cls unit = %q, want empty; CLS is unitless", cls.Unit)
	}
	if cls.Band != "good" {
		t.Errorf("cls band = %q, want good", cls.Band)
	}

	// A metric nobody reported has no value, rather than a zero that would
	// render as a perfect score.
	inp := byMetric[stats.INP]
	if inp.P75 != nil {
		t.Errorf("inp p75 = %v, want null; no samples were recorded", *inp.P75)
	}
}

func TestSummaryIncludesIngestCounters(t *testing.T) {
	a := newTestAPI(t, nil)

	var resp summaryResponse
	call(t, a, "/api/summary", &resp)

	if resp.Ingest.Accepted != 42 || resp.Ingest.Malformed != 3 {
		t.Errorf("Ingest = %+v, want accepted 42 and malformed 3", resp.Ingest)
	}
}

func TestSummaryRespectsWindow(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		add(t, s, refNow.Add(-1*time.Hour), "/", 1440, map[stats.Metric]float64{stats.LCP: 1000})
		add(t, s, refNow.Add(-48*time.Hour), "/", 1440, map[stats.Metric]float64{stats.LCP: 9000})
	})

	var resp summaryResponse
	call(t, a, "/api/summary", &resp) // default 24h window

	if resp.Samples != 1 {
		t.Errorf("Samples = %d, want 1; the 48h-old record is outside the default window", resp.Samples)
	}
	// The whole store is still reported in coverage, so the dashboard can say
	// there is older data.
	if resp.Coverage.Total != 2 {
		t.Errorf("Coverage.Total = %d, want 2", resp.Coverage.Total)
	}
}

func TestSeries(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		// One sample every 10 minutes for 2 hours.
		for i := 0; i < 12; i++ {
			add(t, s, refNow.Add(-2*time.Hour).Add(time.Duration(i)*10*time.Minute),
				"/", 1440, map[stats.Metric]float64{stats.LCP: 1000})
		}
	})

	var resp seriesResponse
	rec := call(t, a, "/api/series?metric=lcp&from=2h&to="+refNow.Format(time.RFC3339)+"&n=12", &resp)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if resp.Metric != stats.LCP {
		t.Errorf("Metric = %q, want lcp", resp.Metric)
	}
	if len(resp.Buckets) != 12 {
		t.Fatalf("got %d buckets, want 12", len(resp.Buckets))
	}
	if resp.BucketSeconds != 600 {
		t.Errorf("BucketSeconds = %v, want 600", resp.BucketSeconds)
	}

	var withData int
	for _, b := range resp.Buckets {
		if b.Samples > 0 {
			withData++
		}
	}
	if withData == 0 {
		t.Error("no bucket contains any sample")
	}

	// Buckets must be in ascending time order and evenly spaced.
	for i := 1; i < len(resp.Buckets); i++ {
		if !resp.Buckets[i].At.After(resp.Buckets[i-1].At) {
			t.Fatalf("bucket %d is not after bucket %d", i, i-1)
		}
	}
}

func TestSeriesEmptyBucketsAreNull(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		add(t, s, refNow.Add(-30*time.Minute), "/", 1440, map[stats.Metric]float64{stats.LCP: 1000})
	})

	var resp seriesResponse
	call(t, a, "/api/series?metric=lcp&n=24", &resp)

	var nulls int
	for _, b := range resp.Buckets {
		if b.P75 == nil {
			nulls++
			if b.Samples != 0 {
				t.Errorf("bucket with null p75 reports %d samples", b.Samples)
			}
		}
	}
	if nulls == 0 {
		t.Error("no empty buckets reported as null; a gap must not render as zero")
	}
}

func TestSeriesDefaultsToLCP(t *testing.T) {
	a := newTestAPI(t, nil)

	var resp seriesResponse
	call(t, a, "/api/series", &resp)

	if resp.Metric != stats.LCP {
		t.Errorf("Metric = %q, want lcp by default", resp.Metric)
	}
	if len(resp.Buckets) != defaultBuckets {
		t.Errorf("got %d buckets, want the default %d", len(resp.Buckets), defaultBuckets)
	}
}

func TestRoutes(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		for i := 0; i < 5; i++ {
			at := refNow.Add(-time.Duration(i+1) * time.Minute)
			add(t, s, at, "/fast", 1440, map[stats.Metric]float64{stats.LCP: 800})
			add(t, s, at, "/slow", 1440, map[stats.Metric]float64{stats.LCP: 6000})
			add(t, s, at, "/medium", 1440, map[stats.Metric]float64{stats.LCP: 3000})
		}
	})

	var resp groupResponse
	call(t, a, "/api/routes?metric=lcp", &resp)

	if len(resp.Rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(resp.Rows), resp.Rows)
	}

	// Worst first.
	if resp.Rows[0].Key != "/slow" {
		t.Errorf("first row = %q, want /slow", resp.Rows[0].Key)
	}
	if resp.Rows[2].Key != "/fast" {
		t.Errorf("last row = %q, want /fast", resp.Rows[2].Key)
	}

	bands := map[string]string{"/fast": "good", "/medium": "needs-improvement", "/slow": "poor"}
	for _, row := range resp.Rows {
		if want := bands[row.Key]; row.Band != want {
			t.Errorf("%s band = %q, want %q", row.Key, row.Band, want)
		}
		if row.Samples != 5 {
			t.Errorf("%s samples = %d, want 5", row.Key, row.Samples)
		}
	}
}

func TestDevices(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		widths := map[string]int{"mobile": 390, "tablet": 820, "desktop": 1680}
		for name, w := range widths {
			_ = name
			for i := 0; i < 3; i++ {
				add(t, s, refNow.Add(-time.Duration(i+1)*time.Minute), "/", w,
					map[stats.Metric]float64{stats.LCP: 1000})
			}
		}
	})

	var resp groupResponse
	call(t, a, "/api/devices?metric=lcp", &resp)

	got := map[string]bool{}
	for _, row := range resp.Rows {
		got[row.Key] = true
		if row.Samples != 3 {
			t.Errorf("%s samples = %d, want 3", row.Key, row.Samples)
		}
	}
	for _, want := range []string{"mobile", "tablet", "desktop"} {
		if !got[want] {
			t.Errorf("no row for %s: %+v", want, resp.Rows)
		}
	}
}

func TestBadRequests(t *testing.T) {
	a := newTestAPI(t, nil)

	targets := []string{
		"/api/summary?from=nonsense",
		"/api/summary?to=nonsense",
		"/api/series?metric=fid",
		"/api/series?n=0",
		"/api/series?n=99999",
		"/api/series?n=abc",
		"/api/routes?metric=bogus",
		"/api/devices?metric=bogus",
	}

	for _, target := range targets {
		rec := call(t, a, target, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: error body is not JSON: %v", target, err)
			continue
		}
		if body["error"] == "" {
			t.Errorf("%s: error body has no message", target)
		}
	}
}

func TestSeriesRejectsInvertedRange(t *testing.T) {
	a := newTestAPI(t, nil)

	from := refNow.Format(time.RFC3339)
	to := refNow.Add(-time.Hour).Format(time.RFC3339)
	rec := call(t, a, "/api/series?from="+from+"&to="+to, nil)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an inverted range", rec.Code)
	}
}

func TestResponsesAreNotCached(t *testing.T) {
	a := newTestAPI(t, nil)

	for _, target := range []string{"/api/summary", "/api/series", "/api/routes", "/api/devices"} {
		rec := call(t, a, target, nil)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", target, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q", target, got)
		}
	}
}

func TestNonGetIsRejected(t *testing.T) {
	a := newTestAPI(t, nil)

	mux := http.NewServeMux()
	a.Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/summary", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
