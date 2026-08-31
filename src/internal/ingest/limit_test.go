package ingest

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// fixedClock returns a clock the test advances by hand, so the refill
// arithmetic is exercised without sleeping.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestLimiter returns a limiter driven by a clock the caller controls.
func newTestLimiter(rate, burst float64) (*Limiter, *fixedClock) {
	clock := &fixedClock{now: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)}
	l := NewLimiter(rate, burst)
	l.now = clock.Now
	return l, clock
}

func TestLimiterAllowsUpToBurst(t *testing.T) {
	l, _ := newTestLimiter(5, 10)

	for i := 0; i < 10; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("request %d was refused inside the burst of 10", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Error("the eleventh request was allowed; the burst is 10")
	}
}

func TestLimiterRefillsOverTime(t *testing.T) {
	l, clock := newTestLimiter(5, 10)

	for i := 0; i < 10; i++ {
		l.Allow("1.2.3.4")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("bucket is not empty")
	}

	// Five tokens per second, so 400ms buys two.
	clock.advance(400 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if !l.Allow("1.2.3.4") {
			t.Errorf("refilled request %d was refused", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Error("a third request was allowed; only two tokens were earned")
	}
}

func TestLimiterRefillIsCappedAtBurst(t *testing.T) {
	l, clock := newTestLimiter(5, 10)

	l.Allow("1.2.3.4")
	// An hour of credit must not become an hour's worth of tokens.
	clock.advance(time.Hour)

	for i := 0; i < 10; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("request %d was refused after a long idle period", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Error("the bucket refilled past its burst")
	}
}

func TestLimiterIsPerSource(t *testing.T) {
	l, _ := newTestLimiter(1, 2)

	for i := 0; i < 2; i++ {
		l.Allow("1.2.3.4")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("the first source still has tokens")
	}
	if !l.Allow("5.6.7.8") {
		t.Error("a second source was refused because the first spent its allowance")
	}
}

func TestLimiterDisabled(t *testing.T) {
	tests := []struct {
		name        string
		rate, burst float64
	}{
		{"zero rate", 0, 10},
		{"zero burst", 5, 0},
		{"negative rate", -1, 10},
		{"both zero", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, _ := newTestLimiter(tt.rate, tt.burst)

			if l.Enabled() {
				t.Error("Enabled() is true for a limiter that should be off")
			}
			for i := 0; i < 1000; i++ {
				if !l.Allow("1.2.3.4") {
					t.Fatalf("request %d was refused by a disabled limiter", i+1)
				}
			}
			if got := l.Tracked(); got != 0 {
				t.Errorf("a disabled limiter tracked %d sources", got)
			}
		})
	}
}

func TestLimiterSweepDropsIdleSources(t *testing.T) {
	l, clock := newTestLimiter(5, 10)

	l.Allow("1.2.3.4")
	l.Allow("5.6.7.8")
	if got := l.Tracked(); got != 2 {
		t.Fatalf("tracked %d sources, want 2", got)
	}

	if dropped := l.Sweep(); dropped != 0 {
		t.Errorf("swept %d active sources, want 0", dropped)
	}

	clock.advance(sourceIdle + time.Second)
	if dropped := l.Sweep(); dropped != 2 {
		t.Errorf("swept %d idle sources, want 2", dropped)
	}
	if got := l.Tracked(); got != 0 {
		t.Errorf("tracked %d sources after the sweep, want 0", got)
	}
}

// TestLimiterBoundsItsOwnMemory is the check that the limiter cannot be turned
// into the attack it exists to prevent. A flood from many forged sources must
// not grow the table without limit.
func TestLimiterBoundsItsOwnMemory(t *testing.T) {
	l, _ := newTestLimiter(5, 10)

	for i := 0; i < maxSources+500; i++ {
		l.Allow(sourceName(i))
	}

	if got := l.Tracked(); got > maxSources {
		t.Errorf("tracked %d sources, over the cap of %d", got, maxSources)
	}
}

// sourceName builds a distinct address-shaped string.
func sourceName(i int) string {
	const digits = "0123456789"
	out := make([]byte, 0, 12)
	out = append(out, "10."...)
	for _, part := range []int{i / 65536 % 256, i / 256 % 256, i % 256} {
		if len(out) > 3 {
			out = append(out, '.')
		}
		if part >= 100 {
			out = append(out, digits[part/100])
		}
		if part >= 10 {
			out = append(out, digits[part/10%10])
		}
		out = append(out, digits[part%10])
	}
	return string(out)
}

func TestLimiterConcurrent(t *testing.T) {
	l, _ := newTestLimiter(0.0001, 50)

	const workers = 8
	const each = 50

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if l.Allow("1.2.3.4") {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	// The clock never advances, so exactly the burst can be spent, whatever
	// order the goroutines interleave in.
	if allowed != 50 {
		t.Errorf("%d requests allowed, want exactly the burst of 50", allowed)
	}
}

// TestHandlerRefusesOverTheLimit checks the endpoint answers 429 rather than
// its usual 204, and that nothing was stored.
func TestHandlerRefusesOverTheLimit(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandlerWithLimit(fs, 1, 3)

	body := `{"u":"/","m":{"lcp":1000}}`
	for i := 0; i < 3; i++ {
		if rec := post(h, body); rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: status = %d, want 204", i+1, rec.Code)
		}
	}

	rec := post(h, body)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("no Retry-After header on a refusal")
	}

	if got := len(fs.all()); got != 3 {
		t.Errorf("stored %d records, want 3", got)
	}
	c := h.Counters()
	if c.Accepted != 3 {
		t.Errorf("Accepted = %d, want 3", c.Accepted)
	}
	if c.RateLimited != 1 {
		t.Errorf("RateLimited = %d, want 1", c.RateLimited)
	}
}

// TestHandlerRateLimitRunsBeforeParsing matters for the cost of an attack: an
// oversized body from a source over its limit must be refused without being
// read, so the counters must show it as rate limited rather than as too large.
func TestHandlerRateLimitRunsBeforeParsing(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandlerWithLimit(fs, 1, 1)

	post(h, `{"u":"/","m":{"lcp":1000}}`) // spends the only token

	huge := make([]byte, MaxBodyBytes*2)
	for i := range huge {
		huge[i] = 'x'
	}
	if rec := post(h, string(huge)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}

	c := h.Counters()
	if c.RateLimited != 1 {
		t.Errorf("RateLimited = %d, want 1", c.RateLimited)
	}
	if c.TooLarge != 0 {
		t.Errorf("TooLarge = %d, want 0; the body should never have been read", c.TooLarge)
	}
}

func TestHandlerDefaultLimitIsEnabled(t *testing.T) {
	h := NewHandler(&fakeStore{})

	if !h.Limiter().Enabled() {
		t.Error("the default handler does not rate limit; the collect endpoint is unauthenticated")
	}
}
