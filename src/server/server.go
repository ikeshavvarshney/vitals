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

	"vitals/src/internal/beacon"
	"vitals/src/internal/dash"
	"vitals/src/internal/demo"
	"vitals/src/internal/ingest"
	"vitals/src/internal/store"
)

// Server is a running instance's state: the record store and the handler over
// it. Close it to flush buffered records.
type Server struct {
	store   *store.Store
	handler http.Handler
	skipped int
}

// Open reads any existing measurements in dataDir, creating it if needed, and
// returns a server ready to serve. The caller must Close it, which flushes the
// write buffer; killing the process instead loses up to
// [vitals/src/internal/store.FlushInterval] of records.
func Open(dataDir string) (*Server, error) {
	db, skipped, err := store.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}

	handler, err := routes(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Server{store: db, handler: handler, skipped: skipped}, nil
}

// Handler returns the HTTP handler serving every route.
func (s *Server) Handler() http.Handler { return s.handler }

// Records reports how many measurements are held.
func (s *Server) Records() int { return s.store.Count() }

// Skipped reports how many unreadable lines were passed over while replaying
// the data directory. A non-zero count means a previous process died
// mid-write, and is worth surfacing rather than swallowing.
func (s *Server) Skipped() int { return s.skipped }

// Close flushes buffered records and releases the data files.
func (s *Server) Close() error { return s.store.Close() }

// routes builds the complete HTTP handler. Dashboard, demo site, beacon,
// collection endpoint, and JSON API all share one mux on one port, which is what
// lets the whole tool be one command with no configuration.
func routes(db *store.Store) (http.Handler, error) {
	mux := http.NewServeMux()

	collector := ingest.NewHandler(db)
	mux.Handle("/v1/collect", collector)

	api := dash.NewAPI(db, collector.Counters)
	api.Register(mux)

	beaconHandler, err := beacon.Handler()
	if err != nil {
		return nil, fmt.Errorf("preparing beacon: %w", err)
	}
	mux.Handle("GET "+beacon.Path, beaconHandler)
	mux.Handle("GET /beacon.src.js", beaconHandler)

	demoHandler, err := demo.Handler()
	if err != nil {
		return nil, fmt.Errorf("preparing demo site: %w", err)
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
		return nil, fmt.Errorf("preparing dashboard: %w", err)
	}
	mux.Handle("/", dashHandler)

	return mux, nil
}

// BeaconPath is where the minified beacon is served, exported so an embedder
// does not have to hard-code it.
const BeaconPath = beacon.Path

// BeaconMaxBytes is the size budget the beacon is held to.
const BeaconMaxBytes = beacon.MaxBytes

// BeaconBytes is the minified beacon's size in bytes.
func BeaconBytes() int { return beacon.Size() }
