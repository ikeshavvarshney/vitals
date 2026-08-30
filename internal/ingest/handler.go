package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"vitals/internal/store"
)

// sessionLen is the number of hex characters kept from the session digest.
// Eight characters is enough to tell visitors apart within a single day and
// short enough that it carries no useful information out of that day.
const sessionLen = 8

// Appender is the subset of [store.Store] that ingest needs. Taking an
// interface here keeps the handler testable without touching the disk.
type Appender interface {
	Append(store.Record) error
}

// Counters records what the endpoint has done since startup. The dashboard
// shows these, because a rejection count that nobody can see is a rejection
// count that nobody fixes.
type Counters struct {
	// Accepted is the number of payloads stored.
	Accepted uint64 `json:"accepted"`
	// Malformed is the number rejected as unparseable.
	Malformed uint64 `json:"malformed"`
	// TooLarge is the number rejected for exceeding MaxBodyBytes.
	TooLarge uint64 `json:"tooLarge"`
	// StoreErrors is the number that parsed but could not be persisted.
	StoreErrors uint64 `json:"storeErrors"`
}

// Handler accepts beacon payloads at POST /v1/collect.
type Handler struct {
	store Appender
	// now is the clock, replaceable in tests.
	now func() time.Time

	accepted    atomic.Uint64
	malformed   atomic.Uint64
	tooLarge    atomic.Uint64
	storeErrors atomic.Uint64
}

// NewHandler returns a Handler that appends accepted payloads to s.
func NewHandler(s Appender) *Handler {
	return &Handler{store: s, now: func() time.Time { return time.Now().UTC() }}
}

// Counters returns a snapshot of the endpoint's counters.
func (h *Handler) Counters() Counters {
	return Counters{
		Accepted:    h.accepted.Load(),
		Malformed:   h.malformed.Load(),
		TooLarge:    h.tooLarge.Load(),
		StoreErrors: h.storeErrors.Load(),
	}
}

// ServeHTTP implements the collection endpoint.
//
// Every outcome that is not a protocol error answers 204. A beacon cannot act
// on an error and will not retry usefully, and an endpoint that returns 4xx to
// sendBeacon just fills the visitor's console with noise while still losing the
// sample. Rejections are counted instead of reported.
//
// No CORS headers are set, and none are needed: sendBeacon with the default
// text/plain content type is a simple request, so the browser sends it
// cross-origin without a preflight and never needs to read the response.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read one byte past the limit so an oversized body is detected without
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

	// A payload carrying no usable metric is not worth a record.
	if len(payload.Values) == 0 {
		h.malformed.Add(1)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	now := h.now()
	rec := store.Record{
		// The server's clock is authoritative. The client timestamp is parsed
		// and validated but never used for storage, because a visitor with a
		// wrong clock would otherwise scatter records across the timeline.
		At:      now,
		Route:   payload.Route,
		Session: SessionID(clientIP(r), r.UserAgent(), now),
		Width:   payload.Width,
		Values:  payload.Values,
	}

	if err := h.store.Append(rec); err != nil {
		h.storeErrors.Add(1)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.accepted.Add(1)
	w.WriteHeader(http.StatusNoContent)
}

// SessionID derives a coarse visitor identifier from the request's origin and
// the current UTC date.
//
// This is the privacy design, not an implementation detail. There is no cookie
// and no persistent identifier. The date is part of the input, so the value
// rotates at midnight UTC and the same visitor is unlinkable across days. The
// digest is truncated to 8 hex characters, which is enough to distinguish
// visitors within one day and too short to be useful for anything else. The IP
// address itself is never stored.
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
// Forwarded headers are deliberately ignored. They are trivially spoofed by the
// client, and trusting them without knowing the proxy topology would let anyone
// fabricate distinct sessions. Behind a reverse proxy this means every visitor
// shares one session id, which is a real limitation and is documented as one.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
