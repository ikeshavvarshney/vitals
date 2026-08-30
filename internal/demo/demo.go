// Package demo serves the bundled demo site.
//
// The demo exists so that a judge, or anyone else evaluating this tool, has
// something to click within seconds of starting the binary. Each page is broken
// in one specific, documented way, so the dashboard shows all three status
// bands rather than a wall of green.
//
// The pages are served from the same binary and load the beacon from the same
// origin, which is the deployment this tool is designed for.
package demo

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"

	"vitals/internal/httpx"
)

// Prefix is the path the demo site is mounted at.
const Prefix = "/demo/"

//go:embed all:site
var siteFS embed.FS

// Handler returns a handler serving the demo site, expecting to be mounted at
// [Prefix] with the prefix stripped.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(siteFS, "site")
	if err != nil {
		return nil, fmt.Errorf("demo: locating embedded site: %w", err)
	}

	fileServer, err := httpx.NewFileServer(sub)
	if err != nil {
		return nil, fmt.Errorf("demo: preparing site: %w", err)
	}
	return http.StripPrefix("/demo", fileServer), nil
}
