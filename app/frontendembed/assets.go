// Package frontendembed serves the Gomar frontend from embedded build files.
package frontendembed

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed web/index.html web/static
var web embed.FS

// New serves the Django root page and Vite files under /static/.
func New() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := "index.html"
		static := strings.HasPrefix(r.URL.Path, "/static/")
		switch {
		case r.URL.Path == "/":
			w.Header().Set("X-Content-Type-Options", "nosniff")
		case static:
			name = strings.TrimPrefix(r.URL.Path, "/static/")
			if name == "." || !fs.ValidPath(name) {
				http.NotFound(w, r)
				return
			}
			name = "static/" + name
		default:
			http.NotFound(w, r)
			return
		}

		body, err := web.ReadFile("web/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !static && r.Method == http.MethodOptions {
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			allow := "GET, HEAD, OPTIONS"
			if static {
				allow = "GET, HEAD"
			}
			w.Header().Set("Allow", allow)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		if static {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", fmt.Sprintf("\"%x\"", sha256.Sum256(body)))
		}
		switch path.Ext(name) {
		case ".html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case ".js", ".mjs":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	})
}
