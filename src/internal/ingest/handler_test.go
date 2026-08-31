package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vitals/src/internal/stats"
	"vitals/src/internal/store"
)

// fakeStore records appends in memory and can be told to fail.
type fakeStore struct {
	mu      sync.Mutex
	records []store.Record
	failing bool
}

func (f *fakeStore) Append(r store.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return store.ErrClosed
	}
	f.records = append(f.records, r)
	return nil
}

func (f *fakeStore) all() []store.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Record, len(f.records))
	copy(out, f.records)
	return out
}

// post sends a body to the handler and returns the response recorder.
func post(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/collect", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.7:54321"
	req.Header.Set("User-Agent", "TestBrowser/1.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlerAcceptsValidPayload(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)

	rec := post(h, `{"u":"/pricing","t":1756500000000,"w":1440,"m":{"lcp":1834.2,"cls":0.06}}`)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}

	got := fs.all()
	if len(got) != 1 {
		t.Fatalf("stored %d records, want 1", len(got))
	}
	if got[0].Route != "/pricing" {
		t.Errorf("Route = %q, want /pricing", got[0].Route)
	}
	if got[0].Width != 1440 {
		t.Errorf("Width = %d, want 1440", got[0].Width)
	}
	if len(got[0].Session) != sessionLen {
		t.Errorf("Session = %q, want %d characters", got[0].Session, sessionLen)
	}
	if c := h.Counters(); c.Accepted != 1 {
		t.Errorf("Accepted = %d, want 1", c.Accepted)
	}
}

func TestHandlerUsesServerClockNotClientTimestamp(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)
	fixed := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return fixed }

	// A client clock two years off must not move the record on the timeline.
	post(h, `{"u":"/","t":1000000000000,"m":{"lcp":1000}}`)

	got := fs.all()
	if len(got) != 1 {
		t.Fatalf("stored %d records, want 1", len(got))
	}
	if !got[0].At.Equal(fixed) {
		t.Errorf("At = %v, want the server clock %v", got[0].At, fixed)
	}
}

func TestHandlerRejectsQuietly(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantCount func(Counters) uint64
	}{
		{"malformed json", `not json`, func(c Counters) uint64 { return c.Malformed }},
		{"empty body", ``, func(c Counters) uint64 { return c.Malformed }},
		{"no route", `{"m":{"lcp":1000}}`, func(c Counters) uint64 { return c.Malformed }},
		{"no usable metrics", `{"u":"/","m":{}}`, func(c Counters) uint64 { return c.Malformed }},
		{"only unknown metrics", `{"u":"/","m":{"fid":88}}`, func(c Counters) uint64 { return c.Malformed }},
		{"oversized", `{"u":"/` + strings.Repeat("a", MaxBodyBytes) + `","m":{}}`, func(c Counters) uint64 { return c.TooLarge }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := &fakeStore{}
			h := NewHandler(fs)

			rec := post(h, tt.body)

			// The response is 204 regardless: a beacon cannot act on an error.
			if rec.Code != http.StatusNoContent {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if n := len(fs.all()); n != 0 {
				t.Errorf("stored %d records, want 0", n)
			}
			if got := tt.wantCount(h.Counters()); got != 1 {
				t.Errorf("counter = %d, want 1; counters are %+v", got, h.Counters())
			}
		})
	}
}

func TestHandlerRejectsNonPost(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/v1/collect", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
		if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
			t.Errorf("%s: Allow = %q, want %q", method, allow, http.MethodPost)
		}
	}
}

func TestHandlerCountsStoreFailures(t *testing.T) {
	fs := &fakeStore{failing: true}
	h := NewHandler(fs)

	rec := post(h, `{"u":"/","m":{"lcp":1000}}`)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	c := h.Counters()
	if c.StoreErrors != 1 {
		t.Errorf("StoreErrors = %d, want 1", c.StoreErrors)
	}
	if c.Accepted != 0 {
		t.Errorf("Accepted = %d, want 0", c.Accepted)
	}
}

