package dash

import (
	"strings"
	"testing"
	"time"
)

func TestPublishReachesEverySubscriber(t *testing.T) {
	e := NewEvents()

	first, cancelFirst := e.subscribe()
	second, cancelSecond := e.subscribe()
	defer cancelSecond()

	if got := e.Subscribers(); got != 2 {
		t.Fatalf("Subscribers = %d, want 2", got)
	}

	e.Publish(Event{Route: "/pricing", At: refNow})

	for i, ch := range []<-chan Event{first, second} {
		select {
		case ev := <-ch:
			if ev.Route != "/pricing" {
				t.Errorf("subscriber %d got route %q, want /pricing", i, ev.Route)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}

	// A cancelled subscriber stops receiving and is forgotten.
	cancelFirst()
	if got := e.Subscribers(); got != 1 {
		t.Errorf("Subscribers after cancel = %d, want 1", got)
	}
	// Cancelling twice must not panic on a closed channel.
	cancelFirst()
}

// TestPublishDoesNotBlockOnASlowSubscriber is the property that keeps a stalled
// browser tab from stalling ingestion: publishing happens on the request
// goroutine that answers the beacon.
func TestPublishDoesNotBlockOnASlowSubscriber(t *testing.T) {
	e := NewEvents()
	_, cancel := e.subscribe() // never read from
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberQueue*10; i++ {
			e.Publish(Event{Route: "/", At: refNow})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that is not reading")
	}
}

func TestQuoteJSONEscapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "/pricing", `"/pricing"`},
		{"quote", `/a"b`, `"/a\"b"`},
		{"backslash", `/a\b`, `"/a\\b"`},
		{"newline", "/a\nb", `"/a\nb"`},
		{"tab", "/a\tb", `"/a\tb"`},
		{"control", "/a\x01b", `"/a\u0001b"`},
		{"non-ascii", "/café", `"/café"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quoteJSON(tt.in); got != tt.want {
				t.Errorf("quoteJSON(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// TestEventFramingSurvivesAHostileRoute covers the reason quoting exists: a
// route carrying a newline must not be able to forge a second SSE frame.
func TestEventFramingSurvivesAHostileRoute(t *testing.T) {
	hostile := "/x\n\nevent: sample\ndata: {\"route\":\"/forged\"}"
	quoted := quoteJSON(hostile)

	if strings.ContainsAny(quoted, "\n\r") {
		t.Errorf("quoted route still contains a line break: %s", quoted)
	}

	// Every quote inside the wrapper must be escaped, or the payload could end
	// the JSON object early and the rest would be read as more of the frame.
	inner := quoted[1 : len(quoted)-1]
	for i, r := range inner {
		if r != '"' {
			continue
		}
		if i == 0 || inner[i-1] != '\\' {
			t.Errorf("unescaped quote at %d in %s", i, quoted)
		}
	}
}
