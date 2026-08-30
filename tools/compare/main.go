// Command compare prints the raw and gzipped size of the vitals beacon
// alongside any other script given on the command line, using one compressor
// for all of them so the numbers are comparable.
//
// It exists to keep the size claim in the README honest and reproducible.
// Measuring our file with one gzip implementation and a competitor's with
// another would produce a difference of a few percent that is an artefact of
// the tooling rather than of the code.
//
// This is not part of the build. Nothing here is fetched automatically: pass
// paths to files you have already downloaded.
//
//	go run ./tools/compare path/to/web-vitals.iife.js
package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"

	"vitals/internal/beacon"
)

func main() {
	rows := []row{{name: "vitals beacon (beacon.min.js)", body: beacon.Script()}}

	for _, path := range os.Args[1:] {
		body, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "compare: %v\n", err)
			os.Exit(1)
		}
		rows = append(rows, row{name: filepath.Base(path), body: body})
	}

	base := rows[0]
	baseGzip, err := gzipSize(base.body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compare: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%-38s %8s %8s %10s %10s\n", "file", "raw", "gzip", "vs raw", "vs gzip")
	for _, r := range rows {
		g, err := gzipSize(r.body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "compare: %v\n", err)
			os.Exit(1)
		}
		ratioRaw := fmt.Sprintf("%.1fx", float64(len(r.body))/float64(len(base.body)))
		ratioGz := fmt.Sprintf("%.1fx", float64(g)/float64(baseGzip))
		if r.name == base.name {
			ratioRaw, ratioGz = "-", "-"
		}
		fmt.Printf("%-38s %7dB %7dB %10s %10s\n", r.name, len(r.body), g, ratioRaw, ratioGz)
	}
}

// row is one file being measured.
type row struct {
	name string
	body []byte
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
