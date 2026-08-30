package dash

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"vitals/internal/ingest"
	"vitals/internal/stats"
	"vitals/internal/store"
)

// percentile is the quantile every figure on the dashboard reports. Core Web
// Vitals are assessed at the 75th.
const percentile = 0.75

// API answers the dashboard's JSON requests over a measurement store.
type API struct {
	store    *store.Store
	counters func() ingest.Counters
	now      func() time.Time
}

// NewAPI returns an API reading from s. A nil counters function reports zeros.
func NewAPI(s *store.Store, counters func() ingest.Counters) *API {
	if counters == nil {
		counters = func() ingest.Counters { return ingest.Counters{} }
	}
	return &API{
		store:    s,
		counters: counters,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Register attaches the API's routes to a mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/summary", a.handleSummary)
	mux.HandleFunc("GET /api/series", a.handleSeries)
	mux.HandleFunc("GET /api/routes", a.handleRoutes)
	mux.HandleFunc("GET /api/devices", a.handleDevices)
}

// metricSummary is one metric's headline figure.
type metricSummary struct {
	Metric  stats.Metric `json:"metric"`
	P75     *float64     `json:"p75"` // null when there are no samples
	Band    string       `json:"band"`
	Samples uint64       `json:"samples"`
	// Thresholds ship with the response so the dashboard need not duplicate
	// the constants in JavaScript.
	Good             float64 `json:"good"`
	NeedsImprovement float64 `json:"needsImprovement"`
	Unit             string  `json:"unit"` // "ms", or "" for the unitless CLS
}

// summaryResponse is the payload of GET /api/summary.
type summaryResponse struct {
	From     time.Time        `json:"from"`
	To       time.Time        `json:"to"`
	Samples  int              `json:"samples"`
	Metrics  []metricSummary  `json:"metrics"`
	Ingest   ingest.Counters  `json:"ingest"`
	Coverage *coverageSummary `json:"coverage"`
}

// coverageSummary reports what the store holds overall, so the dashboard can
// distinguish an empty window from an empty store.
type coverageSummary struct {
	Total  int        `json:"total"`
	Oldest *time.Time `json:"oldest"`
	Newest *time.Time `json:"newest"`
}

func (a *API) handleSummary(w http.ResponseWriter, r *http.Request) {
	q, err := parseQuery(r.URL.Query(), a.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	hists := make(map[stats.Metric]*stats.Histogram, len(stats.Metrics))
	for _, m := range stats.Metrics {
		hists[m] = stats.New(stats.LayoutOf(m))
	}

	total := 0
	a.store.Each(q.Range, func(rec store.Record) bool {
		total++
		for m, v := range rec.Values {
			if h, ok := hists[m]; ok {
				h.Add(v)
			}
		}
		return true
	})

	resp := summaryResponse{
		From:    q.Range.From,
		To:      q.Range.To,
		Samples: total,
		Metrics: make([]metricSummary, 0, len(stats.Metrics)),
		Ingest:  a.counters(),
	}
	for _, m := range stats.Metrics {
		resp.Metrics = append(resp.Metrics, summarize(m, hists[m]))
	}
	resp.Coverage = a.coverage()

	writeJSON(w, resp)
}

// summarize turns one histogram into its reported figure.
func summarize(m stats.Metric, h *stats.Histogram) metricSummary {
	good, needs, _ := stats.Thresholds(m)

	out := metricSummary{
		Metric:           m,
		Samples:          h.Count(),
		Good:             good,
		NeedsImprovement: needs,
		Unit:             unitOf(m),
		Band:             "",
	}
	if v, ok := h.Quantile(percentile); ok {
		out.P75 = &v
		out.Band = stats.BandOf(m, v).String()
	}
	return out
}

// unitOf returns the display unit for a metric. CLS is a unitless score.
func unitOf(m stats.Metric) string {
	if m == stats.CLS {
		return ""
	}
	return "ms"
}

// coverage reports the store's overall extent.
func (a *API) coverage() *coverageSummary {
	c := &coverageSummary{Total: a.store.Count()}
	if oldest, newest, ok := a.store.Span(); ok {
		c.Oldest, c.Newest = &oldest, &newest
	}
	return c
}

// seriesBucket is one time bucket of a series.
type seriesBucket struct {
	// At is the start of the bucket.
	At      time.Time `json:"t"`
	P75     *float64  `json:"p75"`
	Band    string    `json:"band"`
	Samples uint64    `json:"samples"`
}

// seriesResponse is the payload of GET /api/series.
type seriesResponse struct {
	Metric           stats.Metric   `json:"metric"`
	From             time.Time      `json:"from"`
	To               time.Time      `json:"to"`
	BucketSeconds    float64        `json:"bucketSeconds"`
	Good             float64        `json:"good"`
	NeedsImprovement float64        `json:"needsImprovement"`
	Unit             string         `json:"unit"`
	Buckets          []seriesBucket `json:"buckets"`
}

func (a *API) handleSeries(w http.ResponseWriter, r *http.Request) {
	q, err := parseQuery(r.URL.Query(), a.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	m := q.requireMetric()
	rng := q.Range.Normalize()

	span := rng.To.Sub(rng.From)
	if span <= 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("range: to must be after from"))
		return
	}
	width := span / time.Duration(q.Buckets)
	if width <= 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("range: too short for %d buckets", q.Buckets))
		return
	}

	hists := make([]*stats.Histogram, q.Buckets)
	layout := stats.LayoutOf(m)
	for i := range hists {
		hists[i] = stats.New(layout)
	}

	a.store.Each(rng, func(rec store.Record) bool {
		v, ok := rec.Values[m]
		if !ok {
			return true
		}
		i := int(rec.At.Sub(rng.From) / width)
		// The final instant belongs to the last bucket, not to one past it.
		if i >= len(hists) {
			i = len(hists) - 1
		}
		if i < 0 {
			i = 0
		}
		hists[i].Add(v)
		return true
	})

	good, needs, _ := stats.Thresholds(m)
	resp := seriesResponse{
		Metric:           m,
		From:             rng.From,
		To:               rng.To,
		BucketSeconds:    width.Seconds(),
		Good:             good,
		NeedsImprovement: needs,
		Unit:             unitOf(m),
		Buckets:          make([]seriesBucket, len(hists)),
	}
	for i, h := range hists {
		b := seriesBucket{
			At:      rng.From.Add(time.Duration(i) * width),
			Samples: h.Count(),
		}
		if v, ok := h.Quantile(percentile); ok {
			b.P75 = &v
			b.Band = stats.BandOf(m, v).String()
		}
		resp.Buckets[i] = b
	}

	writeJSON(w, resp)
}

