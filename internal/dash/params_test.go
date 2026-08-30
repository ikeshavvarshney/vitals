package dash

import (
	"net/url"
	"testing"
	"time"

	"vitals/internal/stats"
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
	if !q.Range.To.Equal(refNow) {
		t.Errorf("To = %v, want %v", q.Range.To, refNow)
	}
	if want := refNow.Add(-defaultWindow); !q.Range.From.Equal(want) {
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
			name:   "relative from",
			values: url.Values{"from": {"6h"}},
			check: func(t *testing.T, q query) {
				if want := refNow.Add(-6 * time.Hour); !q.Range.From.Equal(want) {
					t.Errorf("From = %v, want %v", q.Range.From, want)
				}
				// Only from was given, so to stays open rather than defaulting.
				if !q.Range.To.IsZero() {
					t.Errorf("To = %v, want the zero time", q.Range.To)
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
