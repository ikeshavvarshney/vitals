package dash

import (
	"net/http"
	"testing"
	"time"

	"vitals/src/internal/stats"
	"vitals/src/internal/store"
)

// seedReport fills a store with a mix of good, needs-improvement, and poor LCP
// samples across two routes and two device classes.
func seedReport(t *testing.T) func(*store.Store) {
	t.Helper()

	return func(s *store.Store) {
		// Three good LCPs on the fast route, from a desktop viewport.
		for i := 0; i < 3; i++ {
			add(t, s, refNow.Add(-time.Duration(i+1)*time.Minute), "/fast", 1440,
				map[stats.Metric]float64{stats.LCP: 900, stats.CLS: 0.02})
		}
		// One needs-improvement and one poor LCP on the slow route, from a
		// mobile viewport.
		add(t, s, refNow.Add(-4*time.Minute), "/slow", 390,
			map[stats.Metric]float64{stats.LCP: 3000})
		add(t, s, refNow.Add(-5*time.Minute), "/slow", 390,
			map[stats.Metric]float64{stats.LCP: 9000})
	}
}

// metricOf returns the report entry for m.
func metricOf(t *testing.T, resp Report, m stats.Metric) ReportMetric {
	t.Helper()
	for _, entry := range resp.Metrics {
		if entry.Metric == m {
			return entry
		}
	}
	t.Fatalf("report has no entry for %q", m)
	return ReportMetric{}
}

func TestReportEmptyStore(t *testing.T) {
	a := newTestAPI(t, nil)

	var resp Report
	rec := call(t, a, "/api/report", &resp)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if resp.Samples != 0 {
		t.Errorf("PageViews = %d, want 0", resp.Samples)
	}
	if len(resp.Metrics) != len(stats.Metrics) {
		t.Fatalf("len(Metrics) = %d, want %d", len(resp.Metrics), len(stats.Metrics))
	}
	if len(resp.Caveats) == 0 {
		t.Error("Caveats is empty; a copied report must carry its own disclosures")
	}

	for _, entry := range resp.Metrics {
		if entry.Band != "" {
			t.Errorf("%s: Band = %q, want empty without samples", entry.Metric, entry.Band)
		}
		if len(entry.Quantiles) != 0 {
			t.Errorf("%s: Quantiles = %v, want none without samples", entry.Metric, entry.Quantiles)
		}
		if entry.Min != nil || entry.Max != nil || entry.Mean != nil {
			t.Errorf("%s: min/max/mean reported without samples", entry.Metric)
		}
		if entry.Name == "" {
			t.Errorf("%s: Name is empty", entry.Metric)
		}
	}
}

func TestReportQuantilesAndDistribution(t *testing.T) {
	a := newTestAPI(t, seedReport(t))

	var resp Report
	call(t, a, "/api/report?from=24h", &resp)

	if resp.Samples != 5 {
		t.Errorf("PageViews = %d, want 5", resp.Samples)
	}

	lcp := metricOf(t, resp, stats.LCP)
	if lcp.Samples != 5 {
		t.Fatalf("LCP samples = %d, want 5", lcp.Samples)
	}

	// Band counts are exact, so they can be asserted precisely: 900ms is good,
	// 3000ms needs improvement, 9000ms is poor.
	want := Distribution{Good: 3, NeedsImprovement: 1, Poor: 1}
	if lcp.Distribution != want {
		t.Errorf("LCP Distribution = %+v, want %+v", lcp.Distribution, want)
	}
	if got := lcp.Distribution.Good + lcp.Distribution.NeedsImprovement + lcp.Distribution.Poor; got != lcp.Samples {
		t.Errorf("Distribution sums to %d, want %d", got, lcp.Samples)
	}

	for _, name := range []string{"p50", "p75", "p90", "p95"} {
		if _, ok := lcp.Quantiles[name]; !ok {
			t.Errorf("LCP quantiles missing %q", name)
		}
	}
	// Quantiles must not decrease, whatever the bucketing does to their values.
	if lcp.Quantiles["p50"] > lcp.Quantiles["p75"] || lcp.Quantiles["p75"] > lcp.Quantiles["p95"] {
		t.Errorf("quantiles are not monotonic: %v", lcp.Quantiles)
	}

	if lcp.Min == nil || *lcp.Min != 900 {
		t.Errorf("LCP Min = %v, want 900", lcp.Min)
	}
	if lcp.Max == nil || *lcp.Max != 9000 {
		t.Errorf("LCP Max = %v, want 9000", lcp.Max)
	}
	if lcp.RelativeError <= 0 {
		t.Errorf("LCP RelativeError = %v, want the duration layout's bound", lcp.RelativeError)
	}

	cls := metricOf(t, resp, stats.CLS)
	if cls.AbsoluteError <= 0 {
		t.Errorf("CLS AbsoluteError = %v, want the score layout's bound", cls.AbsoluteError)
	}
	if cls.Samples != 3 {
		t.Errorf("CLS samples = %d, want 3", cls.Samples)
	}

	inp := metricOf(t, resp, stats.INP)
	if inp.Samples != 0 {
		t.Errorf("INP samples = %d, want 0; no record reported one", inp.Samples)
	}
}

