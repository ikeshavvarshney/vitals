package store

import (
	"math"
	"testing"
	"time"

	"vitals/internal/stats"
)

func TestRecordRoundTrip(t *testing.T) {
	want := Record{
		At:      time.UnixMilli(1756500000000).UTC(),
		Route:   "/pricing",
		Session: "a1b2c3d4",
		Width:   1440,
		Values: map[stats.Metric]float64{
			stats.LCP:  1834.2,
			stats.CLS:  0.06,
			stats.INP:  142,
			stats.TTFB: 210.5,
			stats.FCP:  903.1,
		},
	}

	line, err := want.MarshalLine()
	if err != nil {
		t.Fatalf("MarshalLine: %v", err)
	}

	got, err := UnmarshalLine(line)
	if err != nil {
		t.Fatalf("UnmarshalLine: %v", err)
	}

	if !got.At.Equal(want.At) {
		t.Errorf("At = %v, want %v", got.At, want.At)
	}
	if got.Route != want.Route || got.Session != want.Session || got.Width != want.Width {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if len(got.Values) != len(want.Values) {
		t.Fatalf("got %d values, want %d", len(got.Values), len(want.Values))
	}
	for m, v := range want.Values {
		if got.Values[m] != v {
			t.Errorf("Values[%s] = %v, want %v", m, got.Values[m], v)
		}
	}
}

func TestUnmarshalLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantErr    bool
		wantValues int
		check      func(t *testing.T, r Record)
	}{
		{
			name:       "minimal valid record",
			line:       `{"t":1756500000000,"u":"/","m":{"lcp":1200}}`,
			wantValues: 1,
		},
		{
			name:    "empty object has no timestamp",
			line:    `{}`,
			wantErr: true,
		},
		{
			name:    "not JSON at all",
			line:    `this is not json`,
			wantErr: true,
		},
		{
			name:    "truncated mid-object, as a crash leaves it",
			line:    `{"t":1756500000000,"u":"/pric`,
			wantErr: true,
		},
		{
			name:    "zero timestamp rejected",
			line:    `{"t":0,"u":"/","m":{}}`,
			wantErr: true,
		},
		{
			name:    "negative timestamp rejected",
			line:    `{"t":-5,"u":"/","m":{}}`,
			wantErr: true,
		},
		{
			name:       "unknown metric key dropped, record kept",
			line:       `{"t":1756500000000,"u":"/","m":{"lcp":1200,"fid":88,"bogus":1}}`,
			wantValues: 1,
		},
		{
			name:       "negative metric value dropped",
			line:       `{"t":1756500000000,"u":"/","m":{"lcp":-1,"cls":0.05}}`,
			wantValues: 1,
		},
		{
			name:       "no metrics at all is still a valid record",
			line:       `{"t":1756500000000,"u":"/","m":{}}`,
			wantValues: 0,
		},
		{
			name:       "unicode route survives",
			line:       `{"t":1756500000000,"u":"/café/über","m":{"lcp":1000}}`,
			wantValues: 1,
			check: func(t *testing.T, r Record) {
				if r.Route != "/café/über" {
					t.Errorf("Route = %q, want %q", r.Route, "/café/über")
				}
			},
		},
		{
			name:       "missing metrics object",
			line:       `{"t":1756500000000,"u":"/"}`,
			wantValues: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UnmarshalLine([]byte(tt.line))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalLine(%q) = nil error, want an error", tt.line)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalLine(%q): %v", tt.line, err)
			}
			if len(got.Values) != tt.wantValues {
				t.Errorf("got %d values, want %d: %v", len(got.Values), tt.wantValues, got.Values)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestUnmarshalLineDropsNonFinite(t *testing.T) {
	// encoding/json cannot produce NaN or Inf, but a hand-edited or corrupted
	// file can contain values that decode to them through a very large exponent.
	line := `{"t":1756500000000,"u":"/","m":{"lcp":1e400,"cls":0.05}}`
	got, err := UnmarshalLine([]byte(line))
	if err != nil {
		// Go's decoder rejects an out-of-range float outright, which is also an
		// acceptable outcome: the record is dropped rather than stored as Inf.
		return
	}
	for m, v := range got.Values {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			t.Errorf("Values[%s] = %v, want no non-finite values", m, v)
		}
	}
}

func TestDevice(t *testing.T) {
	tests := []struct {
		width int
		want  DeviceClass
	}{
		{0, DeviceUnknown},
		{-1, DeviceUnknown},
		{320, DeviceMobile},
		{767, DeviceMobile},
		{768, DeviceTablet},
		{1023, DeviceTablet},
		{1024, DeviceDesktop},
		{2560, DeviceDesktop},
	}
	for _, tt := range tests {
		r := Record{Width: tt.width}
		if got := r.Device(); got != tt.want {
			t.Errorf("width %d: Device() = %v, want %v", tt.width, got, tt.want)
		}
	}
}
