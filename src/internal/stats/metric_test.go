package stats

import "testing"

func TestBandOf(t *testing.T) {
	tests := []struct {
		name   string
		metric Metric
		value  float64
		want   Band
	}{
		// LCP: 2500 / 4000
		{"lcp fast", LCP, 1200, Good},
		{"lcp exactly at good boundary", LCP, 2500, Good},
		{"lcp just past good", LCP, 2500.1, NeedsImprovement},
		{"lcp exactly at needs-improvement boundary", LCP, 4000, NeedsImprovement},
		{"lcp just past needs-improvement", LCP, 4000.1, Poor},
		{"lcp very slow", LCP, 15000, Poor},

		// INP: 200 / 500
		{"inp fast", INP, 90, Good},
		{"inp at good boundary", INP, 200, Good},
		{"inp needs improvement", INP, 350, NeedsImprovement},
		{"inp at needs-improvement boundary", INP, 500, NeedsImprovement},
		{"inp poor", INP, 501, Poor},

		// CLS: 0.1 / 0.25
		{"cls none", CLS, 0, Good},
		{"cls at good boundary", CLS, 0.1, Good},
		{"cls needs improvement", CLS, 0.18, NeedsImprovement},
		{"cls at needs-improvement boundary", CLS, 0.25, NeedsImprovement},
		{"cls poor", CLS, 0.4, Poor},

		// FCP: 1800 / 3000
		{"fcp good", FCP, 900, Good},
		{"fcp at good boundary", FCP, 1800, Good},
		{"fcp needs improvement", FCP, 2400, NeedsImprovement},
		{"fcp poor", FCP, 3200, Poor},

		// TTFB: 800 / 1800
		{"ttfb good", TTFB, 210, Good},
		{"ttfb at good boundary", TTFB, 800, Good},
		{"ttfb needs improvement", TTFB, 1200, NeedsImprovement},
		{"ttfb poor", TTFB, 2000, Poor},

		// An unrecognised metric is never rated worse than good, because a
		// fabricated poor rating would be a number nobody measured.
		{"unknown metric", Metric("bogus"), 999999, Good},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BandOf(tt.metric, tt.value); got != tt.want {
				t.Errorf("BandOf(%s, %v) = %v, want %v", tt.metric, tt.value, got, tt.want)
			}
		})
	}
}

func TestBandString(t *testing.T) {
	tests := []struct {
		band Band
		want string
	}{
		{Good, "good"},
		{NeedsImprovement, "needs-improvement"},
		{Poor, "poor"},
		{Band(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.band.String(); got != tt.want {
			t.Errorf("Band(%d).String() = %q, want %q", tt.band, got, tt.want)
		}
	}
}

func TestThresholds(t *testing.T) {
	tests := []struct {
		metric Metric
		good   float64
		needs  float64
		wantOK bool
	}{
		{LCP, 2500, 4000, true},
		{INP, 200, 500, true},
		{CLS, 0.1, 0.25, true},
		{FCP, 1800, 3000, true},
		{TTFB, 800, 1800, true},
		{Metric("bogus"), 0, 0, false},
	}

	for _, tt := range tests {
		good, needs, ok := Thresholds(tt.metric)
		if ok != tt.wantOK {
			t.Errorf("Thresholds(%s) ok = %v, want %v", tt.metric, ok, tt.wantOK)
			continue
		}
		if good != tt.good || needs != tt.needs {
			t.Errorf("Thresholds(%s) = (%v, %v), want (%v, %v)", tt.metric, good, needs, tt.good, tt.needs)
		}
	}
}

func TestLayoutOf(t *testing.T) {
	tests := []struct {
		metric Metric
		want   Layout
	}{
		{LCP, LayoutDuration},
		{INP, LayoutDuration},
		{FCP, LayoutDuration},
		{TTFB, LayoutDuration},
		{CLS, LayoutScore},
	}
	for _, tt := range tests {
		if got := LayoutOf(tt.metric); got != tt.want {
			t.Errorf("LayoutOf(%s) = %v, want %v", tt.metric, got, tt.want)
		}
	}
}

func TestValid(t *testing.T) {
	for _, m := range Metrics {
		if !Valid(m) {
			t.Errorf("Valid(%s) = false, want true", m)
		}
	}
	for _, m := range []Metric{"", "LCP", "fid", "bogus"} {
		if Valid(m) {
			t.Errorf("Valid(%q) = true, want false", m)
		}
	}
}

func TestMetricsCoversEveryThreshold(t *testing.T) {
	// Metrics drives the dashboard; cwvThresholds drives banding. If one gains
	// an entry the other must too, or a metric silently stops being rated.
	if len(Metrics) != len(cwvThresholds) {
		t.Fatalf("Metrics has %d entries, cwvThresholds has %d", len(Metrics), len(cwvThresholds))
	}
	for _, m := range Metrics {
		if _, ok := cwvThresholds[m]; !ok {
			t.Errorf("metric %s has no threshold", m)
		}
	}
}