// groupRow is one row of a breakdown table.
type groupRow struct {
	Key     string   `json:"key"`
	P75     *float64 `json:"p75"`
	Band    string   `json:"band"`
	Samples uint64   `json:"samples"`
}

// groupResponse is the payload of the breakdown endpoints.
type groupResponse struct {
	Metric           stats.Metric `json:"metric"`
	From             time.Time    `json:"from"`
	To               time.Time    `json:"to"`
	Good             float64      `json:"good"`
	NeedsImprovement float64      `json:"needsImprovement"`
	Unit             string       `json:"unit"`
	Rows             []groupRow   `json:"rows"`
}

func (a *API) handleRoutes(w http.ResponseWriter, r *http.Request) {
	a.handleGroup(w, r, func(rec store.Record) string { return rec.Route })
}

func (a *API) handleDevices(w http.ResponseWriter, r *http.Request) {
	a.handleGroup(w, r, func(rec store.Record) string { return string(rec.Device()) })
}

// handleGroup answers a breakdown grouped by whatever key returns.
func (a *API) handleGroup(w http.ResponseWriter, r *http.Request, key func(store.Record) string) {
	q, err := parseQuery(r.URL.Query(), a.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	m := q.requireMetric()
	layout := stats.LayoutOf(m)
	groups := make(map[string]*stats.Histogram)

	a.store.Each(q.Range, func(rec store.Record) bool {
		v, ok := rec.Values[m]
		if !ok {
			return true
		}
		k := key(rec)
		h, ok := groups[k]
		if !ok {
			h = stats.New(layout)
			groups[k] = h
		}
		h.Add(v)
		return true
	})

	good, needs, _ := stats.Thresholds(m)
	resp := groupResponse{
		Metric:           m,
		From:             q.Range.From,
		To:               q.Range.To,
		Good:             good,
		NeedsImprovement: needs,
		Unit:             unitOf(m),
		Rows:             make([]groupRow, 0, len(groups)),
	}
	for k, h := range groups {
		row := groupRow{Key: k, Samples: h.Count()}
		if v, ok := h.Quantile(percentile); ok {
			row.P75 = &v
			row.Band = stats.BandOf(m, v).String()
		}
		resp.Rows = append(resp.Rows, row)
	}

	// Worst first; ties by key so the order is stable between requests.
	sort.Slice(resp.Rows, func(i, j int) bool {
		a, b := resp.Rows[i], resp.Rows[j]
		switch {
		case a.P75 == nil && b.P75 == nil:
			return a.Key < b.Key
		case a.P75 == nil:
			return false
		case b.P75 == nil:
			return true
		case *a.P75 != *b.P75:
			return *a.P75 > *b.P75
		default:
			return a.Key < b.Key
		}
	})

	writeJSON(w, resp)
}

// writeJSON sends v as JSON. The body is marshalled before any header is
// written, so an encoding failure yields a clean 500 rather than a truncated
// 200.
func writeJSON(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"encoding response"}`, http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store") // live data; a cached answer is stale
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeError sends a JSON error naming the parameter that was wrong.
func writeError(w http.ResponseWriter, status int, err error) {
	body, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
	if marshalErr != nil {
		http.Error(w, `{"error":"bad request"}`, status)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
