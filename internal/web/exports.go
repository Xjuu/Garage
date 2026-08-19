package web

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goldstar/internal/export"
	"goldstar/internal/store"
)

func (s *Server) routeExports(api *http.ServeMux) {
	api.HandleFunc("GET /api/exports", s.json(s.listExports))
	api.HandleFunc("POST /api/exports/generate", s.json(s.generateExport))
	api.HandleFunc("GET /api/exports/preview", s.json(s.previewExport))
	api.HandleFunc("GET /api/exports/file", s.downloadExport)
	api.HandleFunc("DELETE /api/exports/file", s.json(s.deleteExport))
}

func (s *Server) listExports(r *http.Request) (any, error) {
	files, err := s.exports.List()
	if err != nil {
		return nil, err
	}
	hits, misses := s.exports.Stats()
	return map[string]any{
		"files":  files,
		"folder": s.cfg.ExportsDir(),
		"cache":  map[string]int{"hits": hits, "misses": misses},
	}, nil
}

// windows are the one-click ranges on the Exports tab. "24h" is deliberately a
// rolling 24 hours rather than "today": an invoice that arrived at 23:50 last
// night is part of what you have not yet looked at.
var windows = map[string]struct {
	label string
	hours int
}{
	"24h": {"Last 24 hours", 24},
	"7d":  {"Last 7 days", 24 * 7},
	"30d": {"Last 30 days", 24 * 30},
	"90d": {"Last 90 days", 24 * 90},
}

func (s *Server) generateExport(r *http.Request) (any, error) {
	var body struct {
		Window string `json:"window"`
		From   string `json:"from"`
		To     string `json:"to"`
		Format string `json:"format"` // "xlsx" (default) or "csv"
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}

	var q store.Query
	var label, from, to string

	switch {
	case body.Window == "all":
		label = "Everything"

	case body.Window == "custom" || (body.From != "" && body.To != ""):
		if body.From == "" || body.To == "" {
			return nil, fail(http.StatusBadRequest, "a custom range needs both a start and an end date")
		}
		from, to = body.From, body.To
		if from > to {
			from, to = to, from
		}
		label = fmt.Sprintf("%s to %s", from, to)
		q.From, q.To = from, to

	default:
		w, ok := windows[body.Window]
		if !ok {
			return nil, fail(http.StatusBadRequest, "unknown window %q", body.Window)
		}
		// Filtering on invoice_date, which is a date not a timestamp, means a
		// 24-hour window is expressed as the two dates it can touch.
		end := time.Now()
		start := end.Add(-time.Duration(w.hours) * time.Hour)
		from, to = start.Format("2006-01-02"), end.Format("2006-01-02")
		label = w.label
		q.From, q.To = from, to
	}

	invoices, err := s.db.AllMatching(q)
	if err != nil {
		return nil, err
	}
	if len(invoices) == 0 {
		return nil, fail(http.StatusBadRequest, "nothing to export for %s", strings.ToLower(label))
	}

	items := 0
	for _, inv := range invoices {
		items += len(inv.Items)
	}

	name := exportName(body.Window, from, to)
	if body.Format == "csv" {
		name = strings.TrimSuffix(name, ".xlsx") + ".csv"
		path := filepath.Join(s.cfg.ExportsDir(), name)
		if err := export.WriteCSV(path, invoices); err != nil {
			return nil, err
		}
		s.exports.Invalidate()
		return map[string]any{
			"name": name, "label": label, "format": "csv",
			"invoices": len(invoices), "items": items,
		}, nil
	}

	path := filepath.Join(s.cfg.ExportsDir(), name)
	if _, err := export.WriteWithManifest(path, label, from, to, invoices); err != nil {
		return nil, err
	}
	s.exports.Invalidate()
	return map[string]any{
		"name": name, "label": label, "format": "xlsx",
		"invoices": len(invoices), "items": items,
	}, nil
}

// exportName keeps generated files distinct by window and range, and adds a
// clock time so regenerating the same window does not silently overwrite the
// previous copy.
func exportName(window, from, to string) string {
	now := time.Now()
	switch {
	case window == "all":
		return fmt.Sprintf("goldstar-all-%s.xlsx", now.Format("2006-01-02-1504"))
	case from != "" && to != "":
		// The range already carries the dates; only a clock time is needed to
		// keep a regenerated copy distinct.
		return fmt.Sprintf("goldstar-%s_%s-at%s.xlsx", from, to, now.Format("1504"))
	default:
		return fmt.Sprintf("goldstar-%s.xlsx", now.Format("2006-01-02-1504"))
	}
}

func (s *Server) previewExport(r *http.Request) (any, error) {
	name := r.URL.Query().Get("name")
	path, err := s.exports.Resolve(name)
	if err != nil {
		return nil, fail(http.StatusNotFound, "%v", err)
	}
	return export.ReadPreview(path, filepath.Base(path))
}

func (s *Server) downloadExport(w http.ResponseWriter, r *http.Request) {
	path, err := s.exports.Resolve(r.URL.Query().Get("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	ctype := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	if strings.HasSuffix(strings.ToLower(path), ".csv") {
		ctype = "text/csv; charset=utf-8"
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(path)))
	w.Header().Set("Content-Type", ctype)
	http.ServeFile(w, r, path)
}

func (s *Server) deleteExport(r *http.Request) (any, error) {
	if err := s.exports.Remove(r.URL.Query().Get("name")); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return map[string]any{"ok": true}, nil
}
