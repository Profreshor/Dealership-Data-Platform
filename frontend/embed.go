// Package frontend contains the production portal built by Vite.
package frontend

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
)

//go:embed all:dist
var assets embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "dist")
	files := http.FileServer(http.FS(root))
	index, indexErr := fs.ReadFile(root, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		info, err := fs.Stat(root, name)
		exists := err == nil
		if !exists {
			if path.Ext(r.URL.Path) == "" {
				if indexErr != nil {
					http.Error(w, "Portal build unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Content-Length", strconv.Itoa(len(index)))
				if r.Method == http.MethodGet {
					_, _ = w.Write(index)
				}
				return
			} else {
				w.Header().Set("Cache-Control", "no-cache")
				http.NotFound(w, r)
				return
			}
		}
		if name == "assets" && info.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			http.NotFound(w, r)
			return
		}
		if exists && info.Mode().IsRegular() && hashedAsset.MatchString(name) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

var hashedAsset = regexp.MustCompile(`^assets/.+-[A-Za-z0-9_-]{8,}\.[^./]+$`)
