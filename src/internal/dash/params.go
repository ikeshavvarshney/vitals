// Package dash serves the dashboard: a small JSON API over the measurement
// store, and the static assets that render it.
package dash

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"vitals/src/internal/stats"
	"vitals/src/internal/store"
)

// Series bucket limits. The cap stops one query from allocating unboundedly.
const (
	defaultBuckets = 48
	maxBuckets     = 720
)

// defaultWindow is the range used when the request names neither end.
const defaultWindow = 24 * time.Hour

// selectablePercentiles are the quantiles a request may ask for. Core Web
// Vitals is assessed at the 75th, which stays the default; the others are for
// looking at the tail, which is where a p75 that looks fine can still hide a
// slow slice of visits. The set is closed because a histogram cannot answer a
// quantile more finely than its buckets, and an arbitrary one invites reading
// precision into the answer that is not there.
var selectablePercentiles = []int{50, 75, 90, 95}

// query is a parsed and validated set of request parameters.
type query struct {
	Range   store.Range
	Metric  stats.Metric
	Buckets int
	// Percentile is the quantile to report, in (0, 1).
	Percentile float64
	// Route, when set, restricts every figure to one route.
	Route string
}

// parseQuery reads the parameters shared by every endpoint. Anything
// unparseable is an error rather than a silent default: a dashboard showing the
// wrong window without saying so is worse than one showing an error.
func parseQuery(v url.Values, now time.Time) (query, error) {
	q := query{Buckets: defaultBuckets, Percentile: percentile}

	from, err := parseTime(v.Get("from"), now)
	if err != nil {
		return query{}, fmt.Errorf("from: %w", err)
	}
	to, err := parseTime(v.Get("to"), now)
	if err != nil {
		return query{}, fmt.Errorf("to: %w", err)
	}

	// Both ends are always resolved to concrete times.
	//
	// Leaving an end open here used to reach store.Range.Normalize, which
	// substitutes the year 9999. Subtracting from that saturates time.Duration,
	// which is an int64 of nanoseconds and tops out around 292 years, so a
	// series over "the last 24 hours" was silently bucketed into six-year
	// intervals. An unbounded range is meaningful to the store; it is not
	// meaningful to a chart with a fixed number of buckets.
	if to.IsZero() {
		// The store's window is half-open, [from, to), so a window ending
		// exactly at now excludes a record stamped in the instant the request
		// is served. That is not hypothetical: on a coarse system clock the
		// beacon and the request that reads it land on the same value, and the
		// measurement vanishes from the view that should show it. Records are
		// stored at millisecond resolution, so ending one millisecond later
		// includes the present instant and nothing that has not happened.
		to = now.Add(time.Millisecond)
	}
	if from.IsZero() {
		from = to.Add(-defaultWindow)
	}
	q.Range = store.Range{From: from, To: to}

	if s := v.Get("metric"); s != "" {
		m := stats.Metric(strings.ToLower(s))
		if !stats.Valid(m) {
			return query{}, fmt.Errorf("metric: unknown metric %q", s)
		}
		q.Metric = m
	}

	if s := v.Get("p"); s != "" {
		p, err := strconv.Atoi(s)
		if err != nil {
			return query{}, fmt.Errorf("p: %w", err)
		}
		if !allowedPercentile(p) {
			return query{}, fmt.Errorf("p: %d is not one of %v", p, selectablePercentiles)
		}
		q.Percentile = float64(p) / 100
	}

	// A route filter is matched exactly. Substring or prefix matching would
	// silently mix two pages into one figure.
	q.Route = strings.TrimSpace(v.Get("route"))

	if s := v.Get("n"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return query{}, fmt.Errorf("n: %w", err)
		}
		if n < 1 || n > maxBuckets {
			return query{}, fmt.Errorf("n: %d is outside 1..%d", n, maxBuckets)
		}
		q.Buckets = n
	}

	return q, nil
}

// minEpochMillis, 2001-09-09, is the earliest accepted epoch timestamp.
// Anything smaller is far more likely a duration missing its unit.
const minEpochMillis = 1_000_000_000_000

// parseTime accepts epoch milliseconds, RFC 3339, or a relative duration such as
// "24h" or "-24h", which both mean that far in the past. An empty string yields
// the zero time.
//
// A bare number is never read as a duration, and one below [minEpochMillis] is
// rejected. Both rules turn an ambiguous parameter into a visible error rather
// than a quietly wrong window.
func parseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}

	// First, because an RFC 3339 timestamp in zone "Z" also ends in a letter.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}

	if endsWithLetter(s) { // a duration must carry a unit
		d, err := time.ParseDuration(s)
		if err != nil {
			return time.Time{}, fmt.Errorf("%q is not a valid duration: %w", s, err)
		}
		if d > 0 {
			d = -d
		}
		return now.Add(d), nil
	}

	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		if ms < minEpochMillis {
			return time.Time{}, fmt.Errorf(
				"%q is too small for an epoch millisecond timestamp; durations need a unit, such as 24h", s)
		}
		return time.UnixMilli(ms).UTC(), nil
	}

	return time.Time{}, fmt.Errorf("%q is not epoch milliseconds, RFC 3339, or a duration", s)
}

// endsWithLetter tells a duration apart from a bare number.
func endsWithLetter(s string) bool {
	if s == "" {
		return false
	}
	c := s[len(s)-1]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// allowedPercentile reports whether p is one of the selectable quantiles.
func allowedPercentile(p int) bool {
	for _, ok := range selectablePercentiles {
		if p == ok {
			return true
		}
	}
	return false
}

// requireMetric returns the query's metric, defaulting to LCP.
func (q query) requireMetric() stats.Metric {
	if q.Metric == "" {
		return stats.LCP
	}
	return q.Metric
}
