// Package httpx serves static assets over HTTP with content hashing,
// conditional requests, and negotiated compression.
//
// It exists in place of a web framework and its middleware stack. Everything
// here is built on net/http, compress/gzip, and crypto/sha256.
package httpx

import (
	"path"
	"strings"
)

// contentTypes maps a file extension to the media type served for it.
//
// The standard library's mime package reads the host's MIME database, which
// means the same file can be served as a different type on two machines. On
// Windows it reads the registry, where a stale entry can serve JavaScript as
// text/plain and break the page. A fixed table is small, and being wrong the
// same way everywhere is better than being wrong differently on one judge's
// laptop.
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

// defaultContentType is served for an extension not in the table. It is
// deliberately not sniffed: guessing at a type for unknown content is how a
// static server ends up serving HTML that a browser will execute.
const defaultContentType = "application/octet-stream"

// ContentType returns the media type for a file name.
func ContentType(name string) string {
	if ct, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		return ct
	}
	return defaultContentType
}

// compressible reports whether a media type benefits from gzip. Images, fonts,
// and archives are already compressed; running them through gzip spends CPU to
// make the response very slightly larger.
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
