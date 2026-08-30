package dash

import (
	"net/http"
	"sort"
	"time"

	"vitals/internal/beacon"
	"vitals/internal/ingest"
	"vitals/internal/stats"
	"vitals/internal/store"
)

// breakdownLimit caps how many routes or device classes a report lists per
// metric. The report is meant to be read, or pasted into a chat window with a
// context limit, so a site with a thousand routes must not produce a thousand
// rows.
const breakdownLimit = 5

// reportQuantiles are the quantiles reported per metric, alongside the p75 the
// rest of the dashboard uses. A single percentile hides whether a bad p75 is
// every visitor being slow or a slow tail.
var reportQuantiles = []struct {
	name string
	q    float64
}{
	{"p50", 0.50},
	{"p75", 0.75},
	{"p90", 0.90},
	{"p95", 0.95},
}

// distribution is how many samples fell in each Core Web Vitals band. These
// counts are exact: they are tallied per record during the scan rather than
// read off the histogram, whose bucket edges do not line up with the
// thresholds.
type distribution struct {
	Good             uint64 `json:"good"`
	NeedsImprovement uint64 `json:"needsImprovement"`
	Poor             uint64 `json:"poor"`
}

// reportMetric is one metric's full entry in a report.
type reportMetric struct {
	Metric           stats.Metric       `json:"metric"`
	Name             string             `json:"name"`
	Unit             string             `json:"unit"`
	Samples          uint64             `json:"samples"`
	Band             string             `json:"band"`
	Quantiles        map[string]float64 `json:"quantiles"` // empty without samples
	Min              *float64           `json:"min"`
	Max              *float64           `json:"max"`
	Mean             *float64           `json:"mean"`
	Good             float64            `json:"good"`
	NeedsImprovement float64            `json:"needsImprovement"`
	Distribution     distribution       `json:"distribution"`
	// RelativeError and AbsoluteError state the worst-case error of the
	// quantiles above: a fraction of the value for millisecond metrics, an
	// absolute score for CLS.
	RelativeError float64    `json:"relativeError"`
	AbsoluteError float64    `json:"absoluteError"`
	WorstRoutes   []groupRow `json:"worstRoutes"`
	WorstDevices  []groupRow `json:"worstDevices"`
}

// reportResponse is the payload of GET /api/report: every figure the dashboard
// shows, for all five metrics at once, in one document that stands on its own.
type reportResponse struct {
	Generated   time.Time        `json:"generated"`
	From        time.Time        `json:"from"`
	To          time.Time        `json:"to"`
	WindowHours float64          `json:"windowHours"`
	Percentile  float64          `json:"headlinePercentile"`
	Samples     int              `json:"pageViews"`
	Metrics     []reportMetric   `json:"metrics"`
	Ingest      ingest.Counters  `json:"ingest"`
	Coverage    *coverageSummary `json:"coverage"`
	BeaconBytes int              `json:"beaconBytes"`
	// Caveats travel with the numbers. A report pasted somewhere else loses the
	// footnotes printed on the dashboard, and these approximations are not safe
	// to read without them.
	Caveats []string `json:"caveats"`
}

// metricNames are the human-readable names used in the report, so a reader who
// has never seen this dashboard knows what each abbreviation measures.
var metricNames = map[stats.Metric]string{
	stats.LCP:  "Largest Contentful Paint",
	stats.INP:  "Interaction to Next Paint (approximated)",
	stats.CLS:  "Cumulative Layout Shift",
	stats.FCP:  "First Contentful Paint",
	stats.TTFB: "Time to First Byte",
}

// reportCaveats are the disclosures the dashboard footer prints, restated so
// they survive being copied out of the page.
var reportCaveats = []string{
	"Percentiles are approximate. Values are counted into fixed histogram buckets rather than sorted, so a quantile carries up to 4.9% relative error for millisecond metrics and 0.0025 absolute for CLS.",
	"INP is approximated. It is the longest single event over 16ms in the page view, not the high percentile of interaction latency that real INP reports. It is pessimistic in the tail.",
	"Band counts are exact. Every other figure except sample counts is bucketed.",
	"Up to 2 seconds of samples are lost if the server is killed rather than shut down.",
	"Device class is derived from viewport width, not from the user agent, so a narrow desktop window counts as mobile.",
	"This is field data from real page views only. It is not a lab audit: there is no waterfall, no resource list, and no element attribution.",
}

