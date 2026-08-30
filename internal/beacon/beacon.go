// Package beacon serves the client-side collection script.
//
// Two files are kept in sync by hand: beacon.src.js is the readable, commented
// version that a reviewer reads, and beacon.min.js is the minified version that
// ships. There is no minifier in this project to run, so the minification is
// manual. [MaxBytes] is enforced by a test, so the central claim of the project
// cannot quietly stop being true.
package beacon

import (
	_ "embed"
	"net/http"
	"strconv"

	"vitals/internal/httpx"
)

// MaxBytes is the hard budget for the raw minified beacon.
//
// This is the number the README advertises. A build that exceeds it fails.
const MaxBytes = 1024

// Path is where the beacon is served. It is deliberately short: the URL is on
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

// Handler serves the beacon with a content-derived ETag and gzip negotiation,
// the same way every other static asset is served.
//
// The script is served from this binary, never from a CDN. That is not a
// preference: a third-party origin on every instrumented page is exactly the
// cost this tool exists to avoid.
func Handler() (http.Handler, error) {
	fs, err := httpx.NewFileServerFromMap(map[string][]byte{
		"b.js":          minified,
		"beacon.src.js": source,
	})
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The source is offered alongside the minified script so that anyone
		// reading the page can check what it does without a build step.
		w.Header().Set("X-Beacon-Source", "/beacon.src.js")
		w.Header().Set("X-Beacon-Bytes", strconv.Itoa(len(minified)))
		fs.ServeHTTP(w, r)
	}), nil
}
