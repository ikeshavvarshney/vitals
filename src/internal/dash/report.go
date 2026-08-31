package dash

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"vitals/src/internal/beacon"
	"vitals/src/internal/ingest"
	"vitals/src/internal/stats"
	"vitals/src/internal/store"
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

// Distribution is how many samples fell in each Core Web Vitals band. These
// counts are exact: they are tallied per record during the scan rather than
// read off the histogram, whose bucket edges do not line up with the
// thresholds.
type Distribution struct {
	Good             uint64 `json:"good"`
	NeedsImprovement uint64 `json:"needsImprovement"`
	Poor             uint64 `json:"poor"`
}

// ReportMetric is one metric's full entry in a [Report].
type ReportMetric struct {
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
	Distribution     Distribution       `json:"Distribution"`
	// RelativeError and AbsoluteError state the worst-case error of the
	// quantiles above: a fraction of the value for millisecond metrics, an
	// absolute score for CLS.
	RelativeError float64    `json:"relativeError"`
	AbsoluteError float64    `json:"absoluteError"`
	WorstRoutes   []GroupRow `json:"worstRoutes"`
	WorstDevices  []GroupRow `json:"worstDevices"`
	// Offenders names the elements the full beacon blamed for this metric,
	// worst first. It is empty for a metric that carries no attribution, which
	// is every metric collected by the small beacon.
	Offenders []Offender `json:"offenders"`
}

// Offender is one element blamed for a metric, with how often it was named and
// how often the page view that named it was rated poor.
//
// The selector is what the browser's element looked like at the moment the
// measurement was taken: a tag, plus an id or the first class. It is not a
// unique path, so two sibling elements that differ in nothing else are counted
// together. That is the useful behaviour for an aggregate and a real limitation
// for a page built out of one repeated component.
type Offender struct {
	Selector string `json:"selector"`
	Samples  uint64 `json:"samples"`
	Poor     uint64 `json:"poor"`
}

// NavigationCount is how many page views began a given way. Only the full
// beacon reports a navigation type, so a site running the small beacon sees an
// empty list rather than a wrong one.
type NavigationCount struct {
	Type    string `json:"type"`
	Samples uint64 `json:"samples"`
}

// Report is every figure the dashboard shows, for all five metrics at once, in
// one document that stands on its own. It is the payload of GET /api/report and
// the document the terminal report prints, so the two cannot disagree.
type Report struct {
	Generated   time.Time       `json:"generated"`
	From        time.Time       `json:"from"`
	To          time.Time       `json:"to"`
	WindowHours float64         `json:"windowHours"`
	Percentile  float64         `json:"headlinePercentile"`
	Route       string          `json:"route,omitempty"`
	Samples     int             `json:"pageViews"`
	Metrics     []ReportMetric  `json:"metrics"`
	Ingest      ingest.Counters `json:"ingest"`
	// Navigation is how the page views in this window began, most common
	// first. Empty when no record carried a navigation type.
	Navigation []NavigationCount `json:"navigation"`
	// Visitors is the number of distinct visitors in the window. A visitor
	// identifier rotates daily, so this counts visitors per day rather than
	// people over time.
	Visitors int `json:"visitors"`
	// Journeys is a handful of the worst individual visitor experiences, worst
	// first. A percentile says a route is slow; a journey says one person hit
	// three slow pages in a row, which is a different and more actionable fact.
	Journeys    []Journey        `json:"journeys"`
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
	"INP depends on which beacon reported it. The small beacon at /b.js sends the longest single event over 16ms, which is pessimistic in the tail; the full beacon at /b-full.js sends real INP, grouped by interaction. Both are stored under the same name and this window may mix them.",
	"Band counts are exact. Every other figure except sample counts is bucketed.",
	"Up to 2 seconds of samples are lost if the server is killed rather than shut down.",
	"Device class is derived from viewport width, not from the user agent, so a narrow desktop window counts as mobile.",
	"This is field data from real page views only. It is not a lab audit: there is no waterfall and no resource list.",
	"Element attribution is best-effort and only from the full beacon. A selector is a tag plus an id or first class, not a unique path, so identical sibling elements are counted as one.",
}

