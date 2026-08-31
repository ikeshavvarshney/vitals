// Package server assembles the whole tool: the measurement store, the
// collection endpoint, the beacon, the demo site, the dashboard, and the JSON
// API, on one handler.
//
// It is the module's only exported surface. Everything it wires together lives
// under internal/, so the routing a test exercises is the routing the binary
// serves rather than a second copy assembled for the occasion, and the
// packages behind it stay free to change.
package server

import (
	"fmt"
	"net/http"
	"time"

	"vitals/src/internal/beacon"
	"vitals/src/internal/dash"
	"vitals/src/internal/demo"
	"vitals/src/internal/ingest"
	"vitals/src/internal/store"
)

// Options configure a server. The zero value is the default: keep everything,
// forever.
type Options struct {
	// Retention is how long a day log is kept. Zero keeps every day. A
	// measurement is never deleted individually: whole days expire together,
	// because a partially rewritten log is a corrupt log.
	Retention time.Duration
	// Logf receives operational notes, such as how many day logs a prune
	// removed. Nil discards them.
	Logf func(format string, args ...any)
}

// Server is a running instance's state: the record store and the handler over
// it. Close it to flush buffered records.
type Server struct {
	store   *store.Store
	api     *dash.API
	handler http.Handler
	skipped int
	opts    Options
	stop    chan struct{}
}

// Open reads any existing measurements in dataDir, creating it if needed, and
// returns a server ready to serve. The caller must Close it, which flushes the
// write buffer; killing the process instead loses up to
// [vitals/src/internal/store.FlushInterval] of records.
func Open(dataDir string) (*Server, error) {
	return OpenWith(dataDir, Options{})
}

// OpenWith is [Open] with explicit options.
func OpenWith(dataDir string, opts Options) (*Server, error) {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}

	db, skipped, err := store.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}

	handler, api, err := routes(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	api.SetRetention(opts.Retention)

	s := &Server{
		store:   db,
		api:     api,
		handler: handler,
		skipped: skipped,
		opts:    opts,
		stop:    make(chan struct{}),
	}

	if opts.Retention > 0 {
		s.prune()
		go s.pruneLoop()
	}
	return s, nil
}

// pruneInterval is how often retention is enforced while running. Expiry is
// day-granular, so checking more often than hourly would only burn a directory
// read.
const pruneInterval = time.Hour

// pruneLoop enforces retention until the server is closed.
func (s *Server) pruneLoop() {
	t := time.NewTicker(pruneInterval)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			s.prune()
		case <-s.stop:
			return
		}
	}
}

// prune drops day logs older than the retention window.
func (s *Server) prune() {
	files, records, err := s.store.Prune(time.Now().UTC().Add(-s.opts.Retention))
	if err != nil {
		s.opts.Logf("retention: %v", err)
		return
	}
	if files > 0 {
		s.opts.Logf("retention: removed %d day log(s), %d record(s) older than %s",
			files, records, s.opts.Retention)
	}
}

// Handler returns the HTTP handler serving every route.
func (s *Server) Handler() http.Handler { return s.handler }

// Records reports how many measurements are held.
func (s *Server) Records() int { return s.store.Count() }

// Skipped reports how many unreadable lines were passed over while replaying
// the data directory. A non-zero count means a previous process died
// mid-write, and is worth surfacing rather than swallowing.
func (s *Server) Skipped() int { return s.skipped }

// ReportOptions selects the window a report covers.
type ReportOptions = dash.ReportOptions

// Report is the full measurement document for a window.
type Report = dash.Report

// Journey is one visitor's sequence of page views, as carried by a [Report].
type Journey = dash.Journey

// Step is one page view inside a [Journey].
type Step = dash.Step

// Report builds the same document GET /api/report returns, without going
// through HTTP. The terminal report uses it.
func (s *Server) Report(opts ReportOptions) (Report, error) { return s.api.BuildReport(opts) }

// Usage reports what the store occupies on disk.
func (s *Server) Usage() (store.Usage, error) { return s.store.Usage() }

// Close stops retention, flushes buffered records, and releases the data files.
func (s *Server) Close() error {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	return s.store.Close()
}

// routes builds the complete HTTP handler. Dashboard, demo site, beacon,
// collection endpoint, and JSON API all share one mux on one port, which is what
// lets the whole tool be one command with no configuration.
func routes(db *store.Store) (http.Handler, *dash.API, error) {
	mux := http.NewServeMux()

	collector := ingest.NewHandler(db)
	mux.Handle("/v1/collect", collector)

	api := dash.NewAPI(db, collector.Counters)
	api.Register(mux)

	// A recorded measurement notifies every connected dashboard. The collector
	// knows nothing about the dashboard; it calls a function it was handed.
	events := api.Events()
	collector.OnRecord(func(rec store.Record) {
		events.Publish(dash.Event{Route: rec.Route, At: rec.At})
	})

	beaconHandler, err := beacon.Handler()
	if err != nil {
		return nil, nil, fmt.Errorf("preparing beacon: %w", err)
	}
	mux.Handle("GET "+beacon.Path, beaconHandler)
	mux.Handle("GET /beacon.src.js", beaconHandler)
	mux.Handle("GET "+beacon.FullPath, beaconHandler)
	mux.Handle("GET /beacon.full.src.js", beaconHandler)

	demoHandler, err := demo.Handler()
	if err != nil {
		return nil, nil, fmt.Errorf("preparing demo site: %w", err)
	}
	mux.Handle("GET "+demo.Prefix, demoHandler)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintln(w, "ok")
	})

	// The dashboard owns the root pattern, so it goes last.
	//
	// No method on the pattern: "GET /" is more specific in method but more
	// general in path than "/v1/collect", which Go's mux rejects as ambiguous.
	// The file server answers non-GET with 405 itself.
	dashHandler, err := dash.Assets()
	if err != nil {
		return nil, nil, fmt.Errorf("preparing dashboard: %w", err)
	}
	mux.Handle("/", dashHandler)

	return mux, api, nil
}

// BeaconPath is where the minified beacon is served, exported so an embedder
// does not have to hard-code it.
const BeaconPath = beacon.Path

// BeaconMaxBytes is the size budget the beacon is held to.
const BeaconMaxBytes = beacon.MaxBytes

// BeaconBytes is the minified beacon's size in bytes.
func BeaconBytes() int { return beacon.Size() }

// FullBeaconPath is where the full-parity beacon is served.
const FullBeaconPath = beacon.FullPath

// FullBeaconMaxBytes is the size budget the full beacon is held to.
const FullBeaconMaxBytes = beacon.MaxFullBytes

// FullBeaconBytes is the minified full beacon's size in bytes.
func FullBeaconBytes() int { return beacon.FullSize() }
