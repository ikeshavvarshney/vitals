package dash

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"vitals/src/internal/stats"
	"vitals/src/internal/store"
)

// addVisit appends one page view by one visitor.
func addVisit(t *testing.T, s *store.Store, at time.Time, session, route string,
	values map[stats.Metric]float64) {
	t.Helper()

	err := s.Append(store.Record{
		At:      at,
		Route:   route,
		Session: session,
		Width:   1440,
		Values:  values,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// seedJourneys builds two visitors: one whose visit degrades from good to poor,
// and one who saw a single fast page.
func seedJourneys(t *testing.T) func(*store.Store) {
	t.Helper()

	return func(s *store.Store) {
		addVisit(t, s, refNow.Add(-9*time.Minute), "aaaa1111", "/",
			map[stats.Metric]float64{stats.LCP: 900})
		addVisit(t, s, refNow.Add(-8*time.Minute), "aaaa1111", "/pricing",
			map[stats.Metric]float64{stats.LCP: 3200})
		addVisit(t, s, refNow.Add(-7*time.Minute), "aaaa1111", "/checkout",
			map[stats.Metric]float64{stats.LCP: 8000, stats.CLS: 0.4})

		addVisit(t, s, refNow.Add(-2*time.Minute), "bbbb2222", "/",
			map[stats.Metric]float64{stats.LCP: 800})
	}
}

func TestJourneysReturnsStepsInOrder(t *testing.T) {
	a := newTestAPI(t, seedJourneys(t))

	var got journeysResponse
	call(t, a, "/api/journeys?from=1h", &got)

	if got.Visitors != 2 {
		t.Errorf("visitors = %d, want 2", got.Visitors)
	}
	if len(got.Journeys) != 2 {
		t.Fatalf("got %d journeys, want 2", len(got.Journeys))
	}

	// Most recently active first, so the single-page visitor leads.
	if got.Journeys[0].Session != "bbbb2222" {
		t.Errorf("first journey = %q, want bbbb2222", got.Journeys[0].Session)
	}

	degrading := got.Journeys[1]
	want := []string{"/", "/pricing", "/checkout"}
	if len(degrading.Steps) != len(want) {
		t.Fatalf("got %d steps, want %d", len(degrading.Steps), len(want))
	}
	for i, w := range want {
		if degrading.Steps[i].Route != w {
			t.Errorf("step %d = %q, want %q", i, degrading.Steps[i].Route, w)
		}
	}
}

// TestJourneyDetectsDegradation covers the field that makes this view worth
// having: a visit that started fine and ended badly.
func TestJourneyDetectsDegradation(t *testing.T) {
	a := newTestAPI(t, seedJourneys(t))

	var got journeysResponse
	call(t, a, "/api/journeys?from=1h", &got)

	for _, j := range got.Journeys {
		switch j.Session {
		case "aaaa1111":
			if !j.Degraded {
				t.Error("the visit that went from 900ms to 8000ms is not marked as degraded")
			}
			if j.Worst != stats.Poor.String() {
				t.Errorf("worst = %q, want poor", j.Worst)
			}
			if j.PageViews != 3 {
				t.Errorf("pageViews = %d, want 3", j.PageViews)
			}
			if j.DurationSeconds != 120 {
				t.Errorf("durationSeconds = %v, want 120", j.DurationSeconds)
			}
		case "bbbb2222":
			if j.Degraded {
				t.Error("a single-page visit cannot have degraded")
			}
			if j.Worst != stats.Good.String() {
				t.Errorf("worst = %q, want good", j.Worst)
			}
			if j.DurationSeconds != 0 {
				t.Errorf("durationSeconds = %v, want 0 for one page view", j.DurationSeconds)
			}
		}
	}
}

func TestJourneyStepCarriesBands(t *testing.T) {
	a := newTestAPI(t, seedJourneys(t))

	var got journeysResponse
	call(t, a, "/api/journeys?from=1h&n=50", &got)

	var last Step
	for _, j := range got.Journeys {
		if j.Session == "aaaa1111" {
			last = j.Steps[len(j.Steps)-1]
		}
	}

	if last.Bands[stats.LCP] != stats.Poor.String() {
		t.Errorf("LCP band = %q, want poor for 8000ms", last.Bands[stats.LCP])
	}
	if last.Bands[stats.CLS] != stats.Poor.String() {
		t.Errorf("CLS band = %q, want poor for 0.4", last.Bands[stats.CLS])
	}
	if last.Worst != stats.Poor.String() {
		t.Errorf("step worst = %q, want poor", last.Worst)
	}
	if last.Device != string(store.DeviceDesktop) {
		t.Errorf("device = %q, want desktop for a 1440px viewport", last.Device)
	}
}

// TestJourneyWithNoRatedMetricIsNotPoor guards the band ranking. A page view
// that reported nothing rateable must not sort above a genuinely poor one.
func TestJourneyWithNoRatedMetricIsNotPoor(t *testing.T) {
	a := newTestAPI(t, func(s *store.Store) {
		addVisit(t, s, refNow.Add(-time.Minute), "cccc3333", "/",
			map[stats.Metric]float64{})
	})

	var got journeysResponse
	call(t, a, "/api/journeys?from=1h", &got)

	// A record with no values is rejected before storage in production, but the
	// store accepts one, and the ranking must cope rather than call it poor.
	for _, j := range got.Journeys {
		if j.Worst == stats.Poor.String() {
			t.Errorf("a journey with no rated metric was rated %q", j.Worst)
		}
	}
}

func TestJourneysRespectRouteFilter(t *testing.T) {
	a := newTestAPI(t, seedJourneys(t))

	var got journeysResponse
	call(t, a, "/api/journeys?from=1h&route=/checkout", &got)

	if len(got.Journeys) != 1 {
		t.Fatalf("got %d journeys, want only the visitor who reached /checkout", len(got.Journeys))
	}
	j := got.Journeys[0]
	if j.Session != "aaaa1111" {
		t.Errorf("session = %q, want aaaa1111", j.Session)
	}
	if len(j.Steps) != 1 || j.Steps[0].Route != "/checkout" {
		t.Errorf("steps = %+v, want only /checkout", j.Steps)
	}
}

func TestJourneysLimitParameter(t *testing.T) {
	a := newTestAPI(t, seedJourneys(t))

	var got journeysResponse
	call(t, a, "/api/journeys?from=1h&n=1", &got)

	if len(got.Journeys) != 1 {
		t.Errorf("got %d journeys, want 1", len(got.Journeys))
	}
	// The visitor count is the whole window, not the truncated list.
	if got.Visitors != 2 {
		t.Errorf("visitors = %d, want 2 even though one journey was returned", got.Visitors)
	}
}

func TestJourneysRejectBadLimit(t *testing.T) {
	a := newTestAPI(t, seedJourneys(t))

	for _, target := range []string{
		"/api/journeys?n=0",
		"/api/journeys?n=-3",
		"/api/journeys?n=abc",
		"/api/journeys?n=51",
	} {
		// call only decodes a 200, so the error body is read here.
		rec := call(t, a, target, nil)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, rec.Code)
			continue
		}

		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: decoding error body: %v", target, err)
			continue
		}
		if body["error"] == "" {
			t.Errorf("%s: no error message in the body", target)
		}
		if !contains(body["error"], "n:") {
			t.Errorf("%s: error does not name the parameter: %q", target, body["error"])
		}
	}
}

