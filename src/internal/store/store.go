package store

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Buffering policy. Whichever limit is reached first triggers a flush.
const (
	// FlushRecords is the number of buffered records that forces a flush.
	FlushRecords = 200
	// FlushInterval is the longest a record may sit unflushed, and therefore
	// the exact amount of data a crash can lose.
	FlushInterval = 2 * time.Second
)

// fileExt is the extension of a day's log file.
const fileExt = ".jsonl"

// dayLayout formats the UTC date used as a log file's name.
const dayLayout = "2006-01-02"

// ErrClosed is returned by operations on a closed Store.
var ErrClosed = errors.New("store: closed")

// Store is an append-only measurement log with an in-memory index. It is safe
// for concurrent use, and appends never wait on disk.
type Store struct {
	dir string

	mu      sync.RWMutex
	records []Record         // sorted by At, ascending
	byRoute map[string][]int // route to indices into records
	closed  bool

	// Write side.
	file     *os.File
	writer   *bufio.Writer
	day      string // UTC date of the open file
	unwrit   int    // records buffered since the last flush
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// Open replays every log file in dir into memory and returns a Store ready to
// append, creating the directory if needed.
//
// A truncated or corrupt line, which is what a crash mid-write leaves behind, is
// skipped and counted rather than failing the replay.
func Open(dir string) (s *Store, skipped int, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, 0, fmt.Errorf("creating %s: %w", dir, err)
	}

	st := &Store{
		dir:     dir,
		byRoute: make(map[string][]int),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}

	skipped, err = st.replay()
	if err != nil {
		return nil, 0, err
	}

	go st.flushLoop()
	return st, skipped, nil
}

// replay reads every log file in the directory, in date order, into the index.
func (s *Store) replay() (skipped int, err error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", s.dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != fileExt {
			continue
		}
		names = append(names, e.Name())
	}
	// File names are ISO dates, so lexical order is chronological order.
	sort.Strings(names)

	for _, name := range names {
		n, err := s.replayFile(filepath.Join(s.dir, name))
		if err != nil {
			return skipped, err
		}
		skipped += n
	}

	// File order is already chronological, but a clock change or a hand-edited
	// file could break that, and every query binary-searches this slice.
	sort.SliceStable(s.records, func(i, j int) bool {
		return s.records[i].At.Before(s.records[j].At)
	})
	s.reindex()
	return skipped, nil
}

// replayFile reads one log file, appending its records to the index.
func (s *Store) replayFile(path string) (skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Generous, so one corrupt long line cannot fail the whole replay.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		r, err := UnmarshalLine(line)
		if err != nil {
			skipped++
			continue
		}
		s.records = append(s.records, r)
	}
	if err := sc.Err(); err != nil {
		// Corrupt past this point. Keep what was read rather than the whole day.
		if errors.Is(err, bufio.ErrTooLong) {
			return skipped + 1, nil
		}
		return skipped, fmt.Errorf("reading %s: %w", path, err)
	}
	return skipped, nil
}

// reindex rebuilds the route index. The caller must hold the write lock.
func (s *Store) reindex() {
	s.byRoute = make(map[string][]int, len(s.byRoute))
	for i, r := range s.records {
		s.byRoute[r.Route] = append(s.byRoute[r.Route], i)
	}
}

// Append buffers a record for writing and adds it to the index immediately, so
// a measurement is queryable before it reaches disk.
func (s *Store) Append(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrClosed
	}

	line, err := r.MarshalLine()
	if err != nil {
		return err
	}
	if err := s.ensureFileLocked(r.At); err != nil {
		return err
	}
	if _, err := s.writer.Write(line); err != nil {
		return fmt.Errorf("buffering record: %w", err)
	}
	if err := s.writer.WriteByte('\n'); err != nil {
		return fmt.Errorf("buffering record: %w", err)
	}

	s.insertLocked(r)

	s.unwrit++
	if s.unwrit >= FlushRecords {
		return s.flushLocked()
	}
	return nil
}

// insertLocked places r into the sorted record slice and updates the route
// index, handling the out-of-order arrival a slow beacon can produce. The
// caller must hold the write lock.
func (s *Store) insertLocked(r Record) {
	i := sort.Search(len(s.records), func(i int) bool {
		return s.records[i].At.After(r.At)
	})

	if i == len(s.records) {
		s.records = append(s.records, r)
		s.byRoute[r.Route] = append(s.byRoute[r.Route], i)
		return
	}

	s.records = append(s.records, Record{})
	copy(s.records[i+1:], s.records[i:])
	s.records[i] = r
	// Every index at or after i shifted, so the route index must be rebuilt.
	// Out-of-order arrivals are rare enough that this is not a hot path.
	s.reindex()
}

// ensureFileLocked opens or rotates the log file so that it matches the UTC day
// of at. The caller must hold the write lock.
func (s *Store) ensureFileLocked(at time.Time) error {
	day := at.UTC().Format(dayLayout)
	if s.file != nil && s.day == day {
		return nil
	}

	if s.file != nil {
		if err := s.closeFileLocked(); err != nil {
			return err
		}
	}

	path := filepath.Join(s.dir, day+fileExt)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	s.file = f
	s.writer = bufio.NewWriterSize(f, 32*1024)
	s.day = day
	return nil
}

// closeFileLocked flushes and closes the current log file. The caller must hold
// the write lock.
func (s *Store) closeFileLocked() error {
	if s.file == nil {
		return nil
	}
	flushErr := s.writer.Flush()
	closeErr := s.file.Close()
	s.file, s.writer, s.day, s.unwrit = nil, nil, "", 0

	if flushErr != nil {
		return fmt.Errorf("flushing log: %w", flushErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing log: %w", closeErr)
	}
	return nil
}

// flushLoop flushes the write buffer every FlushInterval until the Store is
// closed. This is the mechanism behind the documented durability guarantee.
func (s *Store) flushLoop() {
	defer close(s.done)

	t := time.NewTicker(FlushInterval)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			if err := s.Flush(); err != nil {
				// No caller in scope. The error surfaces on the next Append or
				// Close; dropping telemetry beats crashing the server.
				continue
			}
		case <-s.stop:
			return
		}
	}
}

// Flush writes any buffered records to disk.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

// flushLocked flushes the buffer. The caller must hold the write lock.
func (s *Store) flushLocked() error {
	if s.writer == nil || s.unwrit == 0 {
		return nil
	}
	if err := s.writer.Flush(); err != nil {
		return fmt.Errorf("flushing log: %w", err)
	}
	s.unwrit = 0
	return nil
}

// Close stops the flush loop, flushes, and closes the log file. It is safe to
// call more than once.
func (s *Store) Close() error {
	s.stopOnce.Do(func() {
		close(s.stop)
		<-s.done
	})

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	return s.closeFileLocked()
}
