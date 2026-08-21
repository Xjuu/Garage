package web

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

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
	mux.HandleFunc("GET /upload", s.handleRepairsUploadPage)
	mux.HandleFunc("POST /api/repairs/login", s.handleRepairsLogin)
	mux.HandleFunc("POST /api/repairs/logout", s.handleRepairsLogout)
	mux.HandleFunc("GET /api/repairs/session", s.handleRepairsSession)

	device := http.NewServeMux()
	device.HandleFunc("GET /api/repairs/search-vehicles", s.json(s.repairsSearchVehicles))
	device.HandleFunc("GET /api/repairs/reg-exists", s.json(s.repairsRegExists))
	device.HandleFunc("GET /api/repairs/history", s.json(s.repairsHistory))
	device.HandleFunc("POST /api/repairs/log", s.json(s.repairsLogEntry))
	// The read is no more sensitive than search/history above — reading a
	// vehicle's current spec carries none of the risk of changing it, so it
	// sits behind the normal device gate only, not the upload throttle.
	device.HandleFunc("GET /api/repairs/upload/vehicle", s.json(s.repairsUploadGetVehicle))
	// POST checks the upload throttle itself (see repairsUploadSaveVehicle)
	// so it can answer with the specific "reverify" signal the frontend
	// needs, rather than the generic 401/403 requireRepairsDevice already
	// produces for an altogether-signed-out device.
	device.HandleFunc("POST /api/repairs/upload/vehicle", s.json(s.repairsUploadSaveVehicle))
	device.HandleFunc("POST /api/repairs/upload/verify", s.json(s.repairsUploadVerify))
	protected := s.requireRepairsDevice(device)
	mux.Handle("/api/repairs/search-vehicles", protected)
	mux.Handle("/api/repairs/reg-exists", protected)
	mux.Handle("/api/repairs/history", protected)
	mux.Handle("/api/repairs/log", protected)
	mux.Handle("/api/repairs/upload/vehicle", protected)
	mux.Handle("/api/repairs/upload/verify", protected)

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
	s.serveRepairsPage(w, r, "assets/repairs/index.html")
}

// handleRepairsUploadPage is the bulk vehicle-data correction tool — a
// second page on the same site, gated by the exact same device login as
// everything else here (the upload throttle is a second, separate gate
// checked only when an actual update is submitted, not just to look at
// the page).
func (s *Server) handleRepairsUploadPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/upload" {
		http.NotFound(w, r)
		return
	}
	s.serveRepairsPage(w, r, "assets/repairs/upload.html")
}

