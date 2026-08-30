package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// large is a body above gzipMinBytes that compresses well, so the gzip path is
// actually exercised.
var large = []byte(strings.Repeat("the quick brown fox jumps over the lazy dog. ", 40))

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>root</title>")},
		"dash.css":        {Data: large},
		"b.js":            {Data: []byte("console.log(1)")},
		"demo/index.html": {Data: []byte("<!doctype html><title>demo</title>")},
		"demo/slow.html":  {Data: []byte("<!doctype html><title>slow</title>")},
		"img/logo.png":    {Data: bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, 400)},
	}
}

func newTestServer(t *testing.T) *FileServer {
	t.Helper()
	s, err := NewFileServer(testFS())
	if err != nil {
		t.Fatalf("NewFileServer: %v", err)
	}
	return s
}

// get issues a request and returns the recorded response.
func get(s *FileServer, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestNewFileServerRejectsEmptyFS(t *testing.T) {
	if _, err := NewFileServer(fstest.MapFS{}); err == nil {
		t.Error("NewFileServer on an empty FS returned nil, want an error")
	}
}

func TestServeBasic(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name         string
		target       string
		wantStatus   int
		wantType     string
		wantContains string
	}{
		{"root index", "/", http.StatusOK, "text/html; charset=utf-8", "root"},
		{"explicit index", "/index.html", http.StatusOK, "text/html; charset=utf-8", "root"},
		{"nested index via directory", "/demo/", http.StatusOK, "text/html; charset=utf-8", "demo"},
		{"nested index without slash", "/demo", http.StatusOK, "text/html; charset=utf-8", "demo"},
		{"nested file", "/demo/slow.html", http.StatusOK, "text/html; charset=utf-8", "slow"},
		{"javascript", "/b.js", http.StatusOK, "text/javascript; charset=utf-8", "console.log"},
		{"stylesheet", "/dash.css", http.StatusOK, "text/css; charset=utf-8", "quick brown fox"},
		{"png", "/img/logo.png", http.StatusOK, "image/png", ""},
		{"missing", "/nope.html", http.StatusNotFound, "", ""},
		{"missing directory", "/nope/", http.StatusNotFound, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(s, http.MethodGet, tt.target, nil)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			if got := rec.Header().Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if tt.wantContains != "" && !strings.Contains(rec.Body.String(), tt.wantContains) {
				t.Errorf("body does not contain %q", tt.wantContains)
			}
		})
	}
}

func TestContentLengthMatchesBody(t *testing.T) {
	s := newTestServer(t)
	rec := get(s, http.MethodGet, "/b.js", nil)

	want := rec.Body.Len()
	got, err := strconv.Atoi(rec.Header().Get("Content-Length"))
	if err != nil {
		t.Fatalf("Content-Length: %v", err)
	}
	if got != want {
		t.Errorf("Content-Length = %d, body is %d bytes", got, want)
	}
}

func TestHeadSendsNoBody(t *testing.T) {
	s := newTestServer(t)
	rec := get(s, http.MethodHead, "/index.html", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned %d body bytes, want 0", rec.Body.Len())
	}
	if rec.Header().Get("Content-Length") == "" {
		t.Error("HEAD response has no Content-Length")
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("HEAD response has no ETag")
	}
}

func TestRejectsNonGetMethods(t *testing.T) {
	s := newTestServer(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := get(s, method, "/index.html", nil)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
		if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Errorf("%s: Allow = %q, want %q", method, allow, "GET, HEAD")
		}
	}
}

func TestETagIsStableAndContentDerived(t *testing.T) {
	s := newTestServer(t)

	first := get(s, http.MethodGet, "/index.html", nil).Header().Get("ETag")
	second := get(s, http.MethodGet, "/index.html", nil).Header().Get("ETag")

	if first == "" {
		t.Fatal("no ETag on the response")
	}
	if first != second {
		t.Errorf("ETag changed between requests: %q then %q", first, second)
	}
	if !strings.HasPrefix(first, `"`) || !strings.HasSuffix(first, `"`) {
		t.Errorf("ETag %q is not a quoted string", first)
	}

	// Different content must produce a different tag.
	if other := get(s, http.MethodGet, "/demo/index.html", nil).Header().Get("ETag"); other == first {
		t.Error("two different files share an ETag")
	}

	// Identical content in a second server must produce the same tag, which is
	// what makes caching survive a rebuild.
	s2 := newTestServer(t)
	if rebuilt := get(s2, http.MethodGet, "/index.html", nil).Header().Get("ETag"); rebuilt != first {
		t.Errorf("ETag differs across builds: %q then %q", first, rebuilt)
	}
}

