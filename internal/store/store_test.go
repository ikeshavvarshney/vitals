package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"vitals/internal/stats"
)

// baseTime is a fixed instant so tests never depend on the wall clock.
var baseTime = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

// sample builds a record offset from baseTime.
func sample(offset time.Duration, route string, lcp float64) Record {
	return Record{
		At:      baseTime.Add(offset),
		Route:   route,
		Session: "sess0001",
		Width:   1440,
		Values:  map[stats.Metric]float64{stats.LCP: lcp},
	}
}

// openTemp returns a Store in a fresh temporary directory, closed on cleanup.
func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, skipped, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("Open skipped %d lines in an empty directory", skipped)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s, dir
}

func TestOpenCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	s, _, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("data directory not created: %v", err)
	}
}

func TestAppendIsQueryableBeforeFlush(t *testing.T) {
	s, _ := openTemp(t)

	if err := s.Append(sample(0, "/", 1200)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// No Flush call: the record must already be in the index.
	if got := s.Count(); got != 1 {
		t.Errorf("Count = %d, want 1", got)
	}
}

func TestSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	s, _, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := s.Append(sample(time.Duration(i)*time.Minute, "/pricing", float64(1000+i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, skipped, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if got := reopened.Count(); got != 5 {
		t.Fatalf("Count after restart = %d, want 5", got)
	}

	var seen []float64
	reopened.Each(Range{}, func(r Record) bool {
		seen = append(seen, r.Values[stats.LCP])
		return true
	})
	for i, v := range seen {
		if want := float64(1000 + i); v != want {
			t.Errorf("record %d lcp = %v, want %v", i, v, want)
		}
	}
}

func TestReplaySkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-08-30.jsonl")

	content := "" +
		`{"t":1756555200000,"u":"/","m":{"lcp":1000}}` + "\n" +
		"not json at all\n" +
		`{"t":1756555260000,"u":"/","m":{"lcp":2000}}` + "\n" +
		"\n" + // blank line, not a corruption
		`{"t":1756555320000,"u":"/pric` // truncated by a crash, no newline

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	s, skipped, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if got := s.Count(); got != 2 {
		t.Errorf("Count = %d, want 2 good records", got)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (one garbage line, one truncated)", skipped)
	}
}

func TestReplayEmptyFileDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-08-30.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	s, skipped, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if s.Count() != 0 || skipped != 0 {
		t.Errorf("Count = %d, skipped = %d, want 0 and 0", s.Count(), skipped)
	}
}

func TestReplayIgnoresNonLogFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}

	s, skipped, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if s.Count() != 0 || skipped != 0 {
		t.Errorf("Count = %d, skipped = %d, want 0 and 0", s.Count(), skipped)
	}
}

func TestFileRotatesOnUTCDayBoundary(t *testing.T) {
	s, dir := openTemp(t)

	before := time.Date(2026, 8, 30, 23, 59, 0, 0, time.UTC)
	after := time.Date(2026, 8, 31, 0, 1, 0, 0, time.UTC)

	for _, at := range []time.Time{before, after} {
		r := sample(0, "/", 1000)
		r.At = at
		if err := s.Append(r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	for _, name := range []string{"2026-08-30.jsonl", "2026-08-31.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected log file %s: %v", name, err)
		}
	}
}

