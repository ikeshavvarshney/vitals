package httpx

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// gzipMinBytes is the size below which compression is not attempted. Below
// roughly this size the gzip header and trailer cost more than the compression
// saves, and the response is a single packet either way.
const gzipMinBytes = 512

// CacheControl values. HTML is revalidated on every view so a deploy is visible
// immediately; other assets are cached but still carry an ETag, so a
// revalidation costs one 304 rather than a full transfer.
const (
	cacheHTML  = "no-cache"
	cacheAsset = "public, max-age=3600"
)

// asset is one file, prepared once at construction.
type asset struct {
	name         string
	contentType  string
	cacheControl string
	body         []byte
	gzipped      []byte // nil when compression does not help
	etag         string
}

// FileServer serves a fixed set of files from an [fs.FS].
//
// Every file is read, hashed, and compressed once when the server is built, not
// per request. The asset set is embedded in the binary and never changes at
// runtime, so there is nothing to invalidate and no reason to touch the disk
// again. This is what makes the handler allocation-free on the hot path.
type FileServer struct {
	assets map[string]*asset
	// index is the file served for a directory request.
	index string
}

// NewFileServer prepares a handler serving every file in fsys.
//
// Paths are served relative to the root of fsys, so a file at "dash/app.js"
// is served at "/dash/app.js" unless the caller strips a prefix first.
func NewFileServer(fsys fs.FS) (*FileServer, error) {
	s := &FileServer{
		assets: make(map[string]*asset),
		index:  "index.html",
	}

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walking assets at %s: %w", p, err)
		}
		if d.IsDir() {
			return nil
		}

		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("reading asset %s: %w", p, err)
		}
		s.assets["/"+p] = newAsset(p, body)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(s.assets) == 0 {
		return nil, fmt.Errorf("httpx: no assets found")
	}
	return s, nil
}

// newAsset prepares one file: media type, cache policy, entity tag, and a gzip
// encoding when that is smaller than the original.
func newAsset(name string, body []byte) *asset {
	ct := ContentType(name)

	cache := cacheAsset
	if strings.HasPrefix(ct, "text/html") {
		cache = cacheHTML
	}

	a := &asset{
		name:         name,
		contentType:  ct,
		cacheControl: cache,
		body:         body,
		etag:         etagOf(body),
	}

	if len(body) >= gzipMinBytes && compressible(ct) {
		if gz, err := gzipBytes(body); err == nil && len(gz) < len(body) {
			a.gzipped = gz
		}
	}
	return a
}

// etagOf returns a strong entity tag derived from the content itself.
//
// Content hashing rather than modification time is what makes this correct for
// embedded assets: a rebuilt binary has no meaningful mtime, and two builds of
// identical content should produce identical tags. The digest is truncated to
// 128 bits, which is far beyond what a cache validator needs.
func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`
}

// gzipBytes compresses b at the best compression level. Assets are compressed
// once at startup, so the slowest level costs nothing per request.
func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("creating gzip writer: %w", err)
	}
	if _, err := zw.Write(b); err != nil {
		return nil, fmt.Errorf("compressing asset: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finishing gzip stream: %w", err)
	}
	return buf.Bytes(), nil
}

// Has reports whether the server holds a file at the given request path.
func (s *FileServer) Has(urlPath string) bool {
	_, ok := s.assets[cleanPath(urlPath)]
	return ok
}

// Len returns the number of files served.
func (s *FileServer) Len() int { return len(s.assets) }

// ServeHTTP serves one asset.
func (s *FileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	a, ok := s.lookup(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.write(w, r, a)
}

// lookup resolves a request path to an asset, falling back to the directory
// index. It returns false when nothing matches.
func (s *FileServer) lookup(urlPath string) (*asset, bool) {
	p := cleanPath(urlPath)

	if a, ok := s.assets[p]; ok {
		return a, true
	}
	// A directory request serves its index file, with or without the slash.
	if strings.HasSuffix(p, "/") {
		if a, ok := s.assets[p+s.index]; ok {
			return a, true
		}
	} else if a, ok := s.assets[p+"/"+s.index]; ok {
		return a, true
	}
	return nil, false
}

// ServeFile serves one named asset, bypassing directory-index resolution. It
// reports false when no such asset exists, leaving the response untouched so
// the caller can decide what to do.
func (s *FileServer) ServeFile(w http.ResponseWriter, r *http.Request, urlPath string) bool {
	a, ok := s.assets[cleanPath(urlPath)]
	if !ok {
		return false
	}
	s.write(w, r, a)
	return true
}

// write sends an asset, honouring conditional requests and encoding
// negotiation.
func (s *FileServer) write(w http.ResponseWriter, r *http.Request, a *asset) {
	h := w.Header()
	h.Set("Content-Type", a.contentType)
	h.Set("Cache-Control", a.cacheControl)
	h.Set("ETag", a.etag)

	// The response body depends on Accept-Encoding, so any cache between here
	// and the browser must key on it. Omitting this is how a proxy ends up
	// serving a gzip stream to a client that did not ask for one.
	if a.gzipped != nil {
		h.Set("Vary", "Accept-Encoding")
	}

	if matchesETag(r.Header.Get("If-None-Match"), a.etag) {
		// A 304 carries no body and must not carry Content-Length.
		h.Del("Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body := a.body
	if a.gzipped != nil && acceptsGzip(r.Header.Get("Accept-Encoding")) {
		body = a.gzipped
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		// The client hung up mid-response. There is nothing to recover and
		// nothing useful to log per request.
		return
	}
}

// cleanPath normalises a request path and contains it inside the asset root.
//
// path.Clean resolves "." and ".." segments, so "/../../etc/passwd" becomes
// "/etc/passwd", which then simply fails to match any asset. Because assets are
// looked up in a map rather than opened from disk, traversal cannot escape to
// the filesystem even if a segment survives.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	trailingSlash := strings.HasSuffix(p, "/") && p != "/"

	p = path.Clean(p)
	if trailingSlash && p != "/" {
		p += "/"
	}
	return p
}

// matchesETag reports whether an If-None-Match header matches the entity tag.
// It handles the "*" wildcard and comma-separated lists, and tolerates the weak
// prefix, since a weak match is sufficient for a GET.
func matchesETag(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}

// acceptsGzip reports whether the client accepts gzip encoding.
//
// It looks for the token rather than parsing full quality values. The one case
// that matters is an explicit "gzip;q=0", which means the client refuses gzip.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		token, params, _ := strings.Cut(part, ";")
		token = strings.TrimSpace(token)

		if token != "gzip" && token != "*" {
			continue
		}
		if q, ok := quality(params); ok && q == 0 {
			continue
		}
		return true
	}
	return false
}

// quality extracts a q parameter, if present.
func quality(params string) (float64, bool) {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || strings.TrimSpace(k) != "q" {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

var _ http.Handler = (*FileServer)(nil)