// handleReport answers GET /api/report.
func (a *API) handleReport(w http.ResponseWriter, r *http.Request) {
	q, err := parseQuery(r.URL.Query(), a.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, a.report(q))
}

// ReportOptions select the window a [Report] covers.
type ReportOptions struct {
	// Window is how far back to look from now. Zero means 24 hours.
	Window time.Duration
	// Percentile is the quantile to rate each metric at, as a whole
	// percentage: 50, 75, 90, or 95. Zero means 75.
	Percentile int
	// Route restricts every figure to one route, matched exactly.
	Route string
}

// BuildReport produces a report directly, for callers that are not serving
// HTTP. The terminal report uses it, so what it prints is the document the API
// would have returned.
func (a *API) BuildReport(opts ReportOptions) (Report, error) {
	values := url.Values{}
	if opts.Window > 0 {
		values.Set("from", opts.Window.String())
	}
	if opts.Percentile > 0 {
		values.Set("p", strconv.Itoa(opts.Percentile))
	}
	if opts.Route != "" {
		values.Set("route", opts.Route)
	}

	q, err := parseQuery(values, a.now())
	if err != nil {
		return Report{}, err
	}
	return a.report(q), nil
}

// report assembles the document for an already-parsed query.
func (a *API) report(q query) Report {
	agg := newReportAggregator()
	total := 0
	a.each(q, q.Range, func(rec store.Record) bool {
		total++
		agg.add(rec)
		return true
	})

	return Report{
		Generated:   a.now(),
		From:        q.Range.From,
		To:          q.Range.To,
		WindowHours: q.Range.To.Sub(q.Range.From).Hours(),
		Percentile:  q.Percentile,
		Route:       q.Route,
		Samples:     total,
		Metrics:     agg.metrics(q.Percentile),
		Ingest:      a.counters(),
		Navigation:  agg.navigation(),
		Visitors:    a.store.SessionCount(q.Range),
		Journeys:    a.worstJourneys(q),
		Coverage:    a.coverage(),
		BeaconBytes: beacon.Size(),
		Caveats:     reportCaveats,
	}
}

// reportJourneys is how many visitor journeys a report carries. The report is
// meant to be read in one screen, so it spends its space on the worst few
// rather than listing everyone.
const reportJourneys = 3

// worstJourneys returns the least pleasant visitor experiences in the window.
//
// It samples a wider set than it returns and then ranks, because the store
// orders visitors by recency and the most recent visitor is not usually the
// worst-served one.
func (a *API) worstJourneys(q query) []Journey {
	journeys := a.journeys(q, maxJourneys).Journeys
	if len(journeys) == 0 {
		return nil
	}

	sortJourneysWorstFirst(journeys)
	if len(journeys) > reportJourneys {
		journeys = journeys[:reportJourneys]
	}
	return journeys
}

// reportAggregator accumulates one pass over the store into every figure the
// report needs: overall histograms, per-route and per-device histograms, and
// exact band tallies.
type reportAggregator struct {
	overall map[stats.Metric]*stats.Histogram
	routes  map[stats.Metric]map[string]*stats.Histogram
	devices map[stats.Metric]map[string]*stats.Histogram
	bands   map[stats.Metric]*Distribution
	// blamed counts element selectors per metric. Counts, not histograms: the
	// question it answers is which element is named most often when this metric
	// is bad, not what its distribution looks like.
	blamed map[stats.Metric]map[string]*Offender
	navs   map[string]uint64
}