// handleReport answers GET /api/report.
func (a *API) handleReport(w http.ResponseWriter, r *http.Request) {
	q, err := parseQuery(r.URL.Query(), a.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	agg := newReportAggregator()
	total := 0
	a.store.Each(q.Range, func(rec store.Record) bool {
		total++
		agg.add(rec)
		return true
	})

	writeJSON(w, reportResponse{
		Generated:   a.now(),
		From:        q.Range.From,
		To:          q.Range.To,
		WindowHours: q.Range.To.Sub(q.Range.From).Hours(),
		Percentile:  percentile,
		Samples:     total,
		Metrics:     agg.metrics(),
		Ingest:      a.counters(),
		Coverage:    a.coverage(),
		BeaconBytes: beacon.Size(),
		Caveats:     reportCaveats,
	})
}

// reportAggregator accumulates one pass over the store into every figure the
// report needs: overall histograms, per-route and per-device histograms, and
// exact band tallies.
type reportAggregator struct {
	overall map[stats.Metric]*stats.Histogram
	routes  map[stats.Metric]map[string]*stats.Histogram
	devices map[stats.Metric]map[string]*stats.Histogram
	bands   map[stats.Metric]*distribution
}

func newReportAggregator() *reportAggregator {
	agg := &reportAggregator{
		overall: make(map[stats.Metric]*stats.Histogram, len(stats.Metrics)),
		routes:  make(map[stats.Metric]map[string]*stats.Histogram, len(stats.Metrics)),
		devices: make(map[stats.Metric]map[string]*stats.Histogram, len(stats.Metrics)),
		bands:   make(map[stats.Metric]*distribution, len(stats.Metrics)),
	}
	for _, m := range stats.Metrics {
		agg.overall[m] = stats.New(stats.LayoutOf(m))
		agg.routes[m] = make(map[string]*stats.Histogram)
		agg.devices[m] = make(map[string]*stats.Histogram)
		agg.bands[m] = &distribution{}
	}
	return agg
}

// add folds one record into every aggregate.
func (agg *reportAggregator) add(rec store.Record) {
	for m, v := range rec.Values {
		h, ok := agg.overall[m]
		if !ok {
			continue // a metric this build does not know about
		}
		h.Add(v)

		switch stats.BandOf(m, v) {
		case stats.Good:
			agg.bands[m].Good++
		case stats.NeedsImprovement:
			agg.bands[m].NeedsImprovement++
		default:
			agg.bands[m].Poor++
		}

		groupFor(agg.routes[m], rec.Route, m).Add(v)
		groupFor(agg.devices[m], string(rec.Device()), m).Add(v)
	}
}

// groupFor returns the histogram for key, creating it on first use.
func groupFor(group map[string]*stats.Histogram, key string, m stats.Metric) *stats.Histogram {
	h, ok := group[key]
	if !ok {
		h = stats.New(stats.LayoutOf(m))
		group[key] = h
	}
	return h
}

// metrics renders the accumulated state as the report's metric entries.
func (agg *reportAggregator) metrics() []reportMetric {
	out := make([]reportMetric, 0, len(stats.Metrics))

	for _, m := range stats.Metrics {
		h := agg.overall[m]
		good, needs, _ := stats.Thresholds(m)

		entry := reportMetric{
			Metric:           m,
			Name:             metricNames[m],
			Unit:             unitOf(m),
			Samples:          h.Count(),
			Quantiles:        make(map[string]float64, len(reportQuantiles)),
			Good:             good,
			NeedsImprovement: needs,
			Distribution:     *agg.bands[m],
			RelativeError:    h.RelativeError(),
			AbsoluteError:    h.AbsoluteError(),
			WorstRoutes:      topRows(agg.routes[m], m),
			WorstDevices:     topRows(agg.devices[m], m),
		}

		if h.Count() > 0 {
			for _, q := range reportQuantiles {
				if v, ok := h.Quantile(q.q); ok {
					entry.Quantiles[q.name] = v
				}
			}
			if v, ok := h.Quantile(percentile); ok {
				entry.Band = stats.BandOf(m, v).String()
			}
			min, max, mean := h.Min(), h.Max(), h.Mean()
			entry.Min, entry.Max, entry.Mean = &min, &max, &mean
		}

		out = append(out, entry)
	}
	return out
}

// topRows returns the slowest groups, worst first, capped at breakdownLimit.
func topRows(group map[string]*stats.Histogram, m stats.Metric) []groupRow {
	rows := make([]groupRow, 0, len(group))
	for k, h := range group {
		row := groupRow{Key: k, Samples: h.Count()}
		if v, ok := h.Quantile(percentile); ok {
			row.P75 = &v
			row.Band = stats.BandOf(m, v).String()
		}
		rows = append(rows, row)
	}

	// Worst first; ties by key so the order is stable between requests.
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
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

	if len(rows) > breakdownLimit {
		rows = rows[:breakdownLimit]
	}
	return rows
}