func TestJourneysEmptyWindow(t *testing.T) {
	a := newTestAPI(t, seedJourneys(t))

	var got journeysResponse
	call(t, a, "/api/journeys?from=1m", &got)

	if got.Journeys == nil {
		t.Error("journeys is null; an empty window should serialise as an empty list")
	}
	if len(got.Journeys) != 0 {
		t.Errorf("got %d journeys, want none in a one-minute window", len(got.Journeys))
	}
	if got.Note == "" {
		t.Error("the privacy note is missing from an empty response")
	}
}

// TestJourneysCarryPrivacyNote keeps the disclosure attached to the data rather
// than only in the page, so it survives the response being read by anything
// else.
func TestJourneysCarryPrivacyNote(t *testing.T) {
	a := newTestAPI(t, seedJourneys(t))

	var got journeysResponse
	call(t, a, "/api/journeys?from=1h", &got)

	for _, want := range []string{"rotates at midnight", "never stored in a cookie"} {
		if !contains(got.Note, want) {
			t.Errorf("note does not mention %q: %s", want, got.Note)
		}
	}
}

// TestReportCarriesWorstJourneys checks the report picks the bad visit rather
// than the most recent one.
func TestReportCarriesWorstJourneys(t *testing.T) {
	a := newTestAPI(t, seedJourneys(t))

	rep, err := a.BuildReport(ReportOptions{Window: time.Hour})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	if rep.Visitors != 2 {
		t.Errorf("visitors = %d, want 2", rep.Visitors)
	}
	if len(rep.Journeys) == 0 {
		t.Fatal("report carries no journeys")
	}
	if rep.Journeys[0].Session != "aaaa1111" {
		t.Errorf("first reported journey = %q, want the degrading one (aaaa1111)",
			rep.Journeys[0].Session)
	}
}

func TestSortJourneysWorstFirst(t *testing.T) {
	good := Journey{Session: "good", Worst: stats.Good.String(), PageViews: 9}
	needs := Journey{Session: "needs", Worst: stats.NeedsImprovement.String(), PageViews: 1}
	poor := Journey{Session: "poor", Worst: stats.Poor.String(), PageViews: 1}
	poorDegraded := Journey{Session: "degraded", Worst: stats.Poor.String(), PageViews: 1, Degraded: true}

	journeys := []Journey{good, poor, needs, poorDegraded}
	sortJourneysWorstFirst(journeys)

	want := []string{"degraded", "poor", "needs", "good"}
	for i, w := range want {
		if journeys[i].Session != w {
			t.Errorf("position %d = %q, want %q", i, journeys[i].Session, w)
		}
	}
}

func TestBandRankOrdersBands(t *testing.T) {
	tests := []struct {
		band string
		want int
	}{
		{stats.Good.String(), 0},
		{stats.NeedsImprovement.String(), 1},
		{stats.Poor.String(), 2},
		{"", -1},
		{"nonsense", -1},
	}

	for _, tt := range tests {
		if got := bandRank(tt.band); got != tt.want {
			t.Errorf("bandRank(%q) = %d, want %d", tt.band, got, tt.want)
		}
	}
}

// contains is strings.Contains, kept local so the test file's imports stay
// about the thing under test.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
