// Command vitals serves the beacon, ingests Core Web Vitals measurements,
// stores them on local disk, and serves a dashboard over the same port.
//
// One process, one data directory, no external services, empty manifest.
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

	"vitals/src/server"
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
	app, err := server.Open(dataDir)
	if err != nil {
		return err
	}
	// A clean shutdown flushes and loses nothing; kill -9 loses up to
	// store.FlushInterval.
	defer func() {
		if err := app.Close(); err != nil {
			log.Printf("closing store: %v", err)
		}
	}()

	if app.Skipped() > 0 {
		// Skipped lines mean a previous process died mid-write. Say so.
		log.Printf("replayed with %d unreadable line(s) skipped", app.Skipped())
	}
	log.Printf("loaded %d records from %s", app.Records(), dataDir)

	srv := &http.Server{
		Addr:              addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Printf("dashboard  http://localhost%s/", displayAddr(addr))
		log.Printf("demo site  http://localhost%s/demo/", displayAddr(addr))
		log.Printf("beacon     %d bytes at %s", server.BeaconBytes(), server.BeaconPath)

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

// displayAddr turns a listen address into something printable in a URL.
func displayAddr(addr string) string {
	if addr == "" {
		return ":80"
	}
	if addr[0] == ':' {
		return addr
	}
	return addr
}
