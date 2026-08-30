package dash

import (
	"net/url"
	"testing"
	"time"

	"vitals/src/internal/stats"
)

var refNow = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

func TestParseTime(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Time
		wantErr bool
	}{
		{"empty is the zero time", "", time.Time{}, false},
		{"whitespace is the zero time", "   ", time.Time{}, false},
		{"epoch milliseconds", "1756555200000", time.UnixMilli(1756555200000).UTC(), false},
		{"rfc 3339", "2026-08-30T06:00:00Z", time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC), false},
		{"rfc 3339 with offset", "2026-08-30T06:00:00+02:00", time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC), false},
		{"positive duration means the past", "24h", refNow.Add(-24 * time.Hour), false},
		{"negative duration means the past too", "-24h", refNow.Add(-24 * time.Hour), false},
		{"minutes", "90m", refNow.Add(-90 * time.Minute), false},
		{"zero epoch is rejected", "0", time.Time{}, true},
		{"negative epoch is rejected", "-1000", time.Time{}, true},
		{"nonsense", "yesterday", time.Time{}, true},
		{"partial date", "2026-08-30", time.Time{}, true},
		{"empty unit", "24", time.Time{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTime(tt.in, refNow)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTime(%q) = %v, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTime(%q): %v", tt.in, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseTime(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseTimeRejectsBareInteger(t *testing.T) {
	// "24" is ambiguous between 24 milliseconds since the epoch and 24 of some
	// unit. It parses as epoch milliseconds, which is a timestamp in 1970 and
	// almost certainly not what the caller meant, so it must be rejected as
	// implausible rather than silently accepted.
	got, err := parseTime("24", refNow)
	if err == nil {
		t.Errorf("parseTime(\"24\") = %v, want an error", got)
	}
}

func TestParseQueryDefaults(t *testing.T) {
	q, err := parseQuery(url.Values{}, refNow)
	if err != nil {
		t.Fatalf("parseQuery: %v", err)
	}

	if q.Buckets != defaultBuckets {
		t.Errorf("Buckets = %d, want %d", q.Buckets, defaultBuckets)
	}
	// One millisecond past now: see TestDefaultWindowIncludesTheCurrentInstant.
	if want := refNow.Add(time.Millisecond); !q.Range.To.Equal(want) {
		t.Errorf("To = %v, want %v", q.Range.To, want)
	}
	if want := refNow.Add(time.Millisecond - defaultWindow); !q.Range.From.Equal(want) {
		t.Errorf("From = %v, want %v", q.Range.From, want)
	}
	if q.Metric != "" {
		t.Errorf("Metric = %q, want empty", q.Metric)
	}
	if got := q.requireMetric(); got != stats.LCP {
		t.Errorf("requireMetric() = %q, want lcp", got)
	}
}

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name    string
		values  url.Values
		wantErr bool
		check   func(t *testing.T, q query)
	}{
		{
			name:   "explicit metric",
			values: url.Values{"metric": {"cls"}},
			check: func(t *testing.T, q query) {
				if q.Metric != stats.CLS {
					t.Errorf("Metric = %q, want cls", q.Metric)
				}
			},
		},
		{
			name:   "metric is case insensitive",
			values: url.Values{"metric": {"LCP"}},
			check: func(t *testing.T, q query) {
				if q.Metric != stats.LCP {
					t.Errorf("Metric = %q, want lcp", q.Metric)
				}
			},
		},
		{
			name:    "unknown metric",
			values:  url.Values{"metric": {"fid"}},
			wantErr: true,
		},
		{
			name:   "relative from closes the open end at now",
			values: url.Values{"from": {"6h"}},
			check: func(t *testing.T, q query) {
				if want := refNow.Add(-6 * time.Hour); !q.Range.From.Equal(want) {
					t.Errorf("From = %v, want %v", q.Range.From, want)
				}
				// An open end must not survive into a bucketed query: see
				// TestRangeIsAlwaysBoundedAndSane.
				if want := refNow.Add(time.Millisecond); !q.Range.To.Equal(want) {
					t.Errorf("To = %v, want %v", q.Range.To, want)
				}
			},
		},
		{
			name:   "only to given closes the other end",
			values: url.Values{"to": {refNow.Format(time.RFC3339)}},
			check: func(t *testing.T, q query) {
				if !q.Range.To.Equal(refNow) {
					t.Errorf("To = %v, want %v", q.Range.To, refNow)
				}
				if want := refNow.Add(-defaultWindow); !q.Range.From.Equal(want) {
					t.Errorf("From = %v, want %v", q.Range.From, want)
				}
			},
		},
		{
			name:   "bucket count",
			values: url.Values{"n": {"12"}},
			check: func(t *testing.T, q query) {
				if q.Buckets != 12 {
					t.Errorf("Buckets = %d, want 12", q.Buckets)
				}
			},
		},
		{name: "zero buckets", values: url.Values{"n": {"0"}}, wantErr: true},
		{name: "negative buckets", values: url.Values{"n": {"-5"}}, wantErr: true},
		{name: "too many buckets", values: url.Values{"n": {"100000"}}, wantErr: true},
		{name: "non-numeric buckets", values: url.Values{"n": {"many"}}, wantErr: true},
		{name: "bad from", values: url.Values{"from": {"nonsense"}}, wantErr: true},
		{name: "bad to", values: url.Values{"to": {"nonsense"}}, wantErr: true},
		{
			name:   "buckets exactly at the cap",
			values: url.Values{"n": {"720"}},
			check: func(t *testing.T, q query) {
				if q.Buckets != maxBuckets {
					t.Errorf("Buckets = %d, want %d", q.Buckets, maxBuckets)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := parseQuery(tt.values, refNow)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseQuery(%v) = %+v, want an error", tt.values, q)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseQuery(%v): %v", tt.values, err)
			}
			if tt.check != nil {
				tt.check(t, q)
			}
		})
	}
}