func newReportAggregator() *reportAggregator {
	agg := &reportAggregator{
		overall: make(map[stats.Metric]*stats.Histogram, len(stats.Metrics)),
		routes:  make(map[stats.Metric]map[string]*stats.Histogram, len(stats.Metrics)),
		devices: make(map[stats.Metric]map[string]*stats.Histogram, len(stats.Metrics)),
		bands:   make(map[stats.Metric]*Distribution, len(stats.Metrics)),
		blamed:  make(map[stats.Metric]map[string]*Offender, len(stats.Metrics)),
		navs:    make(map[string]uint64),
	}
	for _, m := range stats.Metrics {
		agg.overall[m] = stats.New(stats.LayoutOf(m))
		agg.routes[m] = make(map[string]*stats.Histogram)
		agg.devices[m] = make(map[string]*stats.Histogram)
		agg.bands[m] = &Distribution{}
		agg.blamed[m] = make(map[string]*Offender)
	}
	return agg
}

// add folds one record into every aggregate.
func (agg *reportAggregator) add(rec store.Record) {
	if rec.Nav != "" {
		agg.navs[rec.Nav]++
	}

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

		if sel := rec.Attr[m]; sel != "" {
			blame := agg.blamed[m][sel]
			if blame == nil {
				blame = &Offender{Selector: sel}
				agg.blamed[m][sel] = blame
			}
			blame.Samples++
			if stats.BandOf(m, v) == stats.Poor {
				blame.Poor++
			}
		}
	}
}

// navigation renders the navigation-type tally, most common first.
func (agg *reportAggregator) navigation() []NavigationCount {
	if len(agg.navs) == 0 {
		return nil
	}

	out := make([]NavigationCount, 0, len(agg.navs))
	for kind, n := range agg.navs {
		out = append(out, NavigationCount{Type: kind, Samples: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Samples != out[j].Samples {
			return out[i].Samples > out[j].Samples
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// topOffenders ranks blamed elements by how often they were named in a page
// view rated poor, then by how often they were named at all.
//
// Ranking on the poor count rather than the raw count is what makes the list
// worth reading: the element named on every page view is usually the hero
// image, and it is only interesting when the pages it appears on are slow.
func topOffenders(blamed map[string]*Offender) []Offender {
	if len(blamed) == 0 {
		return nil
	}

	rows := make([]Offender, 0, len(blamed))
	for _, o := range blamed {
		rows = append(rows, *o)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch {
		case a.Poor != b.Poor:
			return a.Poor > b.Poor
		case a.Samples != b.Samples:
			return a.Samples > b.Samples
		default:
			return a.Selector < b.Selector
		}
	})

	if len(rows) > breakdownLimit {
		rows = rows[:breakdownLimit]
	}
	return rows
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

// metrics renders the accumulated state as the report's metric entries. band is
// rated at the requested quantile, which is what the dashboard is showing.
func (agg *reportAggregator) metrics(q float64) []ReportMetric {
	out := make([]ReportMetric, 0, len(stats.Metrics))

	for _, m := range stats.Metrics {
		h := agg.overall[m]
		good, needs, _ := stats.Thresholds(m)

		entry := ReportMetric{
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
			WorstRoutes:      topRows(agg.routes[m], m, q),
			WorstDevices:     topRows(agg.devices[m], m, q),
			Offenders:        topOffenders(agg.blamed[m]),
		}

		if h.Count() > 0 {
			for _, rq := range reportQuantiles {
				if v, ok := h.Quantile(rq.q); ok {
					entry.Quantiles[rq.name] = v
				}
			}
			if v, ok := h.Quantile(q); ok {
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
func topRows(group map[string]*stats.Histogram, m stats.Metric, q float64) []GroupRow {
	rows := make([]GroupRow, 0, len(group))
	for k, h := range group {
		row := GroupRow{Key: k, Samples: h.Count()}
		if v, ok := h.Quantile(q); ok {
			row.Value = &v
			row.Band = stats.BandOf(m, v).String()
		}
		rows = append(rows, row)
	}

	// Worst first; ties by key so the order is stable between requests.
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
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

	if len(rows) > breakdownLimit {
		rows = rows[:breakdownLimit]
	}
	return rows
}
