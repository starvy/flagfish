package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testHandler(t *testing.T, files fstest.MapFS) http.Handler {
	t.Helper()
	return handler(files, slog.New(slog.DiscardHandler))
}

func do(t *testing.T, h http.Handler, method, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Result()
}

func TestServesHashedAssetImmutably(t *testing.T) {
	h := testHandler(t, fstest.MapFS{
		"assets/index-abc123.js": {Data: []byte("console.log(1)")},
		"index.html":             {Data: []byte("<!doctype html>")},
	})

	res := do(t, h, http.MethodGet, "/assets/index-abc123.js")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable", cc)
	}
}

func TestUnknownRouteFallsBackToIndex(t *testing.T) {
	h := testHandler(t, fstest.MapFS{
		"index.html": {Data: []byte("SHELL")},
	})

	res := do(t, h, http.MethodGet, "/challenges/42")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "SHELL" {
		t.Fatalf("body = %q, want the index shell", body)
	}
}

func TestApiPathIsNotServed(t *testing.T) {
	h := testHandler(t, fstest.MapFS{"index.html": {Data: []byte("SHELL")}})

	res := do(t, h, http.MethodGet, "/api/v1/challenges")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an /api path", res.StatusCode)
	}
}

func TestBackendOnlyBuildReports503(t *testing.T) {
	// No index.html: the backend-only build where only the placeholder shipped.
	h := testHandler(t, fstest.MapFS{".gitkeep": {Data: []byte{}}})

	res := do(t, h, http.MethodGet, "/challenges")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the SPA is absent", res.StatusCode)
	}
}
