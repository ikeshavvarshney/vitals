// Package stats provides fixed-bucket histograms, approximate percentiles, and
// Core Web Vitals banding.
//
// Percentiles are read off cumulative bucket counts rather than computed from a
// sorted sample set. That is what production RUM systems do: it is O(1) per
// observation, allocates nothing after construction, and keeps memory flat
// regardless of traffic. It is also approximate, and the error is bounded and
// stated. See [Histogram.RelativeError].
package stats

import (
	"math"
)

// Duration histogram layout. Bucket boundaries grow geometrically so that
// relative error is constant across the range, which is the right shape for
// latency: the difference between 20ms and 25ms does not matter, the difference
// between 2000ms and 2500ms does.
const (
	durationMin   = 1.0     // ms; values below this share bucket 0
	durationMax   = 60000.0 // ms; values at or above this share the overflow bucket
	durationRatio = 1.1     // each bucket is 10% wider than the one below it
)

// Score histogram layout, used for CLS. CLS is unitless, small, and bounded in
// practice, so linear buckets give a flat absolute error instead of a flat
// relative one.
const (
	scoreMax   = 1.0
	scoreWidth = 0.005
)

// Layout describes how observed values map onto bucket indices.
type Layout int

const (
	// LayoutDuration is the geometric layout used for millisecond metrics.
	LayoutDuration Layout = iota
	// LayoutScore is the linear layout used for CLS.
	LayoutScore
)

// Histogram counts observations in fixed buckets and answers approximate
// quantile queries. The zero value is not usable; construct one with [New].
//
// A Histogram is not safe for concurrent use.
type Histogram struct {
	layout  Layout
	counts  []uint64
	total   uint64
	min     float64
	max     float64
	sum     float64
	nonZero bool
}

// New returns an empty Histogram with the given bucket layout.
func New(layout Layout) *Histogram {
	return &Histogram{
		layout: layout,
		counts: make([]uint64, bucketCount(layout)),
	}
}

// bucketCount returns the number of buckets in a layout, including the
// underflow bucket at index 0 and the overflow bucket at the top.
func bucketCount(layout Layout) int {
	switch layout {
	case LayoutScore:
		// One bucket per scoreWidth across [0, scoreMax), plus overflow.
		return int(math.Ceil(scoreMax/scoreWidth)) + 1
	default:
		// Bucket 0 holds [0, durationMin). Buckets 1..n cover the geometric
		// range. The final bucket holds everything at or above durationMax.
		n := int(math.Ceil(math.Log(durationMax/durationMin) / math.Log(durationRatio)))
		return n + 2
	}
}

// Add records one observation. Negative and non-finite values are ignored:
// they cannot come from a well-formed measurement, and silently dropping one
// bad sample is better than poisoning every percentile derived from it.
func (h *Histogram) Add(v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return
	}

	i := h.index(v)
	h.counts[i]++

	if !h.nonZero {
		h.min, h.max = v, v
		h.nonZero = true
	} else {
		h.min = math.Min(h.min, v)
		h.max = math.Max(h.max, v)
	}
	h.sum += v
	h.total++
}

// index returns the bucket index for a non-negative, finite value.
func (h *Histogram) index(v float64) int {
	last := len(h.counts) - 1

	if h.layout == LayoutScore {
		i := int(v / scoreWidth)
		if i > last {
			return last
		}
		return i
	}

	if v < durationMin {
		return 0
	}
	if v >= durationMax {
		return last
	}
	i := 1 + int(math.Log(v/durationMin)/math.Log(durationRatio))
	if i > last {
		return last
	}
	return i
}

