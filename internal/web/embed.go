// Package web embeds the built SPA and serves it as a single-page app: hashed
// assets are immutable and long-lived, index.html is never cached, and any
// unknown path falls back to index.html so the client router owns the URL space.
//
// The dist tree is populated by `task web-build` (Vite -> web/dist -> here). A
// backend-only build ships just the committed placeholder, so `go build` never
// depends on Node having run.
package web

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the embedded SPA. Requests under /api are none of its business
// and get a plain 404 rather than an HTML shell.
func Handler(log *slog.Logger) http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		// dist is embedded at build time; a failure here is a build-time bug.
		panic(err)
	}
	return handler(root, log)
}

func handler(root fs.FS, log *slog.Logger) http.Handler {
	index, indexErr := fs.ReadFile(root, "index.html")
	files := http.FileServerFS(root)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if p := strings.TrimPrefix(r.URL.Path, "/"); p != "" && isFile(root, p) {
			// Vite fingerprints asset filenames, so a given URL's bytes never
			// change — cache them hard. Everything else stays revalidated.
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}

		if indexErr != nil {
			http.Error(w, "frontend not built", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if _, err := w.Write(index); err != nil {
			log.WarnContext(r.Context(), "serving index.html failed", "error", err)
		}
	})
}

func isFile(root fs.FS, name string) bool {
	f, err := root.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	return err == nil && !info.IsDir()
}
