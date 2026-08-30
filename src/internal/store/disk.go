package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Usage describes what the store occupies on disk. It is measured by reading
// the directory rather than by counting bytes as they are written, so it stays
// true after a restart, after a manual deletion, and after pruning.
type Usage struct {
	// Files is the number of day logs held.
	Files int
	// Bytes is their total size.
	Bytes int64
	// Records is the number of measurements in memory, which is what those
	// bytes decode to.
	Records int
	// OldestDay and NewestDay are the UTC dates of the first and last file,
	// empty when there are none.
	OldestDay string
	NewestDay string
}

// BytesPerRecord returns the average on-disk cost of one measurement, or 0
// when the store is empty. It is the honest number to quote for storage
// growth: it includes the JSON keys, not only the values.
func (u Usage) BytesPerRecord() float64 {
	if u.Records == 0 {
		return 0
	}
	return float64(u.Bytes) / float64(u.Records)
}

// Usage reports the store's disk footprint.
//
// It flushes the write buffer first. Without that the figures would lag by up
// to [FlushInterval] and a store holding records could report zero bytes, which
// reads as a bug rather than as buffering. Flushing here is cheap and makes the
// number exact at the moment it is asked for.
func (s *Store) Usage() (Usage, error) {
	if err := s.Flush(); err != nil {
		return Usage{}, err
	}

	days, err := s.dayFiles()
	if err != nil {
		return Usage{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	u := Usage{Records: len(s.records), Files: len(days)}
	for _, d := range days {
		size := d.size
		// A directory entry's size for a file that is still open is stale on
		// Windows until the handle is closed. The handle itself is not, and the
		// open day is exactly the file a live dashboard is watching grow.
		if d.day == s.day && s.file != nil {
			if info, err := s.file.Stat(); err == nil {
				size = info.Size()
			}
		}
		u.Bytes += size
	}
	if len(days) > 0 {
		u.OldestDay = days[0].day
		u.NewestDay = days[len(days)-1].day
	}
	return u, nil
}

// dayFile is one log file on disk.
type dayFile struct {
	day  string // the UTC date its name encodes
	path string
	size int64
}

// dayFiles lists the log files in chronological order. File names are ISO
// dates, so lexical order is chronological order.
func (s *Store) dayFiles() ([]dayFile, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.dir, err)
	}

	out := make([]dayFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != fileExt {
			continue
		}
		day := e.Name()[:len(e.Name())-len(fileExt)]
		if _, err := time.Parse(dayLayout, day); err != nil {
			continue // not one of ours
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", e.Name(), err)
		}
		out = append(out, dayFile{day: day, path: filepath.Join(s.dir, e.Name()), size: info.Size()})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].day < out[j].day })
	return out, nil
}

// Prune deletes whole day logs older than before and drops their records from
// memory, reporting how many files and records went.
//
// A day is kept unless every record it could hold is older than the cutoff, so
// pruning never splits a file: a half-rewritten log is a corrupt log, and the
// unit of expiry here is the day. The file currently being written is never
// removed.
func (s *Store) Prune(before time.Time) (files, records int, err error) {
	days, err := s.dayFiles()
	if err != nil {
		return 0, 0, err
	}

	cutoff := before.UTC().Format(dayLayout)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, 0, ErrClosed
	}

	for _, d := range days {
		// Only days that ended before the cutoff day. The cutoff day itself
		// still holds records newer than the cutoff instant.
		if d.day >= cutoff {
			continue
		}
		if d.day == s.day {
			continue // the open file
		}
		if err := os.Remove(d.path); err != nil {
			return files, 0, fmt.Errorf("removing %s: %w", d.path, err)
		}
		files++
	}
	if files == 0 {
		return 0, 0, nil
	}

	// Drop the matching records. The slice is sorted by time, so everything
	// before the cutoff day is a prefix of it.
	cut := 0
	for cut < len(s.records) && s.records[cut].At.UTC().Format(dayLayout) < cutoff {
		cut++
	}
	s.records = append([]Record(nil), s.records[cut:]...)
	s.reindex()

	return files, cut, nil
}