func TestOutOfOrderAppendKeepsIndexSorted(t *testing.T) {
	s, _ := openTemp(t)

	// Arrive late, early, middle.
	for _, off := range []time.Duration{10 * time.Minute, 0, 5 * time.Minute} {
		if err := s.Append(sample(off, "/", float64(off/time.Minute))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	var times []time.Time
	s.Each(Range{}, func(r Record) bool {
		times = append(times, r.At)
		return true
	})

	if len(times) != 3 {
		t.Fatalf("got %d records, want 3", len(times))
	}
	for i := 1; i < len(times); i++ {
		if times[i].Before(times[i-1]) {
			t.Errorf("records out of order at %d: %v before %v", i, times[i], times[i-1])
		}
	}
}

func TestEachRespectsTimeRange(t *testing.T) {
	s, _ := openTemp(t)

	for i := 0; i < 10; i++ {
		if err := s.Append(sample(time.Duration(i)*time.Minute, "/", float64(i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	tests := []struct {
		name string
		rng  Range
		want int
	}{
		{"everything", Range{}, 10},
		{"from the fourth minute", Range{From: baseTime.Add(3 * time.Minute)}, 7},
		{"up to the fourth minute, exclusive", Range{To: baseTime.Add(3 * time.Minute)}, 3},
		{"a window in the middle", Range{From: baseTime.Add(2 * time.Minute), To: baseTime.Add(5 * time.Minute)}, 3},
		{"a window containing nothing", Range{From: baseTime.Add(20 * time.Minute), To: baseTime.Add(30 * time.Minute)}, 0},
		{"inverted window yields nothing", Range{From: baseTime.Add(5 * time.Minute), To: baseTime.Add(time.Minute)}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := 0
			s.Each(tt.rng, func(Record) bool { n++; return true })
			if n != tt.want {
				t.Errorf("Each(%+v) visited %d records, want %d", tt.rng, n, tt.want)
			}
		})
	}
}

func TestEachStopsEarly(t *testing.T) {
	s, _ := openTemp(t)
	for i := 0; i < 10; i++ {
		if err := s.Append(sample(time.Duration(i)*time.Minute, "/", float64(i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	n := 0
	s.Each(Range{}, func(Record) bool {
		n++
		return n < 3
	})
	if n != 3 {
		t.Errorf("visited %d records after stopping, want 3", n)
	}
}

func TestEachRoute(t *testing.T) {
	s, _ := openTemp(t)

	routes := []string{"/", "/pricing", "/", "/docs", "/pricing", "/"}
	for i, route := range routes {
		if err := s.Append(sample(time.Duration(i)*time.Minute, route, float64(i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	tests := []struct {
		route string
		want  int
	}{
		{"/", 3},
		{"/pricing", 2},
		{"/docs", 1},
		{"/missing", 0},
	}

	for _, tt := range tests {
		n := 0
		s.EachRoute(tt.route, Range{}, func(Record) bool { n++; return true })
		if n != tt.want {
			t.Errorf("EachRoute(%q) visited %d, want %d", tt.route, n, tt.want)
		}
	}
}

func TestEachRouteRespectsTimeRange(t *testing.T) {
	s, _ := openTemp(t)
	for i := 0; i < 6; i++ {
		if err := s.Append(sample(time.Duration(i)*time.Minute, "/", float64(i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	n := 0
	s.EachRoute("/", Range{From: baseTime.Add(2 * time.Minute), To: baseTime.Add(4 * time.Minute)}, func(Record) bool {
		n++
		return true
	})
	if n != 2 {
		t.Errorf("visited %d records, want 2", n)
	}
}

func TestRoutes(t *testing.T) {
	s, _ := openTemp(t)
	for i, route := range []string{"/pricing", "/", "/docs", "/"} {
		if err := s.Append(sample(time.Duration(i)*time.Minute, route, 1000)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got := s.Routes()
	want := []string{"/", "/docs", "/pricing"}
	if len(got) != len(want) {
		t.Fatalf("Routes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Routes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSpan(t *testing.T) {
	s, _ := openTemp(t)

	if _, _, ok := s.Span(); ok {
		t.Error("Span() on an empty store returned ok, want false")
	}

	for i := 0; i < 3; i++ {
		if err := s.Append(sample(time.Duration(i)*time.Hour, "/", 1000)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	oldest, newest, ok := s.Span()
	if !ok {
		t.Fatal("Span() not ok")
	}
	if !oldest.Equal(baseTime) {
		t.Errorf("oldest = %v, want %v", oldest, baseTime)
	}
	if !newest.Equal(baseTime.Add(2 * time.Hour)) {
		t.Errorf("newest = %v, want %v", newest, baseTime.Add(2*time.Hour))
	}
}

func TestFlushAtRecordThreshold(t *testing.T) {
	s, dir := openTemp(t)

	for i := 0; i < FlushRecords; i++ {
		if err := s.Append(sample(time.Duration(i)*time.Second, "/", 1000)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// The threshold flush happens inside Append, so the file has content
	// without anyone calling Flush.
	info, err := os.Stat(filepath.Join(dir, baseTime.Format(dayLayout)+fileExt))
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if info.Size() == 0 {
		t.Error("log file is empty after reaching the record threshold")
	}
}

func TestAppendAfterCloseFails(t *testing.T) {
	dir := t.TempDir()
	s, _, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := s.Append(sample(0, "/", 1000)); err == nil {
		t.Error("Append after Close returned nil, want an error")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s, _, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
}

func TestConcurrentAppendAndQuery(t *testing.T) {
	s, _ := openTemp(t)

	const writers, perWriter = 8, 50
	var wg sync.WaitGroup

	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				r := sample(time.Duration(w*perWriter+i)*time.Millisecond, "/", float64(i))
				if err := s.Append(r); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}(w)
	}

	// Read concurrently. This test exists to be run under -race.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			s.Count()
			s.Each(Range{}, func(Record) bool { return true })
		}
	}()

	wg.Wait()

	if got := s.Count(); got != writers*perWriter {
		t.Errorf("Count = %d, want %d", got, writers*perWriter)
	}
}
