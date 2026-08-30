package dash

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"

	"vitals/internal/httpx"
)

//go:embed assets
var assetsFS embed.FS

// Assets returns a handler serving the dashboard's HTML, CSS, and JavaScript,
// embedded so there is no asset directory to ship alongside the binary.
func Assets() (http.Handler, error) {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return nil, fmt.Errorf("dash: locating embedded assets: %w", err)
	}

	fileServer, err := httpx.NewFileServer(sub)
	if err != nil {
		return nil, fmt.Errorf("dash: preparing assets: %w", err)
	}
	return fileServer, nil
}