// serveRepairsPage shows realPage once the device is authenticated, or the
// PIN screen otherwise. The browser's address bar stays on whatever URL was
// requested either way — Go is just choosing which content to answer with
// — so a successful login only has to reload the current page rather than
// know where to send the browser next.
func (s *Server) serveRepairsPage(w http.ResponseWriter, r *http.Request, realPage string) {
	page := "assets/repairs/pin.html"
	if id, ok := s.repairsAuth.ValidateDevice(r); ok {
		if active, err := s.db.RepairsDeviceActive(id); err == nil && active {
			page = realPage
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

// repairsRegExists backs the "add this as a new registration?" prompt —
// both the main log page and /upload check before treating a typed
// registration that matched nothing as fair game, so a typo doesn't
// silently start a second history for a car that already has one.
func (s *Server) repairsRegExists(r *http.Request) (any, error) {
	reg := r.URL.Query().Get("reg")
	if reg == "" {
		return nil, fail(http.StatusBadRequest, "reg is required")
	}
	exists, err := s.db.RegExists(reg)
	if err != nil {
		return nil, err
	}
	return map[string]bool{"exists": exists}, nil
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

// ── bulk vehicle-data upload ─────────────────────────────────────────────

// vehicleSpecView is the small, upload-tool-scoped read of a vehicle's
// current record — not the full store.Vehicle, which also carries spend
// figures, company assignment and invoice counts that have nothing to do
// with correcting a VIN or a tyre size from a workshop tablet.
type vehicleSpecView struct {
	Registration     string `json:"registration"`
	VIN              string `json:"vin"`
	Make             string `json:"make"`
	Model            string `json:"model"`
	Colour           string `json:"colour"`
	CylinderCapacity string `json:"cylinder_capacity"`
	SpareKeys        string `json:"spare_keys"`
	FuelType         string `json:"fuel_type"`
	EngineSize       string `json:"engine_size"`
	EngineNumber     string `json:"engine_number"`
	TyreSize         string `json:"tyre_size"`
	RadioCode        string `json:"radio_code"`
}

func (s *Server) repairsUploadGetVehicle(r *http.Request) (any, error) {
	reg := r.URL.Query().Get("reg")
	if reg == "" {
		return nil, fail(http.StatusBadRequest, "reg is required")
	}
	v, err := s.db.GetVehicle(reg)
	if err != nil {
		// Nothing on file yet is not an error here — a brand-new
		// registration just gets a blank form ready to fill in, the same
		// as the main repairs page does for a car with no history.
		return vehicleSpecView{Registration: strings.ToUpper(strings.TrimSpace(reg))}, nil
	}
	return vehicleSpecView{
		Registration: v.Registration, VIN: v.VIN, Make: v.Make, Model: v.Model, Colour: v.Colour,
		CylinderCapacity: v.CylinderCapacity, SpareKeys: v.SpareKeys, FuelType: v.FuelType,
		EngineSize: v.EngineSize, EngineNumber: v.EngineNumber, TyreSize: v.TyreSize, RadioCode: v.RadioCode,
	}, nil
}

// repairsUploadSaveVehicle overwrites a vehicle's spec fields outright —
// see store.OverwriteVehicleSpec — after checking the upload throttle
// itself, so it can answer with the specific "reverify" signal the
// frontend watches for, distinct from every other 403 this API returns.
func (s *Server) repairsUploadSaveVehicle(r *http.Request) (any, error) {
	deviceID, _ := s.repairsAuth.ValidateDevice(r) // already proven valid by requireRepairsDevice
	needsVerify, err := s.db.RepairsUploadNeedsVerify(deviceID)
	if err != nil {
		return nil, err
	}
	if needsVerify {
		return nil, fail(http.StatusForbidden, "reverify")
	}

	var body struct {
		VehicleReg       string `json:"vehicle_reg"`
		VIN              string `json:"vin"`
		Make             string `json:"make"`
		Model            string `json:"model"`
		Colour           string `json:"colour"`
		CylinderCapacity string `json:"cylinder_capacity"`
		SpareKeys        string `json:"spare_keys"`
		FuelType         string `json:"fuel_type"`
		EngineSize       string `json:"engine_size"`
		EngineNumber     string `json:"engine_number"`
		TyreSize         string `json:"tyre_size"`
		RadioCode        string `json:"radio_code"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}

	if err := s.db.OverwriteVehicleSpec(body.VehicleReg, store.VehicleSpecPatch{
		Make: body.Make, Model: body.Model, VIN: body.VIN, Colour: body.Colour,
		CylinderCapacity: body.CylinderCapacity, FuelType: body.FuelType,
		EngineSize: body.EngineSize, EngineNumber: body.EngineNumber,
		TyreSize: body.TyreSize, RadioCode: body.RadioCode, SpareKeys: body.SpareKeys,
	}); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	// Only counted once the update actually went through — a rejected or
	// failed attempt must not cost part of the budget.
	if err := s.db.RecordRepairsUpload(deviceID); err != nil {
		return nil, err
	}
	return okResponse(), nil
}

// repairsUploadVerify re-checks the PIN specifically to reset the upload
// throttle — independent of the device's own long-lived login, which this
// never touches.
//
// A wrong code here reports 403, deliberately not 401: the frontend's
// shared api() helper treats any 401 as "this device's own session has
// expired" and navigates back to the sign-in screen — correct for every
// other endpoint, wrong here, where the device is already known-good and
// only the re-typed PIN was wrong. A 403 just surfaces as an ordinary
// error message next to the boxes, the way a mistyped code should.
func (s *Server) repairsUploadVerify(r *http.Request) (any, error) {
	deviceID, _ := s.repairsAuth.ValidateDevice(r)
	var body struct {
		Code string `json:"code"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if err := s.repairsAuth.CheckPIN(r, body.Code); err != nil {
		return nil, fail(http.StatusForbidden, "%v", err)
	}
	if err := s.db.VerifyRepairsUpload(deviceID); err != nil {
		return nil, err
	}
	return okResponse(), nil
}
