// Package web embeds the built SPA and serves it as a single-page app: hashed
// assets are immutable and long-lived, index.html is never cached, and any
// unknown path falls back to index.html so the client router owns the URL space.
//
// The dist tree is populated by `task web-build` (Vite -> web/dist -> here). A
// backend-only build ships just the committed placeholder, so `go build` never
// depends on Node having run.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

const (
	assetsDir = "assets/"

	// Vite fingerprints every filename under assets/, so a given URL's bytes can
	// never change. Everything else must revalidate, or a deploy strands clients on
	// a cached index.html that points at chunks which no longer exist.
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
)

// Handler serves the embedded SPA. Requests under /api are none of its business
// and get a JSON 404 rather than an HTML shell.
func Handler(log *slog.Logger) http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		// dist is embedded at build time; a failure here is a build-time bug.
		panic(err)
	}
	return handler(root, log)
}

type spa struct {
	root  fs.FS
	log   *slog.Logger
	index []byte // nil when the SPA was never staged
	etag  string
}

func handler(root fs.FS, log *slog.Logger) http.Handler {
	s := &spa{root: root, log: log}
	if index, err := fs.ReadFile(root, "index.html"); err == nil {
		sum := sha256.Sum256(index)
		s.index = index
		s.etag = `"` + base64.RawURLEncoding.EncodeToString(sum[:]) + `"`
	}
	return s
}

func (s *spa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// This handler is the router's NotFound, so every unmatched /api path lands here
	// too. Those callers parse JSON; an HTML shell with a 200 would be a lie they
	// cannot even read.
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		s.notFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := cleanPath(r.URL.Path)
	if name != "" && s.serveFile(w, r, name) {
		return
	}
	// A miss under assets/ is a stale deploy, not a client route. Falling back to
	// index.html would hand the browser HTML where it asked for a module and the
	// failure would surface as a syntax error three layers away.
	if strings.HasPrefix(name, assetsDir) {
		s.notFound(w, r)
		return
	}
	s.serveIndex(w, r)
}

// cleanPath maps a request path onto an fs.FS name, or "" if it cannot be one.
// Cleaning resolves any ".." before the name is ever opened, so an escape from the
// embedded tree is not expressible.
func cleanPath(p string) string {
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	if name == "" || !fs.ValidPath(name) {
		return ""
	}
	// Nothing dot-prefixed in dist/ is meant for the wire: the .gitkeep placeholder
	// and Vite's .vite/ metadata are build plumbing.
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return ""
		}
	}
	return name
}

func (s *spa) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := s.root.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}

	content, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			s.log.WarnContext(r.Context(), "reading embedded asset failed", "path", name, "error", err)
			return false
		}
		content = bytes.NewReader(b)
	}

	if strings.HasPrefix(name, assetsDir) {
		w.Header().Set("Cache-Control", cacheImmutable)
	} else {
		w.Header().Set("Cache-Control", cacheRevalidate)
	}
	setContentType(w, name)
	// Embedded files carry a zero modtime, so ServeContent emits no Last-Modified;
	// it still gives us HEAD, Range and conditional requests for free.
	http.ServeContent(w, r, name, info.ModTime(), content)
	return true
}

func (s *spa) serveIndex(w http.ResponseWriter, r *http.Request) {
	if s.index == nil {
		// Backend-only build: only the placeholder shipped. 503 says "this server has
		// no frontend", where a 404 would blame the URL.
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "frontend not built", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", cacheRevalidate)
	w.Header().Set("ETag", s.etag)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// no-cache means revalidate, not refetch: the ETag turns the shell request on
	// every navigation into a 304 for as long as the build is unchanged.
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(s.index))
}

func (s *spa) notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", cacheRevalidate)
	w.WriteHeader(http.StatusNotFound)
	body := `{"title":"Not Found","status":404,"detail":"no route matches this path"}`
	if _, err := io.WriteString(w, body); err != nil {
		s.log.WarnContext(r.Context(), "writing 404 failed", "error", err)
	}
}

// The host's MIME table is not a dependency worth having — a machine that maps .js
// to text/plain breaks every module import in the app. Pin the types the SPA cannot
// load without and let ServeContent sniff the rest.
var contentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json",
	".map":   "application/json",
	".mjs":   "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".wasm":  "application/wasm",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

func setContentType(w http.ResponseWriter, name string) {
	if ct, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		w.Header().Set("Content-Type", ct)
	}
}
