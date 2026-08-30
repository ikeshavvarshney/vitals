package dash

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"vitals/src/internal/beacon"
	"vitals/src/internal/ingest"
	"vitals/src/internal/stats"
	"vitals/src/internal/store"
)

// percentile is the quantile the dashboard reports unless a request asks for
// another. Core Web Vitals are assessed at the 75th.
const percentile = 0.75

// API answers the dashboard's JSON requests over a measurement store.
type API struct {
	store    *store.Store
	counters func() ingest.Counters
	now      func() time.Time
	// retention is how long day logs are kept, reported so the dashboard can
	// say why old measurements are gone. Zero means nothing is dropped.
	retention time.Duration
}

// SetRetention records the retention window the server was started with, for
// reporting only. The API never deletes anything.
func (a *API) SetRetention(d time.Duration) { a.retention = d }

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
	mux.HandleFunc("GET /api/report", a.handleReport)
}

// metricSummary is one metric's headline figure.
type metricSummary struct {
	Metric stats.Metric `json:"metric"`
	// Value is the figure at the requested percentile, null when there are no
	// samples. The key is not named p75: the percentile is selectable, and a
	// field called p75 holding a p90 would be a lie in every consumer.
	Value   *float64 `json:"value"`
	Band    string   `json:"band"`
	Samples uint64   `json:"samples"`
	// Previous is the same figure over the preceding window of equal length,
	// null when that window holds no samples for this metric.
	Previous        *float64 `json:"previous"`
	PreviousSamples uint64   `json:"previousSamples"`
	// Thresholds ship with the response so the dashboard need not duplicate
	// the constants in JavaScript.
	Good             float64 `json:"good"`
	NeedsImprovement float64 `json:"needsImprovement"`
	Unit             string  `json:"unit"` // "ms", or "" for the unitless CLS
}

// summaryResponse is the payload of GET /api/summary.
type summaryResponse struct {
	From       time.Time        `json:"from"`
	To         time.Time        `json:"to"`
	Samples    int              `json:"samples"`
	Percentile float64          `json:"percentile"`
	Route      string           `json:"route,omitempty"`
	Metrics    []metricSummary  `json:"metrics"`
	Ingest     ingest.Counters  `json:"ingest"`
	Coverage   *coverageSummary `json:"coverage"`
	// Compared describes the preceding window each metric's Previous figure
	// came from.
	Compared *comparison `json:"compared"`
	// BeaconBytes lets the dashboard show the beacon's size without issuing a
	// request for it. A tool arguing about page weight should not add a round
	// trip to report its own.
	BeaconBytes int `json:"beaconBytes"`
}

// comparison names the window a metric's previous figure was taken from.
type comparison struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Samples int       `json:"samples"`
}

// coverageSummary reports what the store holds overall, so the dashboard can
// distinguish an empty window from an empty store, and what that costs on disk.
type coverageSummary struct {
	Total  int        `json:"total"`
	Oldest *time.Time `json:"oldest"`
	Newest *time.Time `json:"newest"`
	// Bytes is the size of the day logs, Files their count, and
	// BytesPerRecord the average on-disk cost of one measurement. A tool that
	// argues about page weight should be able to say what it weighs on disk.
	Bytes          int64   `json:"bytes"`
	Files          int     `json:"files"`
	BytesPerRecord float64 `json:"bytesPerRecord"`
	// RetentionDays is how long day logs are kept, 0 when nothing is dropped.
	RetentionDays float64 `json:"retentionDays"`
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
	a.each(q, q.Range, func(rec store.Record) bool {
		total++
		for m, v := range rec.Values {
			if h, ok := hists[m]; ok {
				h.Add(v)
			}
		}
		return true
	})

	prev, prevRange, prevTotal := a.previous(q)

	resp := summaryResponse{
		From:       q.Range.From,
		To:         q.Range.To,
		Samples:    total,
		Percentile: q.Percentile,
		Route:      q.Route,
		Metrics:    make([]metricSummary, 0, len(stats.Metrics)),
		Ingest:     a.counters(),
		Compared: &comparison{
			From:    prevRange.From,
			To:      prevRange.To,
			Samples: prevTotal,
		},
	}
	for _, m := range stats.Metrics {
		resp.Metrics = append(resp.Metrics, summarize(m, hists[m], prev[m], q.Percentile))
	}
	resp.Coverage = a.coverage()
	resp.BeaconBytes = beacon.Size()

	writeJSON(w, resp)
}

