package web

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const shell = "<!doctype html>SHELL"

func staged() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte(shell)},
		"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc123.css": {Data: []byte(":root{}")},
		"favicon.svg":             {Data: []byte("<svg/>")},
		".gitkeep":                {Data: []byte{}},
	}
}

func testHandler(t *testing.T, files fstest.MapFS) http.Handler {
	t.Helper()
	return handler(files, slog.New(slog.DiscardHandler))
}

func do(t *testing.T, h http.Handler, method, path string, header http.Header) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(b)
}

// The cache split is the whole contract: a fingerprinted asset can be held for a
// year, anything whose URL outlives its bytes must revalidate on every load.
func TestCacheControl(t *testing.T) {
	h := testHandler(t, staged())

	tests := []struct {
		name      string
		path      string
		wantCache string
		wantType  string
	}{
		{"hashed script is immutable", "/assets/index-abc123.js", cacheImmutable, "text/javascript; charset=utf-8"},
		{"hashed stylesheet is immutable", "/assets/index-abc123.css", cacheImmutable, "text/css; charset=utf-8"},
		{"unhashed root file revalidates", "/favicon.svg", cacheRevalidate, "image/svg+xml"},
		{"index.html revalidates", "/index.html", cacheRevalidate, "text/html; charset=utf-8"},
		{"spa fallback revalidates", "/challenges/42", cacheRevalidate, "text/html; charset=utf-8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := do(t, h, http.MethodGet, tc.path, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			if got := res.Header.Get("Cache-Control"); got != tc.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tc.wantCache)
			}
			if got := res.Header.Get("Content-Type"); got != tc.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tc.wantType)
			}
		})
	}
}

func TestUnknownRouteFallsBackToIndex(t *testing.T) {
	h := testHandler(t, staged())

	for _, p := range []string{"/", "/challenges/42", "/admin/users", "/login"} {
		res := do(t, h, http.MethodGet, p, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, res.StatusCode)
		}
		if got := body(t, res); got != shell {
			t.Fatalf("GET %s: body = %q, want the index shell", p, got)
		}
	}
}

// A hashed chunk that is gone means the deploy is stale. Handing back the shell
// would surface in the browser as an unreadable module parse error.
func TestMissingAssetIs404NotTheShell(t *testing.T) {
	h := testHandler(t, staged())

	res := do(t, h, http.MethodGet, "/assets/index-deadbeef.js", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
	if strings.Contains(body(t, res), "SHELL") {
		t.Fatal("a missing asset was answered with the SPA shell")
	}
}

func TestApiPathNeverGetsTheShell(t *testing.T) {
	h := testHandler(t, staged())

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api"},
		{http.MethodGet, "/api/v1/challenges"},
		{http.MethodGet, "/api/v1/admin/nope"},
		{http.MethodPost, "/api/v1/submissions"},
		{http.MethodDelete, "/api/v1/whatever"},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := do(t, h, tc.method, tc.path, nil)
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", res.StatusCode)
			}
			if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
				t.Errorf("Content-Type = %q, want JSON", ct)
			}
			if b := body(t, res); strings.Contains(b, "SHELL") || strings.Contains(b, "<!doctype") {
				t.Fatalf("an /api miss was answered with the SPA shell: %q", b)
			}
		})
	}
}

// The path that ships when nobody ran Node: only the placeholder is embedded.
func TestBackendOnlyBuildReports503(t *testing.T) {
	h := testHandler(t, fstest.MapFS{".gitkeep": {Data: []byte{}}})

	for _, p := range []string{"/", "/challenges", "/index.html"} {
		res := do(t, h, http.MethodGet, p, nil)
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("GET %s: status = %d, want 503 when the SPA is absent", p, res.StatusCode)
		}
	}
	// /api is the API's business whether the SPA was staged or not.
	if res := do(t, h, http.MethodGet, "/api/v1/me", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an /api path", res.StatusCode)
	}
}

// Guards the real //go:embed tree rather than a fake one: a checkout that never
// ran `task web-build` must still compile, and must answer 503.
func TestEmbeddedTreeIsPlaceholderOnlyOrComplete(t *testing.T) {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	res := do(t, Handler(slog.New(slog.DiscardHandler)), http.MethodGet, "/challenges", nil)

	if _, err := fs.Stat(root, "index.html"); err != nil {
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("unstaged dist: status = %d, want 503", res.StatusCode)
		}
		return
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("staged dist: status = %d, want 200", res.StatusCode)
	}
}

func TestNoDirectoryTraversal(t *testing.T) {
	h := testHandler(t, staged())

	// Nothing escapes dist/, and nothing dot-prefixed reaches the wire; both fall
	// through to the shell rather than leaking a file.
	for _, p := range []string{"/../embed.go", "/assets/../../embed.go", "/.gitkeep"} {
		res := do(t, h, http.MethodGet, p, nil)
		got := body(t, res)
		if strings.Contains(got, "package web") {
			t.Fatalf("GET %s leaked a file from outside dist", p)
		}
		if res.StatusCode != http.StatusOK || got != shell {
			t.Fatalf("GET %s: status = %d body = %q, want the shell", p, res.StatusCode, got)
		}
	}
}

func TestHeadAndConditionalRequests(t *testing.T) {
	h := testHandler(t, staged())

	head := do(t, h, http.MethodHead, "/assets/index-abc123.js", nil)
	if head.StatusCode != http.StatusOK {
		t.Fatalf("HEAD asset: status = %d, want 200", head.StatusCode)
	}
	if got := head.Header.Get("Cache-Control"); got != cacheImmutable {
		t.Errorf("HEAD asset: Cache-Control = %q, want %q", got, cacheImmutable)
	}

	res := do(t, h, http.MethodGet, "/challenges", nil)
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("the shell carries no ETag, so every navigation refetches it in full")
	}

	again := do(t, h, http.MethodGet, "/challenges", http.Header{"If-None-Match": {etag}})
	if again.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 for a matching ETag", again.StatusCode)
	}
}

func TestNonReadMethodIsRejected(t *testing.T) {
	h := testHandler(t, staged())

	res := do(t, h, http.MethodPost, "/challenges", nil)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
	if got := res.Header.Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
	}
}
