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

// Series bucket limits. A caller asking for one bucket per pixel is reasonable;
// a caller asking for a million is not, and the cap is what stops one query
// from allocating unboundedly.
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

// parseQuery reads the parameters shared by every endpoint.
//
// Time may be given as epoch milliseconds, as RFC 3339, or as a relative
// duration like "24h", which is resolved against now. Anything unparseable is
// an error rather than a silent default: a dashboard showing the wrong window
// without saying so is worse than one showing an error.
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

	// With neither end given, show the most recent window rather than all of
	// history, which is what someone opening the dashboard actually wants.
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

// minEpochMillis is the earliest timestamp accepted as epoch milliseconds,
// 2001-09-09. Anything smaller is far more likely to be a duration written
// without its unit than a genuine timestamp from the 1970s, and guessing wrong
// silently would put the window three decades in the past.
const minEpochMillis = 1_000_000_000_000

// parseTime accepts epoch milliseconds, RFC 3339, or a relative duration such
// as "24h" or "-24h", both of which mean the same thing: that far in the past.
// An empty string yields the zero time, meaning the caller did not specify one.
//
// A bare number without a unit is never read as a duration, and a number below
// [minEpochMillis] is rejected rather than interpreted. Both rules exist so that
// an ambiguous parameter produces an error the caller can see instead of a
// window that is quietly wrong.
func parseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}

	// RFC 3339 is tried first because it also ends in a letter when the zone is
	// "Z", which would otherwise be taken for a duration unit.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}

	// A duration must carry a unit, so "24h" is not mistaken for a timestamp
	// and "24" is not mistaken for a duration.
	if endsWithLetter(s) {
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

// endsWithLetter reports whether s ends in an ASCII letter, which is how a
// duration is told apart from a bare number.
func endsWithLetter(s string) bool {
	if s == "" {
		return false
	}
	c := s[len(s)-1]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// requireMetric returns the query's metric, defaulting to LCP when none was
// given. LCP is the default because it is the metric most sites are worst at.
func (q query) requireMetric() stats.Metric {
	if q.Metric == "" {
		return stats.LCP
	}
	return q.Metric
}
