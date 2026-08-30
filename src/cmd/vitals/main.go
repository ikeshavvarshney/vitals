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
	retain := flag.Duration("retain", 0, "delete day logs older than this, for example 720h; 0 keeps everything")
	flag.Parse()

	if err := run(*addr, *dataDir, *retain); err != nil {
		log.Fatal(err)
	}
}

// run wires the server together and blocks until it is asked to stop.
func run(addr, dataDir string, retain time.Duration) error {
	app, err := server.OpenWith(dataDir, server.Options{
		Retention: retain,
		Logf:      log.Printf,
	})
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
	if u, err := app.Usage(); err != nil {
		log.Printf("reading storage usage: %v", err)
	} else {
		log.Printf("storage   %d day log(s), %s, %.0f bytes per record",
			u.Files, formatBytes(u.Bytes), u.BytesPerRecord())
	}
	if retain > 0 {
		log.Printf("retention  day logs older than %s are removed", retain)
	}

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

// formatBytes renders a size the way the dashboard does, so the two agree.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
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
