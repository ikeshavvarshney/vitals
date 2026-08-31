package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vitals/src/internal/stats"
)

// These benchmarks exist to put numbers behind three claims the documentation
// makes: that appends do not wait on disk, that a late arrival is cheap, and
// that replaying the whole log on startup is affordable at the scale this tool
// targets. Run them with:
//
//	go test ./src/internal/store/ -bench . -benchmem -run '^$'

// benchRecord builds one measurement, varying route and session so the
// secondary indexes are exercised rather than collapsing to a single key.
func benchRecord(i int) Record {
	return Record{
		At:      baseTime.Add(time.Duration(i) * time.Millisecond),
		Route:   fmt.Sprintf("/route-%d", i%50),
		Session: fmt.Sprintf("sess%04d", i%500),
		Width:   1440,
		Values: map[stats.Metric]float64{
			stats.LCP:  1800 + float64(i%900),
			stats.CLS:  0.05,
			stats.INP:  120,
			stats.FCP:  900,
			stats.TTFB: 210,
		},
	}
}

// BenchmarkAppendInOrder is the normal path: every record is newer than the
// last, so it lands at the end of the slice.
func BenchmarkAppendInOrder(b *testing.B) {
	s := openBench(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Append(benchRecord(i)); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
}

// BenchmarkAppendOutOfOrder is the path the index fix is about.
//
// Every record is stamped before the one already stored, so every append
// inserts at the front and has to move every index. This is the pathological
// case, far worse than the microsecond-scale reordering a real collector
// produces, and it is the shape that used to make the append path quadratic.
func BenchmarkAppendOutOfOrder(b *testing.B) {
	s := openBench(b)

	// Seed so the inserts land into a populated slice rather than an empty one.
	for i := 0; i < 5000; i++ {
		if err := s.Append(benchRecord(i)); err != nil {
			b.Fatalf("seeding: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := benchRecord(i)
		// Before every seeded record, so the insert is always at the front.
		r.At = baseTime.Add(-time.Duration(i+1) * time.Millisecond)
		if err := s.Append(r); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
}

// BenchmarkEachRange measures a full scan of a populated window, which is what
// every dashboard panel does.
func BenchmarkEachRange(b *testing.B) {
	s := openBench(b)
	seed(b, s, 100000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		s.Each(Range{}, func(Record) bool {
			n++
			return true
		})
		if n == 0 {
			b.Fatal("scanned no records")
		}
	}
}

// BenchmarkEachRoute measures the same scan through the route index, which is
// the reason the index exists.
func BenchmarkEachRoute(b *testing.B) {
	s := openBench(b)
	seed(b, s, 100000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		s.EachRoute("/route-7", Range{}, func(Record) bool {
			n++
			return true
		})
		if n == 0 {
			b.Fatal("scanned no records")
		}
	}
}

// BenchmarkEachSession measures one visitor's journey, the narrowest query.
func BenchmarkEachSession(b *testing.B) {
	s := openBench(b)
	seed(b, s, 100000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		s.EachSession("sess0007", Range{}, func(Record) bool {
			n++
			return true
		})
		if n == 0 {
			b.Fatal("scanned no records")
		}
	}
}

// BenchmarkSessions measures ranking visitors by recency, which walks every
// session list rather than every record.
func BenchmarkSessions(b *testing.B) {
	s := openBench(b)
	seed(b, s, 100000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(s.Sessions(Range{}, 10)) == 0 {
			b.Fatal("no sessions")
		}
	}
}

// BenchmarkReplay measures startup: parsing every line of a day log back into
// memory and rebuilding both indexes. This is the figure that decides how large
// a data directory the tool can be pointed at.
func BenchmarkReplay(b *testing.B) {
	dir := b.TempDir()

	s, _, err := Open(dir)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	seed(b, s, 100000)
	if err := s.Close(); err != nil {
		b.Fatalf("Close: %v", err)
	}

	size := logBytes(b, dir)
	b.SetBytes(size)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		reopened, skipped, err := Open(dir)
		if err != nil {
			b.Fatalf("reopening: %v", err)
		}
		if skipped != 0 {
			b.Fatalf("replay skipped %d lines", skipped)
		}
		if reopened.Count() == 0 {
			b.Fatal("replayed no records")
		}
		if err := reopened.Close(); err != nil {
			b.Fatalf("Close: %v", err)
		}
	}
}

// BenchmarkMarshalLine and BenchmarkUnmarshalLine isolate the encoding cost,
// which is what a binary segment format would have replaced.
func BenchmarkMarshalLine(b *testing.B) {
	r := benchRecord(1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.MarshalLine(); err != nil {
			b.Fatalf("MarshalLine: %v", err)
		}
	}
}

func BenchmarkUnmarshalLine(b *testing.B) {
	line, err := benchRecord(1).MarshalLine()
	if err != nil {
		b.Fatalf("MarshalLine: %v", err)
	}

	b.SetBytes(int64(len(line)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := UnmarshalLine(line); err != nil {
			b.Fatalf("UnmarshalLine: %v", err)
		}
	}
}

// openBench returns a Store in a temporary directory, closed on cleanup.
func openBench(b *testing.B) *Store {
	b.Helper()

	s, _, err := Open(b.TempDir())
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { s.Close() })
	return s
}

// seed appends n records and flushes them to disk.
func seed(b *testing.B, s *Store, n int) {
	b.Helper()

	for i := 0; i < n; i++ {
		if err := s.Append(benchRecord(i)); err != nil {
			b.Fatalf("seeding: %v", err)
		}
	}
	if err := s.Flush(); err != nil {
		b.Fatalf("Flush: %v", err)
	}
}

// logBytes returns the total size of the day logs in dir.
func logBytes(b *testing.B, dir string) int64 {
	b.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		b.Fatalf("reading %s: %v", dir, err)
	}

	var total int64
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != fileExt {
			continue
		}
		info, err := e.Info()
		if err != nil {
			b.Fatalf("stat %s: %v", e.Name(), err)
		}
		total += info.Size()
	}
	return total
}

// BenchmarkReindex measures a full rebuild of both secondary indexes over the
// same 5,000 records BenchmarkAppendOutOfOrder inserts into.
//
// This is what the append path used to do for every late arrival, so the two
// figures together are the size of the fix rather than an assertion that there
// was one.
func BenchmarkReindex(b *testing.B) {
	s := openBench(b)
	seed(b, s, 5000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.mu.Lock()
		s.reindex()
		s.mu.Unlock()
	}
}
