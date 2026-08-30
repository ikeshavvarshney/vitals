package tests

import (
	"net/http"
	"strings"
	"testing"
)

// fetch returns the response and body for one path on the test server.
func fetch(t *testing.T, url string, headers map[string]string) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building request for %s: %v", url, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body := readBody(t, resp)
	resp.Body.Close()
	return resp, body
}

// TestNothingIsFetchedFromAnywhereElse is the claim the whole project rests on,
// checked against what is actually served rather than against the source tree.
func TestNothingIsFetchedFromAnywhereElse(t *testing.T) {
	srv, _ := newServer(t)

	pages := []string{
		"/", "/snapshot.html", "/dash.css", "/dash.js", "/snapshot.js",
		"/demo/", "/demo/heavy.html", "/demo/shifty.html", "/demo/blocking.html",
		"/demo/demo.css", "/b.js", "/beacon.src.js",
	}

	// The SVG namespace is a URL that is never fetched: it identifies the
	// dialect of an inline data: icon, and the browser resolves it against a
	// built-in table. Nothing else absolute is allowed.
	const allowed = "http://www.w3.org/2000/svg"

	for _, page := range pages {
		t.Run(page, func(t *testing.T) {
			_, body := fetch(t, srv.URL+page, nil)
			scrubbed := strings.ReplaceAll(body, allowed, "")

			for _, bad := range []string{"http://", "https://", "//cdn", "cdnjs", "unpkg", "jsdelivr"} {
				if strings.Contains(scrubbed, bad) {
					t.Errorf("%s references %q; nothing may be fetched from another origin", page, bad)
				}
			}
			for _, font := range []string{"@font-face", "fonts.googleapis", "fonts.gstatic", ".woff"} {
				if strings.Contains(scrubbed, font) {
					t.Errorf("%s loads a web font (%q); the design is a system font stack", page, font)
				}
			}
		})
	}
}

// TestDashboardScriptsRevalidate covers the caching policy the dashboard needs:
// its scripts change under a stable name, so a browser that reuses one for an
// hour without asking keeps running a version that has since been fixed.
func TestDashboardScriptsRevalidate(t *testing.T) {
	srv, _ := newServer(t)

	for _, path := range []string{"/dash.js", "/dash.css", "/snapshot.js"} {
		resp, _ := fetch(t, srv.URL+path, nil)

		if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache", path, got)
		}
		etag := resp.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("%s has no ETag, so revalidation cannot answer 304", path)
		}

		// The revalidation must actually be cheap, or no-cache would be a real
		// cost on every load.
		again, body := fetch(t, srv.URL+path, map[string]string{"If-None-Match": etag})
		if again.StatusCode != http.StatusNotModified {
			t.Errorf("%s conditional request = %d, want 304", path, again.StatusCode)
		}
		if body != "" {
			t.Errorf("%s 304 carried %d bytes of body", path, len(body))
		}
	}
}

// TestBeaconStaysCacheable is the other half of the policy: the beacon is
// requested by every page view of an instrumented site, where a conditional
// request per view would be a real cost.
func TestBeaconStaysCacheable(t *testing.T) {
	srv, _ := newServer(t)

	resp, _ := fetch(t, srv.URL+"/b.js", nil)
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "max-age=") {
		t.Errorf("beacon Cache-Control = %q, want a max-age", got)
	}
}

func TestAssetsAreCompressedWhenAsked(t *testing.T) {
	srv, _ := newServer(t)

	resp, _ := fetch(t, srv.URL+"/dash.js", map[string]string{"Accept-Encoding": "gzip"})
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want it to name Accept-Encoding", got)
	}
}
