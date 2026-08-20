package web

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"

	"goldstar/internal/partsauth"
)

// partsRoutes builds the entire site served at parts.<domain> — a
// deliberately small, separate app: a shared 6-digit PIN instead of an
// account, one screen, three things a worker can do (search a part, search
// a vehicle, log one taken for the other). It shares the database — stock is
// computed from the same invoices — but nothing about its own auth, cookies
// or session state touches the dashboard's.
func (s *Server) partsRoutes(sub fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(sub)))
	mux.HandleFunc("GET /{$}", s.handlePartsRoot)
	mux.HandleFunc("POST /api/parts/login", s.handlePartsLogin)
	mux.HandleFunc("POST /api/parts/logout", s.handlePartsLogout)
	mux.HandleFunc("GET /api/parts/session", s.handlePartsSession)
	// Photos aren't sensitive — IP-gated like everything else on this host,
	// but not behind the device cookie too; there is nothing here worth a
	// second layer of auth for.
	mux.HandleFunc("GET /api/parts/photo/{part}", s.servePartPhoto)

	device := http.NewServeMux()
	device.HandleFunc("GET /api/parts/search-parts", s.json(s.partsSearchParts))
	device.HandleFunc("GET /api/parts/search-vehicles", s.json(s.partsSearchVehicles))
	device.HandleFunc("GET /api/parts/stock", s.json(s.partsStockLookup))
	device.HandleFunc("POST /api/parts/take", s.json(s.partsLogTake))
	protected := s.requirePartsDevice(device)
	mux.Handle("/api/parts/search-parts", protected)
	mux.Handle("/api/parts/search-vehicles", protected)
	mux.Handle("/api/parts/stock", protected)
	mux.Handle("/api/parts/take", protected)

	// Every single request to this host passes through the IP check first,
	// including the PIN page itself — "until an address is added, allow
	// nothing" means nothing, not "nothing except the login screen".
	return s.requireAllowedIP(mux)
}

// requireAllowedIP is the fail-closed gate: an empty allow-list — the
// starting state, and the state until the admin page adds the first address
// — refuses every request rather than defaulting to open.
func (s *Server) requireAllowedIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := partsauth.ClientIP(r)
		allowed, err := s.db.IPAllowed(ip)
		if err != nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		if !allowed {
			http.Error(w, "This address is not permitted to use this site.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requirePartsDevice additionally requires a signed, unexpired device
// cookie whose device has not been revoked. Two separate checks on purpose:
// a forged or expired cookie fails on cryptography alone; a genuine, still
// well-signed cookie from a device the admin has since revoked fails on the
// database lookup that a signature can never express.
func (s *Server) requirePartsDevice(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.partsAuth.ValidateDevice(r)
		if !ok {
			http.Error(w, `{"error":"not signed in"}`, http.StatusUnauthorized)
			return
		}
		active, err := s.db.PartsDeviceActive(id)
		if err != nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		if !active {
			http.Error(w, `{"error":"this device has been removed"}`, http.StatusUnauthorized)
			return
		}
		if partsIsMutating(r.Method) && !s.partsAuth.CSRFOK(r) {
			http.Error(w, `{"error":"bad CSRF token"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// partsIsMutating mirrors internal/auth's identical, unexported check — not
// shared across the package boundary, so duplicated here rather than
// exported just for this one caller.
func partsIsMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// handlePartsRoot serves the PIN screen or the counter itself, depending on
// whether this browser already has a trusted, still-active device.
func (s *Server) handlePartsRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page := "assets/parts/pin.html"
	if id, ok := s.partsAuth.ValidateDevice(r); ok {
		if active, err := s.db.PartsDeviceActive(id); err == nil && active {
			page = "assets/parts/index.html"
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

func (s *Server) handlePartsLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if err := s.partsAuth.CheckPIN(r, body.Code); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	id := s.partsAuth.IssueDevice(w)
	if err := s.db.RegisterPartsDevice(id, r.UserAgent()); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handlePartsLogout(w http.ResponseWriter, r *http.Request) {
	// Forgets this browser only — it does not revoke the device server-side.
	// Revoking is an admin action (a device someone else might still be
	// using should not be killed by one person signing out of it), this is
	// just "stop remembering me on this particular browser".
	s.partsAuth.ClearDevice(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handlePartsSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	authenticated := false
	if id, ok := s.partsAuth.ValidateDevice(r); ok {
		if active, err := s.db.PartsDeviceActive(id); err == nil && active {
			authenticated = true
		}
	}
	json.NewEncoder(w).Encode(map[string]bool{"authenticated": authenticated})
}

// ── the counter itself ──────────────────────────────────────────────────

func (s *Server) partsSearchParts(r *http.Request) (any, error) {
	return s.db.SearchStockParts(r.URL.Query().Get("q"), 20)
}

func (s *Server) partsSearchVehicles(r *http.Request) (any, error) {
	return s.db.SearchStockVehicles(r.URL.Query().Get("q"), 20)
}

func (s *Server) partsStockLookup(r *http.Request) (any, error) {
	part := r.URL.Query().Get("part")
	if part == "" {
		return nil, fail(http.StatusBadRequest, "part is required")
	}
	p, err := s.db.StockPartByNumber(part)
	if err != nil {
		return nil, fail(http.StatusNotFound, "unknown part")
	}
	return p, nil
}

func (s *Server) partsLogTake(r *http.Request) (any, error) {
	var body struct {
		PartNumber string  `json:"part_number"`
		VehicleReg string  `json:"vehicle_reg"`
		Quantity   float64 `json:"quantity"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}

	id, _ := s.partsAuth.ValidateDevice(r) // already proven valid by requirePartsDevice
	if err := s.db.LogStockTake(body.PartNumber, body.VehicleReg, body.Quantity, id); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}

	p, err := s.db.StockPartByNumber(body.PartNumber)
	if err != nil {
		return okResponse(), nil
	}
	return p, nil
}
