// Command vitals serves the beacon, ingests Core Web Vitals measurements,
// stores them on local disk, and serves a dashboard over the same port.
//
// One process, one data directory, no external services, and an empty
// dependency manifest.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vitals/internal/beacon"
	"vitals/internal/dash"
	"vitals/internal/demo"
	"vitals/internal/ingest"
	"vitals/internal/store"
)

// shutdownGrace is how long in-flight requests get to finish after a signal.
const shutdownGrace = 5 * time.Second

func main() {
	log.SetFlags(0)
	log.SetPrefix("vitals: ")

	addr := flag.String("addr", ":8080", "address to listen on")
	dataDir := flag.String("data", "data", "directory for measurement storage")
	flag.Parse()

	if err := run(*addr, *dataDir); err != nil {
		log.Fatal(err)
	}
}

// run wires the server together and blocks until it is asked to stop.
func run(addr, dataDir string) error {
	db, skipped, err := store.Open(dataDir)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	// Closing the store flushes whatever is still buffered, so a clean shutdown
	// loses nothing. A kill -9 loses up to store.FlushInterval.
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("closing store: %v", err)
		}
	}()

	if skipped > 0 {
		// Reported rather than hidden: skipped lines mean a previous process
		// was killed mid-write, and the operator should know.
		log.Printf("replayed with %d unreadable line(s) skipped", skipped)
	}
	log.Printf("loaded %d records from %s", db.Count(), dataDir)

	handler, err := routes(db)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Printf("dashboard  http://localhost%s/", displayAddr(addr))
		log.Printf("demo site  http://localhost%s/demo/", displayAddr(addr))
		log.Printf("beacon     %d bytes at %s", beacon.Size(), beacon.Path)

		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("serving: %w", err)
			return
		}
		errc <- nil
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errc:
		return err
	case <-stop:
		log.Print("shutting down")
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}

// routes builds the complete HTTP handler.
//
// Everything is served from one mux on one port: the dashboard at the root, the
// demo site under /demo/, the beacon at /b.js, the collection endpoint, and the
// JSON API. That is what lets the whole tool be one command with no
// configuration.
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

	// The dashboard is last because it owns the root pattern, which matches
	// anything the more specific patterns above did not.
	//
	// The pattern carries no method. A "GET /" pattern would be more specific
	// in method but more general in path than "/v1/collect", which Go's mux
	// rejects as an ambiguous pair. The file server answers non-GET requests
	// with 405 itself, so nothing is lost.
	dashHandler, err := dash.Assets()
	if err != nil {
		return nil, fmt.Errorf("preparing dashboard: %w", err)
	}
	mux.Handle("/", dashHandler)

	return mux, nil
}

// displayAddr turns a listen address into something worth printing. A bare
// ":8080" is correct but reads badly in a URL.
func displayAddr(addr string) string {
	if addr == "" {
		return ":80"
	}
	if addr[0] == ':' {
		return addr
	}
	return addr
}
