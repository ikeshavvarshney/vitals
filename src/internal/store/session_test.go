package store

import (
	"fmt"
	"testing"
	"time"

	"vitals/src/internal/stats"
)

// visit returns a record for one page view by one visitor.
func visit(offset time.Duration, session, route string, lcp float64) Record {
	return Record{
		At:      baseTime.Add(offset),
		Route:   route,
		Session: session,
		Width:   1440,
		Values:  map[stats.Metric]float64{stats.LCP: lcp},
	}
}

// appendAll stores every record or fails the test.
func appendAll(t *testing.T, s *Store, records ...Record) {
	t.Helper()
	for _, r := range records {
		if err := s.Append(r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

func TestEachSessionWalksOneVisitorInOrder(t *testing.T) {
	s, _ := openTemp(t)

	appendAll(t, s,
		visit(1*time.Minute, "aaaa1111", "/", 900),
		visit(2*time.Minute, "bbbb2222", "/", 1200),
		visit(3*time.Minute, "aaaa1111", "/pricing", 1800),
		visit(4*time.Minute, "bbbb2222", "/blog", 1000),
		visit(5*time.Minute, "aaaa1111", "/checkout", 4200),
	)

	var routes []string
	s.EachSession("aaaa1111", Range{}, func(r Record) bool {
		routes = append(routes, r.Route)
		return true
	})

	want := []string{"/", "/pricing", "/checkout"}
	if len(routes) != len(want) {
		t.Fatalf("got %v, want %v", routes, want)
	}
	for i, w := range want {
		if routes[i] != w {
			t.Errorf("step %d = %q, want %q", i, routes[i], w)
		}
	}
}

func TestEachSessionUnknownVisitorIsEmpty(t *testing.T) {
	s, _ := openTemp(t)
	appendAll(t, s, visit(time.Minute, "aaaa1111", "/", 900))

	called := false
	s.EachSession("nosuchid", Range{}, func(Record) bool {
		called = true
		return true
	})
	if called {
		t.Error("fn was called for a visitor with no records")
	}
}

func TestEachSessionRespectsTimeRange(t *testing.T) {
	s, _ := openTemp(t)

	appendAll(t, s,
		visit(1*time.Minute, "aaaa1111", "/early", 900),
		visit(5*time.Minute, "aaaa1111", "/middle", 900),
		visit(9*time.Minute, "aaaa1111", "/late", 900),
	)

	var routes []string
	s.EachSession("aaaa1111", Range{
		From: baseTime.Add(3 * time.Minute),
		To:   baseTime.Add(7 * time.Minute),
	}, func(r Record) bool {
		routes = append(routes, r.Route)
		return true
	})

	if len(routes) != 1 || routes[0] != "/middle" {
		t.Errorf("got %v, want [/middle]", routes)
	}
}

func TestEachSessionStopsEarly(t *testing.T) {
	s, _ := openTemp(t)

	appendAll(t, s,
		visit(1*time.Minute, "aaaa1111", "/a", 900),
		visit(2*time.Minute, "aaaa1111", "/b", 900),
		visit(3*time.Minute, "aaaa1111", "/c", 900),
	)

	seen := 0
	s.EachSession("aaaa1111", Range{}, func(Record) bool {
		seen++
		return seen < 2
	})
	if seen != 2 {
		t.Errorf("visited %d records, want 2", seen)
	}
}

func TestSessionsOrderedByMostRecent(t *testing.T) {
	s, _ := openTemp(t)

	appendAll(t, s,
		visit(1*time.Minute, "aaaa1111", "/", 900),
		visit(2*time.Minute, "bbbb2222", "/", 900),
		visit(3*time.Minute, "cccc3333", "/", 900),
		// aaaa comes back, which makes it the most recently active.
		visit(4*time.Minute, "aaaa1111", "/pricing", 900),
	)

	got := s.Sessions(Range{}, 10)
	want := []string{"aaaa1111", "cccc3333", "bbbb2222"}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("position %d = %q, want %q", i, got[i], w)
		}
	}
}

func TestSessionsRespectsLimit(t *testing.T) {
	s, _ := openTemp(t)

	for i := 0; i < 5; i++ {
		appendAll(t, s, visit(time.Duration(i)*time.Minute,
			fmt.Sprintf("sess%04d", i), "/", 900))
	}

	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{"under the count", 2, 2},
		{"exactly the count", 5, 5},
		{"over the count", 50, 5},
		{"zero", 0, 0},
		{"negative", -1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(s.Sessions(Range{}, tt.limit)); got != tt.want {
				t.Errorf("got %d sessions, want %d", got, tt.want)
			}
		})
	}
}

func TestSessionsRespectsTimeRange(t *testing.T) {
	s, _ := openTemp(t)

	appendAll(t, s,
		visit(1*time.Minute, "aaaa1111", "/", 900),
		visit(9*time.Minute, "bbbb2222", "/", 900),
	)

	got := s.Sessions(Range{
		From: baseTime.Add(5 * time.Minute),
		To:   baseTime.Add(15 * time.Minute),
	}, 10)

	if len(got) != 1 || got[0] != "bbbb2222" {
		t.Errorf("got %v, want [bbbb2222]", got)
	}
}

