package stats

import (
	"math"
	"testing"
)

// withinRelative reports whether got is within tol (a fraction) of want.
func withinRelative(got, want, tol float64) bool {
	if want == 0 {
		return math.Abs(got) <= tol
	}
	return math.Abs(got-want)/math.Abs(want) <= tol
}

func TestHistogramEmpty(t *testing.T) {
	h := New(LayoutDuration)

	if got, ok := h.Quantile(0.75); ok || got != 0 {
		t.Errorf("Quantile on empty = (%v, %v), want (0, false)", got, ok)
	}
	if h.Count() != 0 {
		t.Errorf("Count = %d, want 0", h.Count())
	}
	if h.Mean() != 0 {
		t.Errorf("Mean = %v, want 0", h.Mean())
	}
}

func TestHistogramSingleSample(t *testing.T) {
	tests := []struct {
		name   string
		layout Layout
		value  float64
	}{
		{"duration mid range", LayoutDuration, 1834.2},
		{"duration sub-millisecond", LayoutDuration, 0.4},
		{"duration at floor", LayoutDuration, 1},
		{"duration above ceiling", LayoutDuration, 120000},
		{"score typical", LayoutScore, 0.06},
		{"score zero", LayoutScore, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(tt.layout)
			h.Add(tt.value)

			// With one sample every quantile must be that sample. Clamping to
			// the observed range is what guarantees this.
			for _, q := range []float64{0, 0.5, 0.75, 0.95, 1} {
				got, ok := h.Quantile(q)
				if !ok {
					t.Fatalf("Quantile(%v) not ok", q)
				}
				if got != tt.value {
					t.Errorf("Quantile(%v) = %v, want %v", q, got, tt.value)
				}
			}
			if h.Count() != 1 {
				t.Errorf("Count = %d, want 1", h.Count())
			}
		})
	}
}

func TestHistogramIgnoresInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		value float64
	}{
		{"negative", -1},
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(LayoutDuration)
			h.Add(tt.value)
			if h.Count() != 0 {
				t.Errorf("Add(%v) recorded a sample; Count = %d, want 0", tt.value, h.Count())
			}
		})
	}
}

func TestHistogramQuantileKnownDistribution(t *testing.T) {
	// 1..100 milliseconds, one sample each. The exact p75 is 75, p50 is 50,
	// p90 is 90. Each must land within the bucket error bound.
	h := New(LayoutDuration)
	for i := 1; i <= 100; i++ {
		h.Add(float64(i))
	}

	tol := h.RelativeError()
	tests := []struct {
		q    float64
		want float64
	}{
		{0.5, 50},
		{0.75, 75},
		{0.9, 90},
		{0.95, 95},
	}

	for _, tt := range tests {
		got, ok := h.Quantile(tt.q)
		if !ok {
			t.Fatalf("Quantile(%v) not ok", tt.q)
		}
		if !withinRelative(got, tt.want, tol) {
			t.Errorf("Quantile(%v) = %v, want %v within %.4f relative", tt.q, got, tt.want, tol)
		}
	}
}

func TestHistogramQuantileSkewedDistribution(t *testing.T) {
	// 90 fast samples and 10 slow ones. p75 must sit in the fast group and p95
	// in the slow group; a percentile that ignored the tail would get p95 wrong.
	h := New(LayoutDuration)
	for i := 0; i < 90; i++ {
		h.Add(500)
	}
	for i := 0; i < 10; i++ {
		h.Add(8000)
	}

	tol := h.RelativeError()

	p75, _ := h.Quantile(0.75)
	if !withinRelative(p75, 500, tol) {
		t.Errorf("p75 = %v, want 500 within %.4f relative", p75, tol)
	}

	p95, _ := h.Quantile(0.95)
	if !withinRelative(p95, 8000, tol) {
		t.Errorf("p95 = %v, want 8000 within %.4f relative", p95, tol)
	}
}

func TestHistogramQuantileScoreLayout(t *testing.T) {
	// CLS values 0.00 to 0.99 in hundredths. Exact p75 is 0.74.
	h := New(LayoutScore)
	for i := 0; i < 100; i++ {
		h.Add(float64(i) / 100)
	}

	got, ok := h.Quantile(0.75)
	if !ok {
		t.Fatal("Quantile(0.75) not ok")
	}
	tol := h.AbsoluteError() + 0.01 // half a bucket, plus the input's own step
	if math.Abs(got-0.74) > tol {
		t.Errorf("Quantile(0.75) = %v, want 0.74 within %v absolute", got, tol)
	}
}

