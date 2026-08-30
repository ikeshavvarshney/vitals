// Package demo serves the bundled demo site: four pages, each broken in one
// specific documented way, so the dashboard shows all three status bands within
// seconds of starting the binary.
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

// Handler returns a handler serving the demo site, to be mounted at [Prefix].
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
