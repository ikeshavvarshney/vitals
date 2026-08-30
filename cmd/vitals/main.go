// Command vitals serves the beacon, ingests Core Web Vitals measurements,
// stores them on local disk, and serves a dashboard over the same port.
//
// One process, one data directory, no external services.
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

// run starts the server and blocks until it is asked to stop, then shuts it
// down gracefully. It returns the first error that prevented clean operation.
func run(addr, dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Printf("listening on %s, data in %s", addr, dataDir)
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

// routes returns the HTTP handler for the whole server. Handlers are added here
// as each subsystem lands.
func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	return mux
}
