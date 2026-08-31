package store

import (
	"sort"
	"time"
)

// Range is a half-open time window, [From, To). Either end may be zero to mean
// unbounded, so the zero Range matches every record.
//
// An open end is deliberately not resolved against the wall clock: that would
// make the same query return different results on consecutive calls, and would
// drop records timestamped ahead of the server.
type Range struct {
	From time.Time
	To   time.Time
}

// Bounds substituted for an open end of a Range.
var (
	beginningOfTime = time.Unix(0, 0).UTC()
	endOfTime       = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
)

// Normalize replaces the open ends of a range with concrete bounds.
func (r Range) Normalize() Range {
	if r.From.IsZero() {
		r.From = beginningOfTime
	}
	if r.To.IsZero() {
		r.To = endOfTime
	}
	return r
}

// Count returns the total number of records held.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// Span returns the timestamps of the oldest and newest records held. ok is
// false when the store is empty.
func (s *Store) Span() (oldest, newest time.Time, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.records) == 0 {
		return time.Time{}, time.Time{}, false
	}
	return s.records[0].At, s.records[len(s.records)-1].At, true
}

// Routes returns every distinct route seen, sorted.
func (s *Store) Routes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, 0, len(s.byRoute))
	for route := range s.byRoute {
		out = append(out, route)
	}
	sort.Strings(out)
	return out
}

// Each calls fn for every record in the range, in ascending time order, and
// stops early if fn returns false. The read lock is held throughout, so fn must
// not call back into the Store and should not block.
func (s *Store) Each(rng Range, fn func(Record) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lo, hi := s.boundsLocked(rng)
	for i := lo; i < hi; i++ {
		if !fn(s.records[i]) {
			return
		}
	}
}

// EachRoute is [Store.Each] restricted to one route, using the route index so
// the others are not scanned.
func (s *Store) EachRoute(route string, rng Range, fn func(Record) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx, ok := s.byRoute[route]
	if !ok {
		return
	}
	lo, hi := s.boundsLocked(rng)
	for _, i := range idx {
		if i < lo {
			continue
		}
		if i >= hi {
			return // indices ascend, so nothing later qualifies
		}
		if !fn(s.records[i]) {
			return
		}
	}
}

// boundsLocked returns the half-open index range covering rng. The caller must
// hold at least the read lock.
func (s *Store) boundsLocked(rng Range) (lo, hi int) {
	rng = rng.Normalize()

	lo = sort.Search(len(s.records), func(i int) bool {
		return !s.records[i].At.Before(rng.From)
	})
	hi = sort.Search(len(s.records), func(i int) bool {
		return !s.records[i].At.Before(rng.To)
	})
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

// Sessions returns the visitor identifiers seen in the range, most recently
// active first, capped at limit. A limit of zero or less returns nothing.
//
// The identifier is the coarse, daily-rotating value the collector derives; it
// is not linkable across days and is not a cookie. See
// [vitals/src/internal/ingest.SessionID].
func (s *Store) Sessions(rng Range, limit int) []string {
	if limit <= 0 {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	lo, hi := s.boundsLocked(rng)

	// last holds each session's newest index inside the window, which is what
	// "most recently active" orders on.
	type activity struct {
		session string
		last    int
	}
	seen := make([]activity, 0, len(s.bySession))

	for session, idx := range s.bySession {
		// Indices ascend, so the newest in range is found from the back.
		for k := len(idx) - 1; k >= 0; k-- {
			i := idx[k]
			if i >= hi {
				continue
			}
			if i < lo {
				break // nothing earlier in this list qualifies either
			}
			seen = append(seen, activity{session: session, last: i})
			break
		}
	}

	sort.Slice(seen, func(i, j int) bool {
		if seen[i].last != seen[j].last {
			return seen[i].last > seen[j].last
		}
		return seen[i].session < seen[j].session
	})

	if len(seen) > limit {
		seen = seen[:limit]
	}

	out := make([]string, 0, len(seen))
	for _, a := range seen {
		out = append(out, a.session)
	}
	return out
}

// EachSession calls fn for every record belonging to one visitor within the
// range, in ascending time order, stopping early if fn returns false. It uses
// the session index, so the other records are never scanned.
//
// The read lock is held throughout, so fn must not call back into the Store and
// should not block.
func (s *Store) EachSession(session string, rng Range, fn func(Record) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx, ok := s.bySession[session]
	if !ok {
		return
	}
	lo, hi := s.boundsLocked(rng)
	for _, i := range idx {
		if i < lo {
			continue
		}
		if i >= hi {
			return // indices ascend, so nothing later qualifies
		}
		if !fn(s.records[i]) {
			return
		}
	}
}

// SessionCount returns how many distinct visitors were seen in the range.
func (s *Store) SessionCount(rng Range) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lo, hi := s.boundsLocked(rng)

	n := 0
	for _, idx := range s.bySession {
		for k := len(idx) - 1; k >= 0; k-- {
			i := idx[k]
			if i >= hi {
				continue
			}
			if i < lo {
				break
			}
			n++
			break
		}
	}
	return n
}
