package dash

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Live update policy.
const (
	// heartbeatInterval is how often an idle stream sends a comment line. It
	// keeps an intermediary from closing a connection it believes is dead, and
	// it is how a client notices the server went away.
	heartbeatInterval = 25 * time.Second
	// subscriberQueue is how many notifications a slow client may fall behind
	// before its notifications start being dropped. Dropping is correct here:
	// a notification carries no data the client cannot re-read, so a late
	// client should skip to the present rather than replay a queue.
	subscriberQueue = 8
)

// Events broadcasts a notification whenever a measurement is recorded, so a
// dashboard can refresh itself instead of polling.
//
// The notification deliberately carries the route and nothing else. Sending the
// figures would mean maintaining a second path that computes them, which is a
// second thing to keep correct; the client re-reads the API it already knows.
type Events struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
}

// Event is one notification.
type Event struct {
	// Route is the page the measurement came from.
	Route string
	// At is when it was recorded.
	At time.Time
}

// NewEvents returns an empty broadcaster.
func NewEvents() *Events {
	return &Events{subscribers: make(map[chan Event]struct{})}
}

// Publish notifies every current subscriber. It never blocks: a subscriber
// whose queue is full misses this notification and catches up on the next one.
func (e *Events) Publish(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for ch := range e.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

// subscribe registers a new listener and returns it with its cancel function.
func (e *Events) subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberQueue)

	e.mu.Lock()
	e.subscribers[ch] = struct{}{}
	e.mu.Unlock()

	return ch, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if _, ok := e.subscribers[ch]; ok {
			delete(e.subscribers, ch)
			close(ch)
		}
	}
}

// Subscribers reports how many streams are connected.
func (e *Events) Subscribers() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.subscribers)
}

// handleEvents answers GET /api/events with a Server-Sent Events stream.
//
// SSE rather than WebSocket: the traffic is one-directional and tiny, the
// protocol is a text format over an ordinary response that net/http already
// serves, and the browser's EventSource reconnects on its own. A WebSocket
// would mean implementing a handshake, framing, and masking by hand, for a
// feature that sends one line per page view.
func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Without flushing there is no streaming, only a response that arrives
		// once at the end. Say so rather than hanging.
		writeError(w, http.StatusInternalServerError, fmt.Errorf("events: streaming unsupported"))
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Proxies that buffer responses would defeat the point of streaming.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	events, cancel := a.events.subscribe()
	defer cancel()

	// An opening comment gets headers and the first bytes to the client at
	// once, so EventSource fires onopen without waiting for a page view.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return

		case ev, ok := <-events:
			if !ok {
				return
			}
			// The route is quoted as JSON so a path containing a quote, a
			// newline, or a non-ASCII character cannot break the framing.
			fmt.Fprintf(w, "event: sample\ndata: {\"route\":%s,\"at\":%q}\n\n",
				quoteJSON(ev.Route), ev.At.UTC().Format(time.RFC3339Nano))
			flusher.Flush()

		case <-ticker.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// quoteJSON renders s as a JSON string. It is a handful of cases rather than a
// call into encoding/json because this runs per event on a hot-ish path and the
// input is a single string.
func quoteJSON(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if r < 0x20 {
				out = append(out, []byte(fmt.Sprintf(`\u%04x`, r))...)
				continue
			}
			out = append(out, []byte(string(r))...)
		}
	}
	return string(append(out, '"'))
}
