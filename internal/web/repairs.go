package web

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strconv"

	"goldstar/internal/store"
)

// repairsRoutes builds the entire site served at repairs.<domain> — a
// shared 6-digit PIN instead of an account, one screen: type a
// registration, see what's been done to that car before, log this visit.
// It shares the database — vehicle spec fields feed straight into the same
// registry the dashboard already shows — but nothing about its own auth,
// cookies or session state touches the dashboard's, or the (separate)
// PIN system any other subdomain might have.
func (s *Server) repairsRoutes(sub fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(sub)))
	mux.HandleFunc("GET /{$}", s.handleRepairsRoot)
	mux.HandleFunc("POST /api/repairs/login", s.handleRepairsLogin)
	mux.HandleFunc("POST /api/repairs/logout", s.handleRepairsLogout)
	mux.HandleFunc("GET /api/repairs/session", s.handleRepairsSession)

	device := http.NewServeMux()
	device.HandleFunc("GET /api/repairs/search-vehicles", s.json(s.repairsSearchVehicles))
	device.HandleFunc("GET /api/repairs/history", s.json(s.repairsHistory))
	device.HandleFunc("POST /api/repairs/log", s.json(s.repairsLogEntry))
	protected := s.requireRepairsDevice(device)
	mux.Handle("/api/repairs/search-vehicles", protected)
	mux.Handle("/api/repairs/history", protected)
	mux.Handle("/api/repairs/log", protected)

	return mux
}

// requireRepairsDevice mirrors requirePartsDevice's now-removed shape: a
// forged or expired cookie fails on cryptography alone (ValidateDevice); a
// genuine, still well-signed cookie from a device the admin has since
// revoked fails on the database lookup a signature can never express.
func (s *Server) requireRepairsDevice(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.repairsAuth.ValidateDevice(r)
		if !ok {
			http.Error(w, `{"error":"not signed in"}`, http.StatusUnauthorized)
			return
		}
		active, err := s.db.RepairsDeviceActive(id)
		if err != nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		if !active {
			http.Error(w, `{"error":"this device has been removed"}`, http.StatusUnauthorized)
			return
		}
		if repairsIsMutating(r.Method) && !s.repairsAuth.CSRFOK(r) {
			http.Error(w, `{"error":"bad CSRF token"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func repairsIsMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// queryLimit reads an optional ?limit= override — normal type-to-search
// uses the default, but "browse everything" (an empty query, fired the
// moment the search box is focused) asks for a much longer list. Anything
// unparsable or non-positive falls back to def rather than erroring over
// what is a convenience parameter, not user input that needs validating.
func queryLimit(r *http.Request, def int) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func (s *Server) handleRepairsRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page := "assets/repairs/pin.html"
	if id, ok := s.repairsAuth.ValidateDevice(r); ok {
		if active, err := s.db.RepairsDeviceActive(id); err == nil && active {
			page = "assets/repairs/index.html"
		}
	}
	b, err := assets.ReadFile(page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(versionAssets(b))
}

func (s *Server) handleRepairsLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if err := s.repairsAuth.CheckPIN(r, body.Code); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	id := s.repairsAuth.IssueDevice(w)
	if err := s.db.RegisterRepairsDevice(id, r.UserAgent()); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleRepairsLogout(w http.ResponseWriter, r *http.Request) {
	// Forgets this browser only — an admin revoking the device is what
	// actually invalidates it server-side.
	s.repairsAuth.ClearDevice(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleRepairsSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	authenticated := false
	if id, ok := s.repairsAuth.ValidateDevice(r); ok {
		if active, err := s.db.RepairsDeviceActive(id); err == nil && active {
			authenticated = true
		}
	}
	json.NewEncoder(w).Encode(map[string]bool{"authenticated": authenticated})
}

// ── the log itself ──────────────────────────────────────────────────────

func (s *Server) repairsSearchVehicles(r *http.Request) (any, error) {
	return s.db.SearchRepairVehicles(r.URL.Query().Get("q"), queryLimit(r, 20))
}

// repairsHistory is what a typed-in registration surfaces: everything
// logged for that car before, so the crew can see it without leaving the
// form — the "make it a list of repairs also" part of the brief.
func (s *Server) repairsHistory(r *http.Request) (any, error) {
	reg := r.URL.Query().Get("reg")
	if reg == "" {
		return nil, fail(http.StatusBadRequest, "reg is required")
	}
	return s.db.ListRepairsForVehicle(reg)
}

func (s *Server) repairsLogEntry(r *http.Request) (any, error) {
	var body store.Repair
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	deviceID, _ := s.repairsAuth.ValidateDevice(r) // already proven valid by requireRepairsDevice
	id, err := s.db.LogRepair(body, deviceID)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return map[string]any{"ok": true, "id": id}, nil
}
