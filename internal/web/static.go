package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

// staticHandler serves the embedded assets with a content-based ETag.
//
// Files embedded with go:embed carry a zero modification time, so the standard
// file server sends neither Last-Modified nor ETag. With nothing to validate
// against, a browser is free to cache heuristically and keep an old stylesheet
// indefinitely — which makes a deployed change invisible, and looks for all
// the world like the change was never made.
//
// Hashing the contents fixes it at the root: the ETag changes only when the
// file does, so an unchanged asset costs a 304 and a changed one is fetched.
func staticHandler(files fs.FS) http.Handler {
	var (
		mu   sync.RWMutex
		tags = map[string]string{}
	)

	etag := func(name string) string {
		mu.RLock()
		tag, ok := tags[name]
		mu.RUnlock()
		if ok {
			return tag
		}

		f, err := files.Open(name)
		if err != nil {
			return ""
		}
		defer f.Close()

		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return ""
		}
		tag = `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`

		mu.Lock()
		tags[name] = tag
		mu.Unlock()
		return tag
	}

	fileServer := http.FileServer(http.FS(files))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if tag := etag(name); tag != "" {
			w.Header().Set("ETag", tag)
			// Revalidate every time. The assets are small and a 304 is cheap;
			// silently serving a stale dashboard is not.
			w.Header().Set("Cache-Control", "no-cache")

			if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, tag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
