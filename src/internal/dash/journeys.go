package dash

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"vitals/src/internal/stats"
	"vitals/src/internal/store"
)

// Journey limits. A journey view is meant to be read, so both ends are capped
// rather than paginated.
const (
	// defaultJourneys is how many visitors are returned when the request does
	// not ask for a number.
	defaultJourneys = 8
	// maxJourneys is the ceiling, so one request cannot ask the server to
	// assemble the whole store.
	maxJourneys = 50
	// maxSteps caps one visitor's page sequence. A visitor with more page views
	// than this has the oldest kept and the rest summarised by the count, since
	// the interesting part of a journey is where it started.
	maxSteps = 25
)

// Step is one page view inside a journey.
type Step struct {
	At    time.Time `json:"t"`
	Route string    `json:"route"`
	// Values holds the metrics reported for this page view. A metric absent
	// here was not reported, which is not the same as being fast.
	Values map[stats.Metric]float64 `json:"values"`
	// Bands rates each reported value against the published thresholds, so the
	// dashboard does not re-implement the arithmetic.
	Bands map[stats.Metric]string `json:"bands"`
	// Worst is the band of the worst-rated metric in this step, which is what
	// colours the step in the dashboard.
	Worst string `json:"worst"`
	// Nav is how this page view began, when the full beacon reported it.
	Nav string `json:"nav,omitempty"`
	// Device is the coarse device class derived from viewport width.
	Device string `json:"device"`
}

// Journey is one visitor's sequence of page views, oldest step first.
type Journey struct {
	// Session is the coarse, daily-rotating visitor identifier. It is derived
	// from the request origin and the current UTC date, is never stored in a
	// cookie, and cannot be linked to the same visitor tomorrow.
	Session string `json:"session"`
	Steps   []Step `json:"steps"`
	// PageViews is the number of page views in the window, which is larger than
	// len(Steps) when the journey was truncated.
	PageViews int `json:"pageViews"`
	// Truncated reports whether steps were dropped to fit maxSteps.
	Truncated bool `json:"truncated"`
	// DurationSeconds is the time from the first step to the last. It is zero
	// for a single-page visit, which is not the same as an instant visit.
	DurationSeconds float64 `json:"durationSeconds"`
	// Worst is the worst band reached anywhere in the journey.
	Worst string `json:"worst"`
	// Degraded reports whether the journey got worse as it went: the worst band
	// of the last step is worse than that of the first. This is the pattern a
	// per-route average cannot show.
	Degraded bool `json:"degraded"`
}

// journeysResponse is the payload of GET /api/journeys.
type journeysResponse struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Visitors is the number of distinct visitors in the window, which is
	// larger than len(Journeys) when the limit truncated the list.
	Visitors int       `json:"visitors"`
	Limit    int       `json:"limit"`
	Journeys []Journey `json:"journeys"`
	// Note travels with the payload, because a journey is the one view here
	// that looks like it identifies a person and does not.
	Note string `json:"note"`
}

// journeyNote is the disclosure attached to every journeys response.
const journeyNote = "A visitor identifier is a truncated hash of the request " +
	"origin, the user agent, and the current UTC date. It rotates at midnight " +
	"UTC, is never stored in a cookie, and cannot be linked to the same person " +
	"on another day or on another site."

// handleJourneys answers GET /api/journeys.
//
// It is the one endpoint that reads along a visitor rather than across an
// aggregate. Every other view answers "how fast is this route"; this answers
// "what did one person actually experience, in order", which is the question a
// percentile cannot.
func (a *API) handleJourneys(w http.ResponseWriter, r *http.Request) {
	q, err := parseQuery(r.URL.Query(), a.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	limit, err := parseJourneyLimit(r.URL.Query().Get("n"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, a.journeys(q, limit))
}

// parseJourneyLimit reads the n parameter, which caps how many visitors are
// returned.
func parseJourneyLimit(s string) (int, error) {
	if s == "" {
		return defaultJourneys, nil
	}

	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("n: %w", err)
	}
	if n < 1 || n > maxJourneys {
		return 0, fmt.Errorf("n: %d is outside 1..%d", n, maxJourneys)
	}
	return n, nil
}

// journeys assembles the response for an already-parsed query.
func (a *API) journeys(q query, limit int) journeysResponse {
	out := journeysResponse{
		From:     q.Range.From,
		To:       q.Range.To,
		Visitors: a.store.SessionCount(q.Range),
		Limit:    limit,
		Journeys: []Journey{},
		Note:     journeyNote,
	}

	for _, session := range a.store.Sessions(q.Range, limit) {
		if j, ok := a.journey(session, q); ok {
			out.Journeys = append(out.Journeys, j)
		}
	}
	return out
}

// journey builds one visitor's sequence. ok is false when the visitor has no
// page view carrying a route in the window, which a filtered query can produce.
func (a *API) journey(session string, q query) (Journey, bool) {
	j := Journey{Session: session, Steps: []Step{}}

	a.store.EachSession(session, q.Range, func(rec store.Record) bool {
		// A route filter applies here too, so filtering the page to one route
		// shows the journeys that passed through it rather than every journey.
		if q.Route != "" && rec.Route != q.Route {
			return true
		}
		j.PageViews++

		if len(j.Steps) < maxSteps {
			j.Steps = append(j.Steps, stepOf(rec))
		} else {
			j.Truncated = true
		}
		return true
	})

	if len(j.Steps) == 0 {
		return Journey{}, false
	}

	first, last := j.Steps[0], j.Steps[len(j.Steps)-1]
	j.DurationSeconds = last.At.Sub(first.At).Seconds()
	j.Worst = worstOf(j.Steps)
	j.Degraded = bandRank(last.Worst) > bandRank(first.Worst)

	return j, true
}

// stepOf renders one record as a journey step.
func stepOf(rec store.Record) Step {
	step := Step{
		At:     rec.At,
		Route:  rec.Route,
		Values: make(map[stats.Metric]float64, len(rec.Values)),
		Bands:  make(map[stats.Metric]string, len(rec.Values)),
		Nav:    rec.Nav,
		Device: string(rec.Device()),
		Worst:  "",
	}

	worst := -1
	for m, v := range rec.Values {
		band := stats.BandOf(m, v)
		step.Values[m] = v
		step.Bands[m] = band.String()

		if rank := bandRank(band.String()); rank > worst {
			worst = rank
			step.Worst = band.String()
		}
	}
	return step
}

// worstOf returns the worst band reached across steps.
func worstOf(steps []Step) string {
	worst := ""
	rank := -1
	for _, s := range steps {
		if r := bandRank(s.Worst); r > rank {
			rank, worst = r, s.Worst
		}
	}
	return worst
}

// bandRank orders the bands so they can be compared. An unrated step ranks
// below good rather than above poor, so a page view that reported nothing never
// makes a journey look worse than it was.
func bandRank(band string) int {
	switch band {
	case stats.Good.String():
		return 0
	case stats.NeedsImprovement.String():
		return 1
	case stats.Poor.String():
		return 2
	default:
		return -1
	}
}

// sortJourneysWorstFirst is used by the report, which has room for a handful of
// journeys and should spend them on the bad ones.
func sortJourneysWorstFirst(journeys []Journey) {
	sort.SliceStable(journeys, func(i, j int) bool {
		a, b := journeys[i], journeys[j]
		switch {
		case bandRank(a.Worst) != bandRank(b.Worst):
			return bandRank(a.Worst) > bandRank(b.Worst)
		case a.Degraded != b.Degraded:
			return a.Degraded
		default:
			return a.PageViews > b.PageViews
		}
	})
}
