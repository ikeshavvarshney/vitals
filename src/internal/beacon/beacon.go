// Package beacon serves the client-side collection scripts.
//
// Two beacons are served, not one. The default is the smallest thing that
// collects Core Web Vitals correctly for a page load; the full build costs
// about 1.7KB more and closes the accuracy gaps the small one documents. A site
// picks one by which script tag it writes, and neither is fetched from a CDN.
//
// Each has a readable source and a hand-minified twin, because this project has
// no minifier to run. [MaxBytes] and [MaxFullBytes] are enforced by a test, so
// the central claim cannot quietly stop being true.
package beacon

import (
	_ "embed"
	"net/http"
	"strconv"

	"vitals/src/internal/httpx"
)

// MaxBytes is the hard budget for the raw minified default beacon, and the
// number the README advertises. A build that exceeds it fails.
const MaxBytes = 1024

// MaxFullBytes is the budget for the full build. It buys real INP, back-forward
// cache handling, soft navigations, prerender correction, and element
// attribution. It is deliberately a separate budget: the sub-1KB claim is about
// the script a site puts on every page by default, and raising that budget to
// fit these features would have made the claim untrue rather than optional.
const MaxFullBytes = 2816

// Path is where the default beacon is served, kept short because the URL
// appears on every instrumented page.
const Path = "/b.js"

// FullPath is where the full build is served.
const FullPath = "/b-full.js"

//go:embed beacon.min.js
var minified []byte

//go:embed beacon.src.js
var source []byte

//go:embed beacon.full.min.js
var fullMinified []byte

//go:embed beacon.full.src.js
var fullSource []byte

// Script returns the minified default beacon exactly as served.
func Script() []byte { return minified }

// Source returns the readable, commented source of the default beacon.
func Source() []byte { return source }

// Size returns the raw byte count of the served default beacon.
func Size() int { return len(minified) }

// FullScript returns the minified full beacon exactly as served.
func FullScript() []byte { return fullMinified }

// FullSource returns the readable, commented source of the full beacon.
func FullSource() []byte { return fullSource }

// FullSize returns the raw byte count of the served full beacon.
func FullSize() int { return len(fullMinified) }

// Build describes one of the served beacons, so callers can list them without
// hard-coding paths or repeating the size numbers.
type Build struct {
	// Path is the URL the script is served from.
	Path string
	// Bytes is the raw minified size.
	Bytes int
	// MaxBytes is the budget the build is held to.
	MaxBytes int
	// SourcePath is the URL of the readable source.
	SourcePath string
	// Summary is a one-line description of what the build collects.
	Summary string
}

// Builds lists every beacon this binary serves, smallest first.
func Builds() []Build {
	return []Build{
		{
			Path:       Path,
			Bytes:      len(minified),
			MaxBytes:   MaxBytes,
			SourcePath: "/beacon.src.js",
			Summary:    "page loads, approximated INP",
		},
		{
			Path:       FullPath,
			Bytes:      len(fullMinified),
			MaxBytes:   MaxFullBytes,
			SourcePath: "/beacon.full.src.js",
			Summary:    "real INP, bfcache, soft navigations, prerender, attribution",
		},
	}
}

// Handler serves the beacons like any other static asset, with a
// content-derived ETag and gzip negotiation.
//
// They are served from this binary, never a CDN: a third-party origin on every
// instrumented page is exactly the cost this tool exists to avoid.
func Handler() (http.Handler, error) {
	fs, err := httpx.NewFileServerFromMap(map[string][]byte{
		"b.js":               minified,
		"beacon.src.js":      source,
		"b-full.js":          fullMinified,
		"beacon.full.src.js": fullSource,
	})
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The readable source is advertised so anyone can check what the
		// minified script does without a build step.
		full := r.URL.Path == FullPath || r.URL.Path == "/beacon.full.src.js"

		if full {
			w.Header().Set("X-Beacon-Source", "/beacon.full.src.js")
			w.Header().Set("X-Beacon-Bytes", strconv.Itoa(len(fullMinified)))
		} else {
			w.Header().Set("X-Beacon-Source", "/beacon.src.js")
			w.Header().Set("X-Beacon-Bytes", strconv.Itoa(len(minified)))
		}
		fs.ServeHTTP(w, r)
	}), nil
}