// TestRangeIsAlwaysBoundedAndSane is a regression test.
//
// The dashboard sends only "from". When "to" was left open it reached
// store.Range.Normalize, which substitutes the year 9999; subtracting from that
// saturates time.Duration at roughly 292 years, and a "last 24 hours" chart was
// silently bucketed into six-year intervals. Found by driving the real dashboard
// in Chrome, not by any unit test, because every test here passed both ends.
func TestRangeIsAlwaysBoundedAndSane(t *testing.T) {
	cases := []url.Values{
		{},
		{"from": {"24h"}},
		{"from": {"1h"}},
		{"to": {refNow.Format(time.RFC3339)}},
		{"metric": {"lcp"}},
	}

	for _, v := range cases {
		q, err := parseQuery(v, refNow)
		if err != nil {
			t.Fatalf("parseQuery(%v): %v", v, err)
		}
		if q.Range.From.IsZero() || q.Range.To.IsZero() {
			t.Errorf("parseQuery(%v) left an end open: %v to %v", v, q.Range.From, q.Range.To)
			continue
		}

		span := q.Range.To.Sub(q.Range.From)
		if span <= 0 {
			t.Errorf("parseQuery(%v) span = %v, want positive", v, span)
		}
		// Anything approaching the int64 nanosecond ceiling means an open end
		// leaked through and the duration saturated.
		if span > 365*24*time.Hour*100 {
			t.Errorf("parseQuery(%v) span = %v, which is implausible and suggests overflow", v, span)
		}
		if bucket := span / time.Duration(q.Buckets); bucket > 90*24*time.Hour {
			t.Errorf("parseQuery(%v) bucket width = %v, far too coarse", v, bucket)
		}
	}
}

func TestParseQueryPercentile(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    float64
		wantErr bool
	}{
		{name: "default is p75", raw: "", want: 0.75},
		{name: "median", raw: "p=50", want: 0.50},
		{name: "p90", raw: "p=90", want: 0.90},
		{name: "p95", raw: "p=95", want: 0.95},
		{name: "unlisted quantile", raw: "p=99", wantErr: true},
		{name: "fraction rather than percent", raw: "p=0.9", wantErr: true},
		{name: "not a number", raw: "p=high", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := url.ParseQuery(tt.raw)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tt.raw, err)
			}

			q, err := parseQuery(v, refNow)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseQuery(%q) = %v, want an error", tt.raw, q.Percentile)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseQuery(%q): %v", tt.raw, err)
			}
			if q.Percentile != tt.want {
				t.Errorf("Percentile = %v, want %v", q.Percentile, tt.want)
			}
		})
	}
}

func TestParseQueryRoute(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "absent", raw: "", want: ""},
		{name: "path", raw: "route=%2Fpricing", want: "/pricing"},
		{name: "trimmed", raw: "route=+%2Fpricing+", want: "/pricing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := url.ParseQuery(tt.raw)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tt.raw, err)
			}

			q, err := parseQuery(v, refNow)
			if err != nil {
				t.Fatalf("parseQuery(%q): %v", tt.raw, err)
			}
			if q.Route != tt.want {
				t.Errorf("Route = %q, want %q", q.Route, tt.want)
			}
		})
	}
}

func TestDefaultWindowIncludesTheCurrentInstant(t *testing.T) {
	// A record stamped at the instant the request is served must fall inside
	// the default window. The store's range is half-open, so a window ending
	// exactly at now would drop it, and on a coarse clock it does.
	q, err := parseQuery(url.Values{}, refNow)
	if err != nil {
		t.Fatalf("parseQuery: %v", err)
	}

	if !q.Range.To.After(refNow) {
		t.Errorf("To = %v, want an instant after now (%v)", q.Range.To, refNow)
	}
	if got := q.Range.To.Sub(refNow); got != time.Millisecond {
		t.Errorf("To is %v past now, want exactly 1ms", got)
	}
	if got := q.Range.To.Sub(q.Range.From); got != defaultWindow {
		t.Errorf("window = %v, want %v", got, defaultWindow)
	}

	// An explicit end stays exactly where the caller put it.
	v := url.Values{"to": []string{refNow.Format(time.RFC3339)}}
	q, err = parseQuery(v, refNow)
	if err != nil {
		t.Fatalf("parseQuery: %v", err)
	}
	if !q.Range.To.Equal(refNow) {
		t.Errorf("explicit To = %v, want %v", q.Range.To, refNow)
	}
}