func TestHandlerBodyLargerThanLimitIsNotFullyBuffered(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)

	// Ten times the limit. The handler must stop reading just past the cap.
	body := `{"u":"/","m":{"lcp":1}}` + strings.Repeat("x", MaxBodyBytes*10)
	rec := post(h, body)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if c := h.Counters(); c.TooLarge != 1 {
		t.Errorf("TooLarge = %d, want 1; counters are %+v", c.TooLarge, c)
	}
}

func TestHandlerStoresOnlyKnownMetrics(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)

	post(h, `{"u":"/","m":{"lcp":1200,"fid":88,"cls":0.02}}`)

	got := fs.all()
	if len(got) != 1 {
		t.Fatalf("stored %d records, want 1", len(got))
	}
	if len(got[0].Values) != 2 {
		t.Errorf("stored %v, want only lcp and cls", got[0].Values)
	}
	if got[0].Values[stats.LCP] != 1200 || got[0].Values[stats.CLS] != 0.02 {
		t.Errorf("stored %v, want lcp 1200 and cls 0.02", got[0].Values)
	}
}

func TestSessionID(t *testing.T) {
	day1 := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	day1Later := time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 8, 31, 0, 30, 0, 0, time.UTC)

	base := SessionID("203.0.113.7", "Mozilla/5.0", day1)

	t.Run("length", func(t *testing.T) {
		if len(base) != sessionLen {
			t.Errorf("length = %d, want %d", len(base), sessionLen)
		}
	})

	t.Run("stable within a day", func(t *testing.T) {
		if got := SessionID("203.0.113.7", "Mozilla/5.0", day1Later); got != base {
			t.Errorf("id changed within the same UTC day: %q then %q", base, got)
		}
	})

	t.Run("rotates at midnight UTC", func(t *testing.T) {
		if got := SessionID("203.0.113.7", "Mozilla/5.0", day2); got == base {
			t.Error("id did not rotate across the UTC day boundary")
		}
	})

	t.Run("differs by address", func(t *testing.T) {
		if got := SessionID("198.51.100.4", "Mozilla/5.0", day1); got == base {
			t.Error("id did not change with a different address")
		}
	})

	t.Run("differs by user agent", func(t *testing.T) {
		if got := SessionID("203.0.113.7", "OtherBrowser/2.0", day1); got == base {
			t.Error("id did not change with a different user agent")
		}
	})

	t.Run("field boundaries are unambiguous", func(t *testing.T) {
		// Without a separator these two inputs would hash identically.
		a := SessionID("1.2.3.4", "Xbrowser", day1)
		b := SessionID("1.2.3.4X", "browser", day1)
		if a == b {
			t.Error("concatenation collision: fields are not separated")
		}
	})
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"ipv4 with port", "203.0.113.7:54321", "", "203.0.113.7"},
		{"ipv6 with port", "[2001:db8::1]:54321", "", "2001:db8::1"},
		{"no port", "203.0.113.7", "", "203.0.113.7"},
		{"forwarded header is ignored", "203.0.113.7:54321", "198.51.100.9", "203.0.113.7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/collect", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tt.forwarded)
			}
			if got := clientIP(req); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandlerConcurrent(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			post(h, `{"u":"/","m":{"lcp":1000}}`)
		}()
	}
	wg.Wait()

	if c := h.Counters(); c.Accepted != n {
		t.Errorf("Accepted = %d, want %d", c.Accepted, n)
	}
	if got := len(fs.all()); got != n {
		t.Errorf("stored %d records, want %d", got, n)
	}
}

