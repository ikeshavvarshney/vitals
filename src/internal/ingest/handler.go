package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"vitals/src/internal/store"
)

// dedupeWindow is how many recent page-view identifiers are remembered. A
// duplicate arrives within seconds of the original, so this only has to outlast
// a burst: at a thousand page views a minute it covers the last minute, and
// forgetting early costs one stored duplicate rather than an error.
const dedupeWindow = 4096

// sessionLen is how much of the session digest is kept: enough to tell visitors
// apart within one day, too little to be useful beyond it.
const sessionLen = 8

// Appender is the subset of [store.Store] that ingest needs, so the handler is
// testable without touching the disk.
type Appender interface {
	Append(store.Record) error
}

// Counters records what the endpoint has done since startup. The dashboard
// shows them: an invisible rejection count is one nobody fixes.
type Counters struct {
	// Accepted is the number of payloads stored.
	Accepted uint64 `json:"accepted"`
	// Malformed is the number rejected as unparseable.
	Malformed uint64 `json:"malformed"`
	// TooLarge is the number rejected for exceeding MaxBodyBytes.
	TooLarge uint64 `json:"tooLarge"`
	// StoreErrors is the number that parsed but could not be persisted.
	StoreErrors uint64 `json:"storeErrors"`
	// Duplicate is the number dropped because a payload carrying the same
	// page-view identifier had already been stored.
	Duplicate uint64 `json:"duplicate"`
}

// Handler accepts beacon payloads at POST /v1/collect.
type Handler struct {
	store Appender
	now   func() time.Time // replaceable in tests
	// onRecord, when set, is called after a measurement is stored. It is how
	// the dashboard learns that something arrived without polling for it.
	// Handlers run on the request goroutine, so an implementation must not
	// block: the collector's answer to the beacon waits on it.
	onRecord func(store.Record)

	recent *recentIDs

	accepted    atomic.Uint64
	malformed   atomic.Uint64
	tooLarge    atomic.Uint64
	storeErrors atomic.Uint64
	duplicate   atomic.Uint64
}

// OnRecord registers a function called after each accepted measurement is
// stored. It replaces any previous one and must be set before serving.
func (h *Handler) OnRecord(fn func(store.Record)) { h.onRecord = fn }

// NewHandler returns a Handler that appends accepted payloads to s.
func NewHandler(s Appender) *Handler {
	return &Handler{
		store:  s,
		now:    func() time.Time { return time.Now().UTC() },
		recent: newRecentIDs(dedupeWindow),
	}
}

// Counters returns a snapshot of the endpoint's counters.
func (h *Handler) Counters() Counters {
	return Counters{
		Accepted:    h.accepted.Load(),
		Malformed:   h.malformed.Load(),
		TooLarge:    h.tooLarge.Load(),
		StoreErrors: h.storeErrors.Load(),
		Duplicate:   h.duplicate.Load(),
	}
}

// ServeHTTP implements the collection endpoint.
//
// Every outcome that is not a protocol error answers 204: a beacon cannot act on
// an error, so rejections are counted rather than reported.
//
// No CORS headers are set and none are needed. sendBeacon's default text/plain
// content type makes this a CORS-simple request, sent cross-origin without a
// preflight and never read by the page.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// One byte past the limit, so an oversized body is detected without
	// buffering all of it.
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil {
		h.malformed.Add(1)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	payload, err := Parse(body)
	if err != nil {
		switch {
		case errors.Is(err, ErrTooLarge):
			h.tooLarge.Add(1)
		default:
			h.malformed.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if len(payload.Values) == 0 { // nothing worth a record
		h.malformed.Add(1)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// A payload the beacon identified and already delivered is dropped rather
	// than stored twice. sendBeacon and the keepalive fetch fallback can both
	// reach the server in a rare race, and a page hidden, restored, and hidden
	// again re-sends what it already sent. A payload with no identifier is from
	// the small beacon, which does not generate one, and is always stored.
	if payload.ID != "" && !h.recent.add(payload.ID) {
		h.duplicate.Add(1)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	now := h.now()
	rec := store.Record{
		// Server clock, not the client's: a visitor with a wrong clock would
		// otherwise scatter records across the timeline.
		At:      now,
		Route:   payload.Route,
		Session: SessionID(clientIP(r), r.UserAgent(), now),
		Width:   payload.Width,
		Nav:     payload.Nav,
		Attr:    payload.Attribution,
		Values:  payload.Values,
	}

	if err := h.store.Append(rec); err != nil {
		h.storeErrors.Add(1)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.accepted.Add(1)
	w.WriteHeader(http.StatusNoContent)

	if h.onRecord != nil {
		h.onRecord(rec)
	}
}

// recentIDs is a fixed-size set of the most recently seen identifiers.
//
// A ring of keys alongside the set bounds the memory: inserting into a full
// ring evicts the oldest key. That is deliberately not an LRU. An LRU would
// keep a popular key alive forever, and every key here is used exactly twice at
// most, so recency of insertion is the only ordering that means anything.
type recentIDs struct {
	mu   sync.Mutex
	seen map[string]struct{}
	ring []string
	next int
}

// newRecentIDs returns a set holding at most size identifiers.
func newRecentIDs(size int) *recentIDs {
	return &recentIDs{
		seen: make(map[string]struct{}, size),
		ring: make([]string, size),
	}
}

// add records id and reports whether it was new. A repeat returns false.
func (r *recentIDs) add(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.seen[id]; ok {
		return false
	}

	if old := r.ring[r.next]; old != "" {
		delete(r.seen, old)
	}
	r.ring[r.next] = id
	r.next = (r.next + 1) % len(r.ring)
	r.seen[id] = struct{}{}
	return true
}

// SessionID derives a coarse visitor identifier from the request's origin and
// the current UTC date.
//
// This is the privacy design, not an implementation detail: no cookie, no
// persistent identifier, and the IP is never stored. The date is part of the
// input, so the value rotates at midnight UTC and a visitor is unlinkable
// across days.
func SessionID(ip, userAgent string, at time.Time) string {
	h := sha256.New()
	h.Write([]byte(ip))
	h.Write([]byte{0}) // separator, so "a"+"bc" and "ab"+"c" differ
	h.Write([]byte(userAgent))
	h.Write([]byte{0})
	h.Write([]byte(at.UTC().Format("2006-01-02")))

	return hex.EncodeToString(h.Sum(nil))[:sessionLen]
}

// clientIP returns the address the request came from.
//
// Forwarded headers are deliberately ignored: they are trivially spoofed, and
// trusting them would let anyone fabricate distinct sessions. Behind a reverse
// proxy this means every visitor shares one session id, a documented limitation.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