func TestHistogramErrorBoundHolds(t *testing.T) {
	// The stated relative error bound is a promise made in the README. Assert it
	// against a spread of values across the whole geometric range.
	h := New(LayoutDuration)
	values := []float64{1, 2, 5, 12, 47, 133, 512, 1834, 4096, 9999, 41000}
	for _, v := range values {
		h.Add(v)
	}

	tol := h.RelativeError()
	if tol > 0.05 {
		t.Fatalf("RelativeError = %v, want the documented bound of about 0.0488", tol)
	}

	// Every single-sample histogram of these values must report that value back
	// within the bound.
	for _, v := range values {
		one := New(LayoutDuration)
		one.Add(v)
		one.Add(v) // two samples, so clamping is not doing all the work
		got, _ := one.Quantile(0.75)
		if !withinRelative(got, v, tol) {
			t.Errorf("value %v round-tripped as %v, outside the %.4f bound", v, got, tol)
		}
	}
}

func TestHistogramOverflowAndUnderflow(t *testing.T) {
	h := New(LayoutDuration)
	h.Add(0.1)     // underflow bucket
	h.Add(0.2)     // underflow bucket
	h.Add(250000)  // overflow bucket
	h.Add(1000000) // overflow bucket

	if h.Count() != 4 {
		t.Fatalf("Count = %d, want 4", h.Count())
	}
	if h.Min() != 0.1 {
		t.Errorf("Min = %v, want 0.1", h.Min())
	}
	if h.Max() != 1000000 {
		t.Errorf("Max = %v, want 1000000", h.Max())
	}

	// p100 is the observed maximum, never a bucket bound.
	if got, _ := h.Quantile(1); got != 1000000 {
		t.Errorf("Quantile(1) = %v, want 1000000", got)
	}
	// p0 is the observed minimum.
	if got, _ := h.Quantile(0); got != 0.1 {
		t.Errorf("Quantile(0) = %v, want 0.1", got)
	}
}

func TestHistogramMean(t *testing.T) {
	h := New(LayoutDuration)
	for _, v := range []float64{10, 20, 30, 40} {
		h.Add(v)
	}
	if got := h.Mean(); got != 25 {
		t.Errorf("Mean = %v, want 25 exactly", got)
	}
}

func TestHistogramMerge(t *testing.T) {
	a := New(LayoutDuration)
	b := New(LayoutDuration)
	for i := 1; i <= 50; i++ {
		a.Add(float64(i))
	}
	for i := 51; i <= 100; i++ {
		b.Add(float64(i))
	}

	if err := a.Merge(b); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if a.Count() != 100 {
		t.Errorf("Count after merge = %d, want 100", a.Count())
	}
	if a.Min() != 1 || a.Max() != 100 {
		t.Errorf("range after merge = [%v, %v], want [1, 100]", a.Min(), a.Max())
	}

	got, _ := a.Quantile(0.75)
	if !withinRelative(got, 75, a.RelativeError()) {
		t.Errorf("merged p75 = %v, want 75 within bound", got)
	}
}

func TestHistogramMergeIntoEmpty(t *testing.T) {
	a := New(LayoutDuration)
	b := New(LayoutDuration)
	b.Add(42)

	if err := a.Merge(b); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if a.Count() != 1 {
		t.Fatalf("Count = %d, want 1", a.Count())
	}
	if a.Min() != 42 || a.Max() != 42 {
		t.Errorf("range = [%v, %v], want [42, 42]", a.Min(), a.Max())
	}
}

func TestHistogramMergeNil(t *testing.T) {
	a := New(LayoutDuration)
	a.Add(5)
	if err := a.Merge(nil); err != nil {
		t.Fatalf("Merge(nil) = %v, want nil", err)
	}
	if a.Count() != 1 {
		t.Errorf("Count = %d, want 1", a.Count())
	}
}

func TestHistogramMergeLayoutMismatch(t *testing.T) {
	a := New(LayoutDuration)
	b := New(LayoutScore)
	if err := a.Merge(b); err == nil {
		t.Error("Merge across layouts returned nil, want an error")
	}
}

func TestBucketCountsAreSane(t *testing.T) {
	// A layout change that silently altered the bucket count would change every
	// stored percentile. Pin the numbers so that requires a deliberate edit.
	tests := []struct {
		layout Layout
		want   int
	}{
		{LayoutDuration, 118}, // ceil(log(60000)/log(1.1)) = 116, plus under and overflow
		{LayoutScore, 201},    // 200 buckets of 0.005 across [0, 1), plus overflow
	}
	for _, tt := range tests {
		if got := bucketCount(tt.layout); got != tt.want {
			t.Errorf("bucketCount(%v) = %d, want %d", tt.layout, got, tt.want)
		}
	}
}
