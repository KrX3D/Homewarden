// Package web serves the compiled web interface embedded in the binary.
//
// Vite builds into dist/. Only a marker file is tracked in git, so a plain `go build`
// without a frontend build still compiles and serves a short notice instead.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const missing = `<!doctype html><meta charset="utf-8"><title>Homewarden</title>
<p style="font:16px system-ui;padding:2rem">The web interface was not built into this binary. Run <code>npm run build</code> in <code>web/</code>.</p>`

// Handler serves static files and falls back to index.html for client-side routes.
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(root, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					// Vite puts a content hash in every asset name.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(root, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if err != nil {
			w.Write([]byte(missing))
			return
		}
		w.Write(index)
	})
}