func TestConditionalRequest(t *testing.T) {
	s := newTestServer(t)
	etag := get(s, http.MethodGet, "/index.html", nil).Header().Get("ETag")

	tests := []struct {
		name        string
		ifNoneMatch string
		wantStatus  int
	}{
		{"exact match", etag, http.StatusNotModified},
		{"wildcard", "*", http.StatusNotModified},
		{"weak prefix on the request", "W/" + etag, http.StatusNotModified},
		{"present in a list", `"other", ` + etag + `, "another"`, http.StatusNotModified},
		{"list with spaces", "  " + etag + "  ", http.StatusNotModified},
		{"no match", `"something-else"`, http.StatusOK},
		{"empty header", "", http.StatusOK},
		{"unquoted garbage", "garbage", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := map[string]string{}
			if tt.ifNoneMatch != "" {
				h["If-None-Match"] = tt.ifNoneMatch
			}
			rec := get(s, http.MethodGet, "/index.html", h)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusNotModified {
				if rec.Body.Len() != 0 {
					t.Errorf("304 carried %d body bytes, want 0", rec.Body.Len())
				}
				if cl := rec.Header().Get("Content-Length"); cl != "" {
					t.Errorf("304 carried Content-Length %q, want none", cl)
				}
				if rec.Header().Get("ETag") == "" {
					t.Error("304 has no ETag")
				}
			}
		})
	}
}

func TestGzipNegotiation(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name           string
		acceptEncoding string
		target         string
		wantGzip       bool
	}{
		{"gzip accepted on a compressible asset", "gzip, deflate, br", "/dash.css", true},
		{"gzip alone", "gzip", "/dash.css", true},
		{"wildcard accepted", "*", "/dash.css", true},
		{"no accept-encoding header", "", "/dash.css", false},
		{"only brotli", "br", "/dash.css", false},
		{"gzip explicitly refused", "gzip;q=0", "/dash.css", false},
		{"gzip with a positive q", "gzip;q=0.8", "/dash.css", true},
		{"png is not compressed", "gzip", "/img/logo.png", false},
		{"tiny file is not compressed", "gzip", "/b.js", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := map[string]string{}
			if tt.acceptEncoding != "" {
				h["Accept-Encoding"] = tt.acceptEncoding
			}
			rec := get(s, http.MethodGet, tt.target, h)

			enc := rec.Header().Get("Content-Encoding")
			if tt.wantGzip && enc != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", enc)
			}
			if !tt.wantGzip && enc != "" {
				t.Fatalf("Content-Encoding = %q, want none", enc)
			}
			if !tt.wantGzip {
				return
			}

			// The body must actually be a valid gzip stream of the original.
			zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
			if err != nil {
				t.Fatalf("response is not valid gzip: %v", err)
			}
			got, err := io.ReadAll(zr)
			if err != nil {
				t.Fatalf("decompressing: %v", err)
			}
			if !bytes.Equal(got, large) {
				t.Error("decompressed body does not match the original")
			}
			if rec.Body.Len() >= len(large) {
				t.Errorf("gzip body is %d bytes, original is %d; compression did not help",
					rec.Body.Len(), len(large))
			}
		})
	}
}

func TestVaryHeaderOnCompressibleAssets(t *testing.T) {
	s := newTestServer(t)

	// A compressible asset varies by Accept-Encoding whether or not this
	// particular client asked for gzip.
	rec := get(s, http.MethodGet, "/dash.css", nil)
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}

	// An asset that is never compressed does not need it.
	rec = get(s, http.MethodGet, "/img/logo.png", map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q on an uncompressed asset, want none", got)
	}
}

func TestCacheControl(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		target string
		want   string
	}{
		{"/index.html", cacheHTML},
		{"/demo/index.html", cacheHTML},
		{"/b.js", cacheAsset},
		{"/dash.css", cacheAsset},
		{"/img/logo.png", cacheAsset},
	}

	for _, tt := range tests {
		rec := get(s, http.MethodGet, tt.target, nil)
		if got := rec.Header().Get("Cache-Control"); got != tt.want {
			t.Errorf("%s: Cache-Control = %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestPathTraversal(t *testing.T) {
	s := newTestServer(t)

	// None of these may reach anything outside the asset set. They either 404
	// or resolve to a legitimate asset, but never escape.
	targets := []string{
		"/../index.html",
		"/../../etc/passwd",
		"/demo/../../etc/passwd",
		"/./././../../../../../../etc/passwd",
		"/demo/../index.html",
		"//etc/passwd",
		"/demo/./slow.html",
	}

	for _, target := range targets {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+target, nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)

		if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "root:") {
			t.Errorf("%s escaped the asset root", target)
		}
		if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 200 or 404", target, rec.Code)
		}
	}
}

func TestCleanPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "/"},
		{"/", "/"},
		{"/a", "/a"},
		{"a", "/a"},
		{"/a/", "/a/"},
		{"/a//b", "/a/b"},
		{"/a/./b", "/a/b"},
		{"/a/../b", "/b"},
		{"/../a", "/a"},
		{"/../../a", "/a"},
		{"/a/b/../..", "/"},
	}

	for _, tt := range tests {
		if got := cleanPath(tt.in); got != tt.want {
			t.Errorf("cleanPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestServeFile(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	rec := httptest.NewRecorder()
	if !s.ServeFile(rec, req, "/b.js") {
		t.Fatal("ServeFile returned false for an existing asset")
	}
	if !strings.Contains(rec.Body.String(), "console.log") {
		t.Error("ServeFile served the wrong content")
	}

	rec = httptest.NewRecorder()
	if s.ServeFile(rec, req, "/missing.js") {
		t.Error("ServeFile returned true for a missing asset")
	}
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Error("ServeFile touched the response for a missing asset")
	}

	// ServeFile does not fall back to a directory index.
	rec = httptest.NewRecorder()
	if s.ServeFile(rec, req, "/demo/") {
		t.Error("ServeFile resolved a directory index, want an exact match only")
	}
}

func TestHasAndLen(t *testing.T) {
	s := newTestServer(t)

	if !s.Has("/b.js") {
		t.Error("Has(/b.js) = false, want true")
	}
	if s.Has("/missing.js") {
		t.Error("Has(/missing.js) = true, want false")
	}
	if got := s.Len(); got != len(testFS()) {
		t.Errorf("Len = %d, want %d", got, len(testFS()))
	}
}

func TestContentType(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"index.html", "text/html; charset=utf-8"},
		{"app.js", "text/javascript; charset=utf-8"},
		{"dash.css", "text/css; charset=utf-8"},
		{"data.json", "application/json; charset=utf-8"},
		{"chart.svg", "image/svg+xml"},
		{"logo.PNG", "image/png"},
		{"photo.jpeg", "image/jpeg"},
		{"favicon.ico", "image/x-icon"},
		{"notes.txt", "text/plain; charset=utf-8"},
		{"archive.tar.gz", defaultContentType},
		{"no-extension", defaultContentType},
		{"weird.xyz", defaultContentType},
	}

	for _, tt := range tests {
		if got := ContentType(tt.name); got != tt.want {
			t.Errorf("ContentType(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCompressible(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"text/html; charset=utf-8", true},
		{"text/css; charset=utf-8", true},
		{"text/javascript; charset=utf-8", true},
		{"application/json; charset=utf-8", true},
		{"image/svg+xml", true},
		{"application/xml", true},
		{"image/png", false},
		{"image/jpeg", false},
		{"font/woff2", false},
		{defaultContentType, false},
	}

	for _, tt := range tests {
		if got := compressible(tt.contentType); got != tt.want {
			t.Errorf("compressible(%q) = %v, want %v", tt.contentType, got, tt.want)
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{"gzip", true},
		{"gzip, deflate", true},
		{"deflate, gzip", true},
		{"gzip;q=1.0", true},
		{"gzip;q=0.5", true},
		{"*", true},
		{"br, gzip", true},
		{"GZIP", false}, // tokens are case-sensitive as sent by real browsers
		{"", false},
		{"deflate", false},
		{"br", false},
		{"gzip;q=0", false},
		{"gzip;q=0.0", false},
	}

	for _, tt := range tests {
		if got := acceptsGzip(tt.header); got != tt.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tt.header, got, tt.want)
		}
	}
}

func TestMatchesETag(t *testing.T) {
	const tag = `"abc123"`
	tests := []struct {
		header string
		want   bool
	}{
		{tag, true},
		{"*", true},
		{"W/" + tag, true},
		{`"other", "abc123"`, true},
		{`  "abc123"  `, true},
		{`"other"`, false},
		{"", false},
		{"abc123", false}, // unquoted is not a valid entity tag
	}

	for _, tt := range tests {
		if got := matchesETag(tt.header, tag); got != tt.want {
			t.Errorf("matchesETag(%q, %q) = %v, want %v", tt.header, tag, got, tt.want)
		}
	}
}