// representative returns the value reported for observations in bucket i.
//
// For the geometric layout it is the geometric mean of the bucket bounds, which
// halves the worst-case relative error compared with reporting either bound.
// For the linear layout it is the arithmetic midpoint.
func (h *Histogram) representative(i int) float64 {
	last := len(h.counts) - 1

	if h.layout == LayoutScore {
		if i >= last {
			// Open-ended top bucket: the observed maximum is the honest answer.
			return math.Max(h.max, scoreMax)
		}
		return (float64(i) + 0.5) * scoreWidth
	}

	switch {
	case i == 0:
		return durationMin / 2
	case i >= last:
		return math.Max(h.max, durationMax)
	default:
		lo := durationMin * math.Pow(durationRatio, float64(i-1))
		return lo * math.Sqrt(durationRatio)
	}
}

// Quantile returns the approximate value at quantile q, where q is in [0, 1].
// ok is false when the histogram is empty, in which case the value is zero.
//
// The result is the representative value of the bucket containing the sample at
// rank ceil(q*n), so it is accurate to within the bucket width. It is never
// interpolated between buckets: interpolation would imply a precision the data
// does not have.
func (h *Histogram) Quantile(q float64) (value float64, ok bool) {
	if h.total == 0 {
		return 0, false
	}
	if q <= 0 {
		return h.min, true
	}
	if q >= 1 {
		return h.max, true
	}

	// Rank of the sample we want, 1-based.
	rank := uint64(math.Ceil(q * float64(h.total)))
	if rank < 1 {
		rank = 1
	}

	var cum uint64
	for i, c := range h.counts {
		cum += c
		if cum >= rank {
			return clampToObserved(h, h.representative(i)), true
		}
	}
	return h.max, true
}

// clampToObserved keeps a bucket representative inside the range of values that
// were actually observed. Without this, a histogram holding a single 3ms sample
// would report a p75 of 3.14ms, which is a number no visitor experienced.
func clampToObserved(h *Histogram, v float64) float64 {
	return math.Min(math.Max(v, h.min), h.max)
}

// Count returns the number of observations recorded.
func (h *Histogram) Count() uint64 { return h.total }

// Min returns the smallest observed value, or 0 if empty.
func (h *Histogram) Min() float64 { return h.min }

// Max returns the largest observed value, or 0 if empty.
func (h *Histogram) Max() float64 { return h.max }

// Mean returns the arithmetic mean of the observations, or 0 if empty. Unlike
// the quantiles, the mean is exact: it is accumulated from the raw values.
func (h *Histogram) Mean() float64 {
	if h.total == 0 {
		return 0
	}
	return h.sum / float64(h.total)
}

// Merge adds the contents of other into h. Both histograms must use the same
// layout; merging different layouts returns an error rather than producing a
// quietly wrong result.
func (h *Histogram) Merge(other *Histogram) error {
	if other == nil {
		return nil
	}
	if h.layout != other.layout {
		return errLayoutMismatch
	}
	for i, c := range other.counts {
		h.counts[i] += c
	}
	if other.nonZero {
		if !h.nonZero {
			h.min, h.max = other.min, other.max
			h.nonZero = true
		} else {
			h.min = math.Min(h.min, other.min)
			h.max = math.Max(h.max, other.max)
		}
	}
	h.sum += other.sum
	h.total += other.total
	return nil
}

// RelativeError returns the worst-case relative error of a quantile drawn from
// the geometric range of this histogram, as a fraction.
//
// For the duration layout this is sqrt(ratio)-1, about 0.0488, because the
// reported value is the geometric mean of a bucket whose bounds differ by a
// factor of ratio. The score layout is linear, so its error is absolute rather
// than relative and this returns 0; use [Histogram.AbsoluteError] instead.
func (h *Histogram) RelativeError() float64 {
	if h.layout == LayoutScore {
		return 0
	}
	return math.Sqrt(durationRatio) - 1
}

// AbsoluteError returns the worst-case absolute error for the score layout,
// which is half a bucket width. It returns 0 for the duration layout, whose
// error is relative rather than absolute.
func (h *Histogram) AbsoluteError() float64 {
	if h.layout == LayoutScore {
		return scoreWidth / 2
	}
	return 0
}
