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
