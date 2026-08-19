package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// bodyStyles are the six silhouettes the front end knows about. A custom image
// may be supplied for any of them.
var bodyStyles = []string{"saloon", "estate", "mpv", "suv", "van", "taxi"}

// iconExtensions are tried in order. SVG first: it scales and, if drawn in
// currentColor, still inverts with the theme. A raster image will not invert —
// it looks identical on both — which is the trade for using artwork.
var iconExtensions = []string{".svg", ".png", ".webp", ".jpg", ".jpeg"}

func (s *Server) routeIcons(api *http.ServeMux) {
	api.HandleFunc("GET /api/fleet-icons", s.json(s.listIcons))
	api.HandleFunc("GET /api/fleet-icon/{style}", s.fleetIcon)
}

// iconPath finds a supplied image for a body style, or "" if there is none.
func (s *Server) iconPath(style string) string {
	if !validStyle(style) {
		return ""
	}
	dir := filepath.Join(s.cfg.DataDir, "icons")
	for _, ext := range iconExtensions {
		p := filepath.Join(dir, style+ext)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Size() > 0 {
			return p
		}
	}
	return ""
}

func validStyle(style string) bool {
	for _, s := range bodyStyles {
		if s == style {
			return true
		}
	}
	return false
}

// iconVersion is a short fingerprint of the file's size and modification time.
// It goes in the URL so an icon can be cached forever and still update the
// moment it is replaced — the address changes, so no stale copy can survive.
func iconVersion(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano())))
	return hex.EncodeToString(sum[:])[:10]
}

// listIcons tells the front end which styles have artwork and at what version,
// so it can fall back to the built-in drawing for the rest rather than showing
// a broken image.
func (s *Server) listIcons(r *http.Request) (any, error) {
	have := map[string]string{}
	for _, style := range bodyStyles {
		if p := s.iconPath(style); p != "" {
			have[style] = iconVersion(p)
		}
	}
	return map[string]any{
		"styles": bodyStyles,
		"custom": have,
		"folder": filepath.Join(s.cfg.DataDir, "icons"),
	}, nil
}

func (s *Server) fleetIcon(w http.ResponseWriter, r *http.Request) {
	// The style comes from a fixed list, never from the path, so a crafted
	// request cannot reach a file outside the icons folder.
	path := s.iconPath(r.PathValue("style"))
	if path == "" {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(path, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
	}

	// A request carrying the current fingerprint can be cached indefinitely:
	// replacing the file changes the fingerprint, so the browser asks for a
	// different URL rather than holding a stale image. Without one, fall back
	// to revalidating, which is correct but costs a round trip per page.
	if v := r.URL.Query().Get("v"); v != "" && v == iconVersion(path) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	}
	http.ServeFile(w, r, path)
}
