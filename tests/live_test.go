package tests

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestLiveStreamDeliversASample subscribes the way the dashboard's EventSource
// does, posts a measurement, and waits for the notification.
func TestLiveStreamDeliversASample(t *testing.T) {
	srv, _ := newServer(t)

	resp, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	// The stream opens with a comment, so a client knows it is connected before
	// anything has happened.
	select {
	case line := <-lines:
		if !strings.HasPrefix(line, ":") {
			t.Errorf("first line = %q, want an opening comment", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream sent nothing on connect")
	}

	seed(t, srv, "/live-check", 1440, map[string]float64{"lcp": 1234})

	deadline := time.After(5 * time.Second)
	var sawEvent, sawData bool
	for !sawData {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream closed before delivering the sample")
			}
			switch {
			case line == "event: sample":
				sawEvent = true
			case strings.HasPrefix(line, "data: "):
				if !sawEvent {
					t.Errorf("data line before its event line: %q", line)
				}
				if !strings.Contains(line, `"route":"/live-check"`) {
					t.Errorf("data = %q, want the route that was recorded", line)
				}
				sawData = true
			}
		case <-deadline:
			t.Fatal("no sample event arrived within 5s")
		}
	}
}

// TestLiveStreamForgetsADisconnectedClient guards the leak that would otherwise
// accumulate one goroutine and one channel per closed browser tab.
func TestLiveStreamForgetsADisconnectedClient(t *testing.T) {
	srv, _ := newServer(t)

	client := &http.Client{}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}

	// Read the opening comment so the subscription is certainly registered.
	buf := make([]byte, 32)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("reading the opening comment: %v", err)
	}
	resp.Body.Close()

	// After the client goes away, ingestion must keep working: a publisher that
	// blocked on a dead subscriber would stall the collector instead.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			seed(t, srv, "/after-disconnect", 1440, map[string]float64{"lcp": 900})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ingestion stalled after a stream client disconnected")
	}

	var summary struct {
		Samples int `json:"samples"`
	}
	getJSON(t, srv.URL+"/api/summary?from=24h", &summary)
	if summary.Samples != 20 {
		t.Errorf("samples = %d, want 20", summary.Samples)
	}
}
