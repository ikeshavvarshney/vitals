// Package store persists Core Web Vitals measurements as an append-only log on
// local disk and serves time-range queries from an in-memory index.
//
// The on-disk format is JSON Lines, one file per UTC day. This is a deliberate
// choice rather than a placeholder: at the scale this tool targets, one site
// and thousands of page views a day, a log plus a sorted slice is the correct
// engineering answer. It has no replication, no compaction, and no query
// planner, and it would not survive being pointed at a large site.
//
// Writes are buffered. A crash loses at most [FlushInterval] of samples. For
// performance telemetry that is an intentional trade.
package store

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"vitals/internal/stats"
)

// Record is one page view's worth of measurements.
type Record struct {
	// At is when the beacon was received, truncated to milliseconds.
	At time.Time
	// Route is the request path with the query string stripped.
	Route string
	// Session is the coarse, daily-rotating visitor identifier. It is derived,
	// never stored in a cookie, and cannot be linked across days.
	Session string
	// Width is the viewport width in CSS pixels, used to derive a device class.
	Width int
	// Values holds the measured metrics. A metric absent from the map was not
	// reported by the browser, which is different from being reported as zero.
	Values map[stats.Metric]float64
}

// wireRecord is the on-disk shape. The keys are short because the file is
// written once per page view and read back in full on every restart.
type wireRecord struct {
	T int64              `json:"t"`           // epoch milliseconds
	U string             `json:"u"`           // route
	S string             `json:"s,omitempty"` // session
	W int                `json:"w,omitempty"` // viewport width
	M map[string]float64 `json:"m"`           // metric values
}

// MarshalLine encodes r as a single JSON line without a trailing newline.
func (r Record) MarshalLine() ([]byte, error) {
	w := wireRecord{
		T: r.At.UTC().UnixMilli(),
		U: r.Route,
		S: r.Session,
		W: r.Width,
		M: make(map[string]float64, len(r.Values)),
	}
	for m, v := range r.Values {
		w.M[string(m)] = v
	}

	b, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("encoding record: %w", err)
	}
	return b, nil
}

// UnmarshalLine decodes a single JSON line produced by [Record.MarshalLine].
//
// Unknown metric keys and non-finite values are dropped rather than rejected: a
// log written by a newer version should still replay in an older one, and a
// single bad field should not cost the whole record.
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
		Values:  make(map[stats.Metric]float64, len(w.M)),
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

// The three device classes. They are derived from viewport width rather than
// from the user-agent string: user-agent sniffing is unreliable, is being
// actively reduced by browsers, and would be a fingerprinting surface we do
// not want.
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

// Device returns the record's device class, or [DeviceUnknown] when the beacon
// reported no viewport width.
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
