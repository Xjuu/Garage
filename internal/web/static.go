package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"regexp"
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

// assetRef matches the stylesheet and script URLs in the served HTML —
// including ones in a subdirectory like /static/repairs/pinbox.js: the
// character class used to exclude '/', which silently meant every asset
// under a subdirectory was served unversioned and never cache-busted no
// matter how many times its content changed on deploy. Every repairs.
// <domain> script and stylesheet lived under /static/repairs/ and was
// affected — a browser that ever cached one kept serving that exact copy
// for up to 4 hours (see staticHandler's max-age) after every single
// deploy, regardless of what actually shipped.
var assetRef = regexp.MustCompile(`/static/([A-Za-z0-9._/-]+\.(?:css|js))`)

// versionAssets rewrites /static/app.css into /static/app.css?v=<hash>.
//
// Validation headers are not enough on their own once a CDN sits in front. An
// edge cache holding an old stylesheet keeps serving it for its whole TTL, so
// a deployed change is invisible to everyone behind that cache — which is
// exactly what happened here: hours of CSS changes never reached the browser
// while the origin served them correctly all along.
//
// Putting the content hash in the URL removes the possibility entirely. A
// changed file has an address no cache has ever seen, so there is nothing
// stale to serve; an unchanged one keeps its address and stays cached.
func versionAssets(page []byte) []byte {
	return assetRef.ReplaceAllFunc(page, func(match []byte) []byte {
		name := string(assetRef.FindSubmatch(match)[1])
		sum, err := assetHash(name)
		if err != nil {
			return match // serve it unversioned rather than break the page
		}
		// A new slice, never append to `match`: ReplaceAllFunc hands back a
		// window into the source buffer, so appending to it writes over the
		// bytes that follow and corrupts the page.
		out := make([]byte, 0, len(match)+16)
		out = append(out, match...)
		return append(out, "?v="+sum...)
	})
}

var (
	hashMu    sync.RWMutex
	hashCache = map[string]string{}
)

func assetHash(name string) (string, error) {
	hashMu.RLock()
	sum, ok := hashCache[name]
	hashMu.RUnlock()
	if ok {
		return sum, nil
	}

	b, err := assets.ReadFile("assets/" + name)
	if err != nil {
		return "", err
	}
	raw := sha256.Sum256(b)
	sum = hex.EncodeToString(raw[:])[:12]

	hashMu.Lock()
	hashCache[name] = sum
	hashMu.Unlock()
	return sum, nil
}
