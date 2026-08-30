// Package httpx serves static assets with content hashing, conditional
// requests, and negotiated compression, in place of a web framework and its
// middleware stack.
package httpx

import (
	"path"
	"strings"
)

// contentTypes maps a file extension to its media type.
//
// mime.TypeByExtension consults the host's MIME database, so the same binary can
// serve JavaScript as text/plain on a machine with a stale registry entry. A
// fixed table is wrong the same way everywhere, which is the property that
// matters.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".webp":  "image/webp",
	".gif":   "image/gif",
	".ico":   "image/x-icon",
	".txt":   "text/plain; charset=utf-8",
	".xml":   "application/xml",
	".woff2": "font/woff2",
}

// defaultContentType is served for an unknown extension. Content is never
// sniffed: guessing is how a static server ends up serving executable HTML.
const defaultContentType = "application/octet-stream"

// ContentType returns the media type for a file name.
func ContentType(name string) string {
	if ct, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		return ct
	}
	return defaultContentType
}

// compressible reports whether a media type benefits from gzip. Images and
// fonts are already compressed, so gzip would only make them larger.
func compressible(contentType string) bool {
	switch {
	case strings.HasPrefix(contentType, "text/"):
		return true
	case strings.HasPrefix(contentType, "application/json"),
		strings.HasPrefix(contentType, "application/xml"),
		strings.HasPrefix(contentType, "image/svg+xml"):
		return true
	default:
		return false
	}
}
