package ingest

import (
	"sync"
	"time"
)

// Rate limit defaults.
//
// A page view produces exactly one payload, so a browser that is behaving needs
// a rate of roughly one request per page view. The burst is what makes the
// limit invisible to a real visitor: someone opening ten tabs at once, or a
// single-page app sending one payload per soft navigation while a visitor
// clicks quickly, spends burst rather than rate.
const (
	// DefaultRate is how many payloads one source may sustain per second.
	DefaultRate = 5
	// DefaultBurst is how many it may send at once before the rate applies.
	DefaultBurst = 40
	// maxSources caps how many distinct sources are tracked, so the limiter
	// cannot itself be turned into a memory exhaustion attack. Reaching it
	// evicts the least recently seen source.
	maxSources = 8192
	// sourceIdle is how long a source is kept after its last request. A source
	// that has been quiet this long has a full bucket anyway, so forgetting it
	// changes nothing except the memory it occupies.
	sourceIdle = 10 * time.Minute
)

// Limiter is a per-source token bucket.
//
// The collection endpoint is unauthenticated and writes to disk, which is the
// combination that has to be bounded: without this, anyone who finds the URL
// can fill the data directory as fast as their connection allows.
//
// The bucket is not refilled by a background goroutine. Each source records
// when it was last topped up, and the tokens owed since then are added on the
// next request, so an idle source costs nothing but its map entry and there is
// no timer per client.
type Limiter struct {
	rate  float64 // tokens added per second
	burst float64 // bucket capacity
	now   func() time.Time

	mu      sync.Mutex
	sources map[string]*bucket
}

// bucket is one source's allowance.
type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter returns a limiter allowing rate requests per second per source,
// with the given burst. A rate or burst of zero or less disables limiting, which
// is what the -rate flag set to zero asks for.
func NewLimiter(rate, burst float64) *Limiter {
	return &Limiter{
		rate:    rate,
		burst:   burst,
		now:     time.Now,
		sources: make(map[string]*bucket),
	}
}

// Enabled reports whether the limiter rejects anything. A disabled limiter is
// kept rather than made nil so the handler has no branch on the hot path.
func (l *Limiter) Enabled() bool { return l != nil && l.rate > 0 && l.burst > 0 }

// Allow reports whether a request from source may proceed, spending one token
// if so.
func (l *Limiter) Allow(source string) bool {
	if !l.Enabled() {
		return true
	}

	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.sources[source]
	if !ok {
		// A source is admitted with a full bucket minus this request, so a
		// first-time visitor is never delayed.
		if len(l.sources) >= maxSources {
			l.evictLocked(now)
		}
		l.sources[source] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}

	// Credit the tokens earned since the last request, capped at the burst.
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evictLocked makes room in a full table.
//
// It first drops every source idle longer than sourceIdle, which is the common
// case and costs one pass. If that frees nothing, because every tracked source
// is currently active, it drops the single least recently seen one so the table
// still cannot grow without bound. The caller must hold the mutex.
func (l *Limiter) evictLocked(now time.Time) {
	for source, b := range l.sources {
		if now.Sub(b.last) > sourceIdle {
			delete(l.sources, source)
		}
	}
	if len(l.sources) < maxSources {
		return
	}

	var oldest string
	var oldestAt time.Time
	for source, b := range l.sources {
		if oldest == "" || b.last.Before(oldestAt) {
			oldest, oldestAt = source, b.last
		}
	}
	if oldest != "" {
		delete(l.sources, oldest)
	}
}

// Tracked returns how many sources the limiter is holding, for the dashboard's
// counters and for tests.
func (l *Limiter) Tracked() int {
	if !l.Enabled() {
		return 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.sources)
}

// Sweep drops sources that have been idle long enough to have a full bucket.
// The server calls it periodically so a burst of one-off clients does not hold
// memory until the table fills.
func (l *Limiter) Sweep() int {
	if !l.Enabled() {
		return 0
	}

	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	before := len(l.sources)
	for source, b := range l.sources {
		if now.Sub(b.last) > sourceIdle {
			delete(l.sources, source)
		}
	}
	return before - len(l.sources)
}
