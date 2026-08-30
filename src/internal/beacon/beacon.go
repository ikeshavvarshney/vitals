// Package beacon serves the client-side collection script.
//
// Two files are kept in sync by hand: beacon.src.js is the readable source a
// reviewer reads, and beacon.min.js is what ships, minified by hand because this
// project has no minifier to run. [MaxBytes] is enforced by a test, so the
// central claim cannot quietly stop being true.
package beacon

import (
	_ "embed"
	"net/http"
	"strconv"

	"vitals/src/internal/httpx"
)

// MaxBytes is the hard budget for the raw minified beacon, and the number the
// README advertises. A build that exceeds it fails.
const MaxBytes = 1024

// Path is where the beacon is served, kept short because the URL appears on
// every instrumented page.
const Path = "/b.js"

//go:embed beacon.min.js
var minified []byte

//go:embed beacon.src.js
var source []byte

// Script returns the minified beacon exactly as served.
func Script() []byte { return minified }

// Source returns the readable, commented beacon source.
func Source() []byte { return source }

// Size returns the raw byte count of the served beacon.
func Size() int { return len(minified) }

// Handler serves the beacon like any other static asset, with a content-derived
// ETag and gzip negotiation.
//
// It is served from this binary, never a CDN: a third-party origin on every
// instrumented page is exactly the cost this tool exists to avoid.
func Handler() (http.Handler, error) {
	fs, err := httpx.NewFileServerFromMap(map[string][]byte{
		"b.js":          minified,
		"beacon.src.js": source,
	})
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The readable source is advertised so anyone can check what the
		// minified script does without a build step.
		w.Header().Set("X-Beacon-Source", "/beacon.src.js")
		w.Header().Set("X-Beacon-Bytes", strconv.Itoa(len(minified)))
		fs.ServeHTTP(w, r)
	}), nil
}