func TestReportBreakdownsAreWorstFirst(t *testing.T) {
	a := newTestAPI(t, seedReport(t))

	var resp Report
	call(t, a, "/api/report?from=24h", &resp)

	lcp := metricOf(t, resp, stats.LCP)
	if len(lcp.WorstRoutes) != 2 {
		t.Fatalf("len(WorstRoutes) = %d, want 2", len(lcp.WorstRoutes))
	}
	if lcp.WorstRoutes[0].Key != "/slow" {
		t.Errorf("WorstRoutes[0] = %q, want %q", lcp.WorstRoutes[0].Key, "/slow")
	}
	if lcp.WorstRoutes[0].Value == nil || lcp.WorstRoutes[1].Value == nil {
		t.Fatal("a route with samples reported a null p75")
	}
	if *lcp.WorstRoutes[0].Value < *lcp.WorstRoutes[1].Value {
		t.Errorf("routes are not worst first: %v", lcp.WorstRoutes)
	}

	if len(lcp.WorstDevices) != 2 {
		t.Fatalf("len(WorstDevices) = %d, want 2", len(lcp.WorstDevices))
	}
	if lcp.WorstDevices[0].Key != "mobile" {
		t.Errorf("WorstDevices[0] = %q, want %q", lcp.WorstDevices[0].Key, "mobile")
	}
}

func TestReportBreakdownLimit(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		// One route per sample, each slower than the last, so the cap has to
		// discard the fastest.
		for i := 0; i < breakdownLimit+4; i++ {
			add(t, s, refNow.Add(-time.Duration(i+1)*time.Minute),
				"/r"+string(rune('a'+i)), 1440,
				map[stats.Metric]float64{stats.LCP: float64(500 + i*500)})
		}
	})

	var resp Report
	call(t, a, "/api/report?from=24h", &resp)

	lcp := metricOf(t, resp, stats.LCP)
	if len(lcp.WorstRoutes) != breakdownLimit {
		t.Errorf("len(WorstRoutes) = %d, want %d", len(lcp.WorstRoutes), breakdownLimit)
	}
	if lcp.Samples != uint64(breakdownLimit+4) {
		t.Errorf("Samples = %d, want %d; the cap must not drop samples from the totals",
			lcp.Samples, breakdownLimit+4)
	}
}

