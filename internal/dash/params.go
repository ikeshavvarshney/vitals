// Package dash serves the dashboard: a small JSON API over the measurement
// store, and the static assets that render it.
package dash

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"vitals/internal/stats"
	"vitals/internal/store"
)

// Series bucket limits. The cap stops one query from allocating unboundedly.
const (
	defaultBuckets = 48
	maxBuckets     = 720
)

// defaultWindow is the range used when the request names neither end.
const defaultWindow = 24 * time.Hour

// query is a parsed and validated set of request parameters.
type query struct {
	Range   store.Range
	Metric  stats.Metric
	Buckets int
}

// parseQuery reads the parameters shared by every endpoint. Anything
// unparseable is an error rather than a silent default: a dashboard showing the
// wrong window without saying so is worse than one showing an error.
func parseQuery(v url.Values, now time.Time) (query, error) {
	q := query{Buckets: defaultBuckets}

	from, err := parseTime(v.Get("from"), now)
	if err != nil {
		return query{}, fmt.Errorf("from: %w", err)
	}
	to, err := parseTime(v.Get("to"), now)
	if err != nil {
		return query{}, fmt.Errorf("to: %w", err)
	}

	// Neither end given: show the most recent window, not all of history.
	if from.IsZero() && to.IsZero() {
		to = now
		from = now.Add(-defaultWindow)
	}
	q.Range = store.Range{From: from, To: to}

	if s := v.Get("metric"); s != "" {
		m := stats.Metric(strings.ToLower(s))
		if !stats.Valid(m) {
			return query{}, fmt.Errorf("metric: unknown metric %q", s)
		}
		q.Metric = m
	}

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

// requireMetric returns the query's metric, defaulting to LCP.
func (q query) requireMetric() stats.Metric {
	if q.Metric == "" {
		return stats.LCP
	}
	return q.Metric
}
