// Command beaconsize reports the raw and gzipped size of every beacon this
// binary serves and fails when one exceeds its budget.
//
// The size claim is the central argument of this project, so it is enforced by
// the build rather than asserted in a README that could drift.
package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"

	"vitals/src/internal/beacon"
)

func main() {
	scripts := map[string][]byte{
		beacon.Path:     beacon.Script(),
		beacon.FullPath: beacon.FullScript(),
	}

	over := 0
	for _, b := range beacon.Builds() {
		gzipped, err := gzipSize(scripts[b.Path])
		if err != nil {
			fmt.Fprintf(os.Stderr, "beaconsize: %s: %v\n", b.Path, err)
			os.Exit(2)
		}

		fmt.Printf("%-11s raw %4d B   gzip %4d B   budget %4d B   %s\n",
			b.Path, b.Bytes, gzipped, b.MaxBytes, b.Summary)

		if b.Bytes > b.MaxBytes {
			fmt.Fprintf(os.Stderr, "beaconsize: %s is over budget by %d bytes\n",
				b.Path, b.Bytes-b.MaxBytes)
			over++
			continue
		}
		fmt.Printf("%-11s within budget by %d B\n", "", b.MaxBytes-b.Bytes)
	}

	if over > 0 {
		os.Exit(1)
	}
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