func TestReportRejectsBadWindow(t *testing.T) {
	a := newTestAPI(t, nil)

	rec := call(t, a, "/api/report?from=yesterday", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// addFull appends a record shaped like one from the full beacon: with a
// navigation type and an element blamed for each metric.
func addFull(t *testing.T, s *store.Store, at time.Time, route, nav string,
	values map[stats.Metric]float64, attr map[stats.Metric]string) {
	t.Helper()

	err := s.Append(store.Record{
		At:      at,
		Route:   route,
		Session: "sess0001",
		Width:   390,
		Nav:     nav,
		Attr:    attr,
		Values:  values,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// TestReportRanksOffendersByPoorSamples is the behaviour that makes the list
// worth reading: the element named most often is usually the hero image, and it
// only matters when the page views naming it were slow.
func TestReportRanksOffendersByPoorSamples(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		// Named four times, always fast. Common, and not the problem.
		for i := 0; i < 4; i++ {
			addFull(t, s, refNow.Add(-time.Duration(i+1)*time.Minute), "/", "navigate",
				map[stats.Metric]float64{stats.LCP: 800},
				map[stats.Metric]string{stats.LCP: "img.logo"})
		}
		// Named twice, poor both times. Rarer, and the thing to fix.
		for i := 0; i < 2; i++ {
			addFull(t, s, refNow.Add(-time.Duration(i+10)*time.Minute), "/", "navigate",
				map[stats.Metric]float64{stats.LCP: 6000},
				map[stats.Metric]string{stats.LCP: "img.hero"})
		}
	})

	rep, err := a.BuildReport(ReportOptions{Window: time.Hour})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	lcp := metricEntry(t, rep, stats.LCP)
	if len(lcp.Offenders) != 2 {
		t.Fatalf("got %d offenders, want 2: %+v", len(lcp.Offenders), lcp.Offenders)
	}

	top := lcp.Offenders[0]
	if top.Selector != "img.hero" {
		t.Errorf("top offender = %q, want img.hero", top.Selector)
	}
	if top.Samples != 2 || top.Poor != 2 {
		t.Errorf("top offender = %d samples, %d poor; want 2 and 2", top.Samples, top.Poor)
	}
	if second := lcp.Offenders[1]; second.Selector != "img.logo" || second.Poor != 0 {
		t.Errorf("second offender = %+v, want img.logo with 0 poor", second)
	}
}

func TestReportOffendersAreEmptyWithoutAttribution(t *testing.T) {
	a := newTestAPI(t, seedReport(t))

	rep, err := a.BuildReport(ReportOptions{Window: time.Hour})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	for _, m := range rep.Metrics {
		if len(m.Offenders) != 0 {
			t.Errorf("%s has offenders without any attribution stored: %+v", m.Metric, m.Offenders)
		}
	}
	if len(rep.Navigation) != 0 {
		t.Errorf("Navigation = %+v, want empty when no record carries a type", rep.Navigation)
	}
}

func TestReportOffendersAreCapped(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		for i := 0; i < breakdownLimit+4; i++ {
			addFull(t, s, refNow.Add(-time.Duration(i+1)*time.Minute), "/", "navigate",
				map[stats.Metric]float64{stats.LCP: 6000},
				map[stats.Metric]string{stats.LCP: "div#slot-" + string(rune('a'+i))})
		}
	})

	rep, err := a.BuildReport(ReportOptions{Window: time.Hour})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	if got := len(metricEntry(t, rep, stats.LCP).Offenders); got != breakdownLimit {
		t.Errorf("got %d offenders, want the cap of %d", got, breakdownLimit)
	}
}

func TestReportCountsNavigationTypes(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		kinds := []string{"navigate", "navigate", "navigate", "soft-navigation", "soft-navigation", "back-forward-cache"}
		for i, kind := range kinds {
			addFull(t, s, refNow.Add(-time.Duration(i+1)*time.Minute), "/", kind,
				map[stats.Metric]float64{stats.LCP: 1000}, nil)
		}
	})

	rep, err := a.BuildReport(ReportOptions{Window: time.Hour})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	want := []NavigationCount{
		{Type: "navigate", Samples: 3},
		{Type: "soft-navigation", Samples: 2},
		{Type: "back-forward-cache", Samples: 1},
	}
	if len(rep.Navigation) != len(want) {
		t.Fatalf("got %+v, want %+v", rep.Navigation, want)
	}
	for i, w := range want {
		if rep.Navigation[i] != w {
			t.Errorf("Navigation[%d] = %+v, want %+v", i, rep.Navigation[i], w)
		}
	}
}

// TestReportOffendersCountOnlyTheirOwnMetric guards the obvious aggregation
// mistake: blaming the LCP element for a bad CLS because both arrived on the
// same record.
func TestReportOffendersCountOnlyTheirOwnMetric(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		addFull(t, s, refNow.Add(-time.Minute), "/", "navigate",
			map[stats.Metric]float64{stats.LCP: 6000, stats.CLS: 0.4},
			map[stats.Metric]string{stats.LCP: "img.hero", stats.CLS: "div#banner"})
	})

	rep, err := a.BuildReport(ReportOptions{Window: time.Hour})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	for metric, want := range map[stats.Metric]string{stats.LCP: "img.hero", stats.CLS: "div#banner"} {
		got := metricEntry(t, rep, metric).Offenders
		if len(got) != 1 {
			t.Fatalf("%s: got %d offenders, want 1: %+v", metric, len(got), got)
		}
		if got[0].Selector != want {
			t.Errorf("%s: offender = %q, want %q", metric, got[0].Selector, want)
		}
	}
	if got := metricEntry(t, rep, stats.INP).Offenders; len(got) != 0 {
		t.Errorf("INP picked up an offender it was never given: %+v", got)
	}
}

// metricEntry returns one metric's entry from a report.
func metricEntry(t *testing.T, rep Report, m stats.Metric) ReportMetric {
	t.Helper()

	for _, entry := range rep.Metrics {
		if entry.Metric == m {
			return entry
		}
	}
	t.Fatalf("report has no entry for %s", m)
	return ReportMetric{}
}