// summarize turns one histogram into its reported figure. prev is the same
// metric over the preceding window of equal length, and may be nil.
func summarize(m stats.Metric, h, prev *stats.Histogram, q float64) metricSummary {
	good, needs, _ := stats.Thresholds(m)

	out := metricSummary{
		Metric:           m,
		Samples:          h.Count(),
		Good:             good,
		NeedsImprovement: needs,
		Unit:             unitOf(m),
		Band:             "",
	}
	if v, ok := h.Quantile(q); ok {
		out.Value = &v
		out.Band = stats.BandOf(m, v).String()
	}
	if prev != nil {
		if v, ok := prev.Quantile(q); ok {
			out.Previous = &v
			out.PreviousSamples = prev.Count()
		}
	}
	return out
}

// previous aggregates the window immediately before the requested one, of the
// same length, so the dashboard can say whether a figure moved rather than only
// what it is. It returns the histograms, the range covered, and the page views
// in it.
func (a *API) previous(q query) (map[stats.Metric]*stats.Histogram, store.Range, int) {
	span := q.Range.To.Sub(q.Range.From)
	rng := store.Range{From: q.Range.From.Add(-span), To: q.Range.From}

	hists := make(map[stats.Metric]*stats.Histogram, len(stats.Metrics))
	for _, m := range stats.Metrics {
		hists[m] = stats.New(stats.LayoutOf(m))
	}

	total := 0
	if span > 0 {
		a.each(q, rng, func(rec store.Record) bool {
			total++
			for m, v := range rec.Values {
				if h, ok := hists[m]; ok {
					h.Add(v)
				}
			}
			return true
		})
	}
	return hists, rng, total
}

// unitOf returns the display unit for a metric. CLS is a unitless score.
func unitOf(m stats.Metric) string {
	if m == stats.CLS {
		return ""
	}
	return "ms"
}

// coverage reports the store's overall extent and its disk footprint. A disk
// read that fails leaves the byte fields at zero rather than failing the whole
// response: the figures are diagnostics, not the measurement.
func (a *API) coverage() *coverageSummary {
	c := &coverageSummary{Total: a.store.Count(), RetentionDays: a.retention.Hours() / 24}
	if oldest, newest, ok := a.store.Span(); ok {
		c.Oldest, c.Newest = &oldest, &newest
	}
	if u, err := a.store.Usage(); err == nil {
		c.Bytes, c.Files, c.BytesPerRecord = u.Bytes, u.Files, u.BytesPerRecord()
	}
	return c
}

// each scans the records a query selects. Naming a route uses the store's route
// index, so the other routes are not walked at all.
func (a *API) each(q query, rng store.Range, fn func(store.Record) bool) {
	if q.Route != "" {
		a.store.EachRoute(q.Route, rng, fn)
		return
	}
	a.store.Each(rng, fn)
}

// seriesBucket is one time bucket of a series.
type seriesBucket struct {
	// At is the start of the bucket.
	At      time.Time `json:"t"`
	Value   *float64  `json:"value"`
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

	a.each(q, rng, func(rec store.Record) bool {
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
		if v, ok := h.Quantile(q.Percentile); ok {
			b.Value = &v
			b.Band = stats.BandOf(m, v).String()
		}
		resp.Buckets[i] = b
	}

	writeJSON(w, resp)
}

// GroupRow is one row of a breakdown table: one route or one device class.
type GroupRow struct {
	Key     string   `json:"key"`
	Value   *float64 `json:"value"`
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
	Rows             []GroupRow   `json:"rows"`
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

	a.each(q, q.Range, func(rec store.Record) bool {
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
		Rows:             make([]GroupRow, 0, len(groups)),
	}
	for k, h := range groups {
		row := GroupRow{Key: k, Samples: h.Count()}
		if v, ok := h.Quantile(q.Percentile); ok {
			row.Value = &v
			row.Band = stats.BandOf(m, v).String()
		}
		resp.Rows = append(resp.Rows, row)
	}

	// Worst first; ties by key so the order is stable between requests.
	sort.Slice(resp.Rows, func(i, j int) bool {
		a, b := resp.Rows[i], resp.Rows[j]
		switch {
		case a.Value == nil && b.Value == nil:
			return a.Key < b.Key
		case a.Value == nil:
			return false
		case b.Value == nil:
			return true
		case *a.Value != *b.Value:
			return *a.Value > *b.Value
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