func TestHandlerStoresAttributionAndNavigationType(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)

	post(h, `{"u":"/checkout","w":390,"i":"abc123def","n":"soft-navigation",`+
		`"m":{"lcp":2400,"inp":312},"a":{"lcp":"img.hero","inp":"button.buy"}}`)

	got := fs.all()
	if len(got) != 1 {
		t.Fatalf("stored %d records, want 1", len(got))
	}
	if got[0].Nav != "soft-navigation" {
		t.Errorf("Nav = %q, want soft-navigation", got[0].Nav)
	}
	if got[0].Attr[stats.LCP] != "img.hero" {
		t.Errorf("Attr[lcp] = %q, want img.hero", got[0].Attr[stats.LCP])
	}
	if got[0].Attr[stats.INP] != "button.buy" {
		t.Errorf("Attr[inp] = %q, want button.buy", got[0].Attr[stats.INP])
	}
}

// TestHandlerDropsDuplicatePayload covers the race the page-view identifier
// exists for: sendBeacon and the keepalive fetch fallback both landing.
func TestHandlerDropsDuplicatePayload(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)

	body := `{"u":"/","i":"abc123def","m":{"lcp":1000}}`
	first := post(h, body)
	second := post(h, body)

	// A duplicate is not an error the beacon can act on, so it is answered the
	// same way as an accepted one.
	if first.Code != http.StatusNoContent || second.Code != http.StatusNoContent {
		t.Errorf("statuses = %d and %d, want %d twice", first.Code, second.Code, http.StatusNoContent)
	}
	if got := len(fs.all()); got != 1 {
		t.Fatalf("stored %d records, want 1", got)
	}

	c := h.Counters()
	if c.Accepted != 1 {
		t.Errorf("Accepted = %d, want 1", c.Accepted)
	}
	if c.Duplicate != 1 {
		t.Errorf("Duplicate = %d, want 1", c.Duplicate)
	}
}

func TestHandlerKeepsDistinctIdentifiers(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)

	post(h, `{"u":"/","i":"aaa111","m":{"lcp":1000}}`)
	post(h, `{"u":"/","i":"bbb222","m":{"lcp":1100}}`)

	if got := len(fs.all()); got != 2 {
		t.Errorf("stored %d records, want 2", got)
	}
	if c := h.Counters(); c.Duplicate != 0 {
		t.Errorf("Duplicate = %d, want 0", c.Duplicate)
	}
}

// TestHandlerWithoutIdentifierIsNeverDeduplicated protects the small beacon,
// which sends no identifier at all. Two identical payloads from it are two real
// page views and both must be stored.
func TestHandlerWithoutIdentifierIsNeverDeduplicated(t *testing.T) {
	fs := &fakeStore{}
	h := NewHandler(fs)

	body := `{"u":"/","m":{"lcp":1000}}`
	post(h, body)
	post(h, body)

	if got := len(fs.all()); got != 2 {
		t.Errorf("stored %d records, want 2", got)
	}
	if c := h.Counters(); c.Duplicate != 0 {
		t.Errorf("Duplicate = %d, want 0", c.Duplicate)
	}
}

func TestRecentIDsEvictsOldestFirst(t *testing.T) {
	r := newRecentIDs(2)

	for _, id := range []string{"a", "b"} {
		if !r.add(id) {
			t.Fatalf("add(%q) = false on first insert", id)
		}
	}
	if r.add("a") {
		t.Error(`add("a") = true, want false while still remembered`)
	}

	// "c" evicts "a", the oldest, and leaves "b".
	if !r.add("c") {
		t.Fatal(`add("c") = false, want true`)
	}
	if r.add("b") {
		t.Error(`add("b") = true, want false while still remembered`)
	}
	// A repeat does not insert, so the ring is unchanged and "a" is still the
	// one that was evicted.
	if !r.add("a") {
		t.Error(`add("a") = false, want true after eviction`)
	}
}

func TestRecentIDsConcurrent(t *testing.T) {
	r := newRecentIDs(64)

	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if r.add("shared") {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if accepted != 1 {
		t.Errorf("%d goroutines saw the identifier as new, want exactly 1", accepted)
	}
}
