package stats

import "errors"

// errLayoutMismatch is returned when histograms of different layouts are merged.
var errLayoutMismatch = errors.New("stats: cannot merge histograms with different layouts")

// Metric identifies one of the five collected Core Web Vitals. The string
// values are the short keys used in the beacon payload and the JSON API.
type Metric string

// The five metrics this tool collects.
const (
	LCP  Metric = "lcp"  // Largest Contentful Paint, milliseconds
	CLS  Metric = "cls"  // Cumulative Layout Shift, unitless
	INP  Metric = "inp"  // Interaction to Next Paint, milliseconds (approximated)
	TTFB Metric = "ttfb" // Time to First Byte, milliseconds
	FCP  Metric = "fcp"  // First Contentful Paint, milliseconds
)

// Metrics lists every collected metric in the order the dashboard shows them.
var Metrics = []Metric{LCP, INP, CLS, FCP, TTFB}

// Band is the good / needs improvement / poor rating of a metric value.
type Band int

// The three Core Web Vitals ratings.
const (
	Good Band = iota
	NeedsImprovement
	Poor
)

// String returns the band's identifier as used in the JSON API and as a CSS
// class name on the dashboard.
func (b Band) String() string {
	switch b {
	case Good:
		return "good"
	case NeedsImprovement:
		return "needs-improvement"
	case Poor:
		return "poor"
	default:
		return "unknown"
	}
}

// thresholds holds the upper bound of the good band and the upper bound of the
// needs-improvement band for one metric. A value at or below Good is good; a
// value at or below NeedsImprovement is needs-improvement; anything higher is
// poor.
type thresholds struct {
	Good             float64
	NeedsImprovement float64
}

// cwvThresholds are the published Core Web Vitals thresholds.
//
// Source: web.dev/articles/vitals, "Core Web Vitals metrics and thresholds",
// which defines LCP at 2500/4000ms, INP at 200/500ms, and CLS at 0.1/0.25. FCP
// (1800/3000ms) and TTFB (800/1800ms) are Google's supplementary diagnostic
// thresholds from web.dev/articles/fcp and web.dev/articles/ttfb; they are not
// Core Web Vitals themselves but are banded the same way here.
var cwvThresholds = map[Metric]thresholds{
	LCP:  {Good: 2500, NeedsImprovement: 4000},
	INP:  {Good: 200, NeedsImprovement: 500},
	CLS:  {Good: 0.1, NeedsImprovement: 0.25},
	FCP:  {Good: 1800, NeedsImprovement: 3000},
	TTFB: {Good: 800, NeedsImprovement: 1800},
}

// BandOf rates a value against the published threshold for its metric. An
// unknown metric is rated Good, because inventing a poor rating for a metric we
// have no thresholds for would be a fabricated number on the dashboard.
func BandOf(m Metric, value float64) Band {
	t, ok := cwvThresholds[m]
	if !ok {
		return Good
	}
	switch {
	case value <= t.Good:
		return Good
	case value <= t.NeedsImprovement:
		return NeedsImprovement
	default:
		return Poor
	}
}

// Thresholds returns the good and needs-improvement upper bounds for a metric,
// so the dashboard can draw threshold lines without duplicating the constants.
// ok is false for an unknown metric.
func Thresholds(m Metric) (good, needsImprovement float64, ok bool) {
	t, found := cwvThresholds[m]
	if !found {
		return 0, 0, false
	}
	return t.Good, t.NeedsImprovement, true
}

// LayoutOf returns the histogram layout appropriate to a metric. CLS is
// unitless and small, so it uses the linear score layout; everything else is a
// duration in milliseconds.
func LayoutOf(m Metric) Layout {
	if m == CLS {
		return LayoutScore
	}
	return LayoutDuration
}

// Valid reports whether m is one of the five collected metrics.
func Valid(m Metric) bool {
	_, ok := cwvThresholds[m]
	return ok
}
