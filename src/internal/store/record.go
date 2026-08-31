// Package store persists Core Web Vitals measurements as an append-only log on
// local disk and serves time-range queries from an in-memory index.
//
// The format is JSON Lines, one file per UTC day, replayed into a sorted slice
// at startup. This is a deliberate choice for the scale it targets, one site:
// no replication, no compaction, no query planner, and the whole set lives in
// memory. Writes are buffered, so a crash loses at most [FlushInterval].
package store

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"vitals/src/internal/stats"
)

// Record is one page view's worth of measurements.
type Record struct {
	// At is when the beacon was received, truncated to milliseconds.
	At time.Time
	// Route is the request path with the query string stripped.
	Route string
	// Session is the coarse, daily-rotating visitor identifier. Derived, never
	// stored in a cookie, and unlinkable across days.
	Session string
	// Width is the viewport width in CSS pixels, used to derive a device class.
	Width int
	// Nav is how the page view began: a browser navigation type, or the full
	// beacon's label for a back-forward cache restore or a soft navigation.
	// Empty when the beacon did not report one, which is every record the
	// small beacon writes.
	Nav string
	// Attr names the element responsible for a metric, keyed by that metric.
	// Only the full beacon reports it, and only for LCP, CLS, and INP. An
	// absent entry means the beacon could not name an element, not that no
	// element was involved.
	Attr map[stats.Metric]string
	// Values holds the measured metrics. An absent metric was not reported,
	// which is not the same as being reported as zero.
	Values map[stats.Metric]float64
}

// wireRecord is the on-disk shape. Short keys: written once per page view and
// read back in full on every restart.
type wireRecord struct {
	T int64              `json:"t"`           // epoch milliseconds
	U string             `json:"u"`           // route
	S string             `json:"s,omitempty"` // session
	W int                `json:"w,omitempty"` // viewport width
	N string             `json:"n,omitempty"` // navigation type
	A map[string]string  `json:"a,omitempty"` // element attribution, by metric
	M map[string]float64 `json:"m"`           // metric values
}

// MarshalLine encodes r as a single JSON line without a trailing newline.
func (r Record) MarshalLine() ([]byte, error) {
	w := wireRecord{
		T: r.At.UTC().UnixMilli(),
		U: r.Route,
		S: r.Session,
		W: r.Width,
		N: r.Nav,
		M: make(map[string]float64, len(r.Values)),
	}
	for m, v := range r.Values {
		w.M[string(m)] = v
	}
	// Written only when there is something to write: the small beacon reports
	// no attribution at all, and an empty object on every one of its lines
	// would be pure overhead in the log.
	for m, v := range r.Attr {
		if v == "" {
			continue
		}
		if w.A == nil {
			w.A = make(map[string]string, len(r.Attr))
		}
		w.A[string(m)] = v
	}

	b, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("encoding record: %w", err)
	}
	return b, nil
}

// UnmarshalLine decodes a single JSON line produced by [Record.MarshalLine].
// Unknown metric keys and non-finite values are dropped rather than rejected,
// so one bad field does not cost the whole record.
func UnmarshalLine(line []byte) (Record, error) {
	var w wireRecord
	if err := json.Unmarshal(line, &w); err != nil {
		return Record{}, fmt.Errorf("decoding record: %w", err)
	}
	if w.T <= 0 {
		return Record{}, fmt.Errorf("decoding record: missing or invalid timestamp %d", w.T)
	}

	r := Record{
		At:      time.UnixMilli(w.T).UTC(),
		Route:   w.U,
		Session: w.S,
		Width:   w.W,
		Nav:     w.N,
		Values:  make(map[stats.Metric]float64, len(w.M)),
	}
	for k, v := range w.A {
		m := stats.Metric(k)
		if !stats.Valid(m) || v == "" {
			continue
		}
		if r.Attr == nil {
			r.Attr = make(map[stats.Metric]string, len(w.A))
		}
		r.Attr[m] = v
	}
	for k, v := range w.M {
		m := stats.Metric(k)
		if !stats.Valid(m) {
			continue
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			continue
		}
		r.Values[m] = v
	}
	return r, nil
}

// DeviceClass is a coarse device bucket derived from viewport width.
type DeviceClass string

// The three device classes, derived from viewport width rather than from the
// user-agent string, which is unreliable and a fingerprinting surface.
const (
	DeviceMobile  DeviceClass = "mobile"
	DeviceTablet  DeviceClass = "tablet"
	DeviceDesktop DeviceClass = "desktop"
	DeviceUnknown DeviceClass = "unknown"
)

// Device breakpoints in CSS pixels.
const (
	mobileMaxWidth = 767
	tabletMaxWidth = 1023
)

// Device returns the record's device class, or [DeviceUnknown] when no viewport
// width was reported.
func (r Record) Device() DeviceClass {
	switch {
	case r.Width <= 0:
		return DeviceUnknown
	case r.Width <= mobileMaxWidth:
		return DeviceMobile
	case r.Width <= tabletMaxWidth:
		return DeviceTablet
	default:
		return DeviceDesktop
	}
}
