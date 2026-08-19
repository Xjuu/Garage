package web

import (
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

// listIcons tells the front end which styles have artwork, so it can fall back
// to the built-in drawing for the rest rather than showing a broken image.
func (s *Server) listIcons(r *http.Request) (any, error) {
	have := map[string]bool{}
	for _, style := range bodyStyles {
		have[style] = s.iconPath(style) != ""
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
	// Artwork changes rarely but must not be stale after a replacement, so a
	// short cache with revalidation rather than a long one.
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	http.ServeFile(w, r, path)
}