func TestSessionCount(t *testing.T) {
	s, _ := openTemp(t)

	appendAll(t, s,
		visit(1*time.Minute, "aaaa1111", "/", 900),
		visit(2*time.Minute, "aaaa1111", "/pricing", 900),
		visit(3*time.Minute, "bbbb2222", "/", 900),
	)

	if got := s.SessionCount(Range{}); got != 2 {
		t.Errorf("SessionCount = %d, want 2 distinct visitors from 3 page views", got)
	}
	// From 2.5 minutes on, only the third page view is in range, so only one
	// visitor is counted even though two were seen overall.
	if got := s.SessionCount(Range{From: baseTime.Add(150 * time.Second)}); got != 1 {
		t.Errorf("SessionCount over a partial window = %d, want 1", got)
	}
	if got := s.SessionCount(Range{From: baseTime.Add(10 * time.Minute)}); got != 0 {
		t.Errorf("SessionCount over an empty window = %d, want 0", got)
	}
}

// TestRecordWithoutSessionIsNotIndexed keeps an empty identifier out of the
// index. Records replayed from a log written before sessions existed have none,
// and grouping them all under one empty visitor would invent a journey.
func TestRecordWithoutSessionIsNotIndexed(t *testing.T) {
	s, _ := openTemp(t)

	appendAll(t, s,
		visit(1*time.Minute, "", "/", 900),
		visit(2*time.Minute, "aaaa1111", "/", 900),
	)

	if got := s.Sessions(Range{}, 10); len(got) != 1 || got[0] != "aaaa1111" {
		t.Errorf("Sessions = %v, want only the identified visitor", got)
	}
	if got := s.SessionCount(Range{}); got != 1 {
		t.Errorf("SessionCount = %d, want 1", got)
	}
}

// TestSessionIndexSurvivesRestart checks the index is rebuilt from the log
// rather than only maintained in memory.
func TestSessionIndexSurvivesRestart(t *testing.T) {
	s, dir := openTemp(t)

	appendAll(t, s,
		visit(1*time.Minute, "aaaa1111", "/", 900),
		visit(2*time.Minute, "aaaa1111", "/pricing", 900),
	)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, skipped, err := Open(dir)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer reopened.Close()
	if skipped != 0 {
		t.Fatalf("replay skipped %d lines", skipped)
	}

	var routes []string
	reopened.EachSession("aaaa1111", Range{}, func(r Record) bool {
		routes = append(routes, r.Route)
		return true
	})
	if len(routes) != 2 {
		t.Fatalf("got %v after restart, want two page views", routes)
	}
}

// TestIndexesStayCorrectUnderOutOfOrderArrival is the regression test for the
// append path.
//
// The collector stamps a record with the wall clock and then takes the store
// lock separately, so concurrent page views regularly arrive in the opposite
// order to their timestamps. The insert used to rebuild both indexes from
// scratch every time that happened; it now shifts them. This asserts the
// shifted indexes still point at the right records.
func TestIndexesStayCorrectUnderOutOfOrderArrival(t *testing.T) {
	s, _ := openTemp(t)

	// Interleave three visitors across three routes, arriving in an order that
	// forces an insert into the middle almost every time.
	offsets := []int{9, 3, 7, 1, 8, 2, 6, 0, 5, 4}
	sessions := []string{"aaaa1111", "bbbb2222", "cccc3333"}
	routes := []string{"/", "/pricing", "/checkout"}

	for n, off := range offsets {
		appendAll(t, s, visit(
			time.Duration(off)*time.Minute,
			sessions[n%len(sessions)],
			routes[n%len(routes)],
			float64(1000+off),
		))
	}

	// Whatever the arrival order, every index entry must resolve to a record
	// that actually carries that route or that session, in ascending time.
	for _, route := range s.Routes() {
		var last time.Time
		count := 0
		s.EachRoute(route, Range{}, func(r Record) bool {
			if r.Route != route {
				t.Errorf("route index for %q yielded a record for %q", route, r.Route)
			}
			if !last.IsZero() && r.At.Before(last) {
				t.Errorf("route index for %q is out of time order", route)
			}
			last = r.At
			count++
			return true
		})
		if count == 0 {
			t.Errorf("route index for %q is empty", route)
		}
	}

	total := 0
	for _, session := range s.Sessions(Range{}, 100) {
		var last time.Time
		s.EachSession(session, Range{}, func(r Record) bool {
			if r.Session != session {
				t.Errorf("session index for %q yielded a record for %q", session, r.Session)
			}
			if !last.IsZero() && r.At.Before(last) {
				t.Errorf("session index for %q is out of time order", session)
			}
			last = r.At
			total++
			return true
		})
	}
	if total != len(offsets) {
		t.Errorf("session indexes cover %d records, want %d", total, len(offsets))
	}
}
