// Command beaconsize reports the raw and gzipped size of the minified beacon
// and fails when the raw size exceeds the budget.
//
// The size claim is the central argument of this project, so it is enforced by
// the build rather than asserted in a README that could drift.
package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"

	"vitals/internal/beacon"
)

func main() {
	raw := beacon.Size()

	gzipped, err := gzipSize(beacon.Script())
	if err != nil {
		fmt.Fprintf(os.Stderr, "beaconsize: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("beacon.min.js  raw %4d B   gzip %4d B   budget %d B\n",
		raw, gzipped, beacon.MaxBytes)

	if raw > beacon.MaxBytes {
		fmt.Fprintf(os.Stderr, "\nbeaconsize: over budget by %d bytes\n", raw-beacon.MaxBytes)
		os.Exit(1)
	}
	fmt.Printf("within budget by %d B\n", beacon.MaxBytes-raw)
}

// gzipSize returns the length of b compressed at the best level.
func gzipSize(b []byte) (int, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return 0, fmt.Errorf("creating gzip writer: %w", err)
	}
	if _, err := zw.Write(b); err != nil {
		return 0, fmt.Errorf("compressing: %w", err)
	}
	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("finishing gzip stream: %w", err)
	}
	return buf.Len(), nil
}
