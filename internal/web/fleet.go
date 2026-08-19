package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"goldstar/internal/store"
)

// routesFleet registers the company registry and the per-vehicle / per-part
// statistics endpoints.
func (s *Server) routesFleet(api *http.ServeMux) {
	api.HandleFunc("GET /api/companies", s.json(s.companies))
	api.HandleFunc("POST /api/companies", s.json(s.addCompany))
	api.HandleFunc("PATCH /api/companies/{id}", s.json(s.renameCompany))
	api.HandleFunc("DELETE /api/companies/{id}", s.json(s.deleteCompany))

	api.HandleFunc("GET /api/registry", s.json(s.registry))
	api.HandleFunc("GET /api/registry/unassigned", s.json(s.unassigned))
	api.HandleFunc("PUT /api/registry/{reg}", s.json(s.saveVehicle))
	api.HandleFunc("DELETE /api/registry/{reg}", s.json(s.deleteVehicle))

	api.HandleFunc("GET /api/vehicle/{reg}", s.json(s.vehicleStats))
	api.HandleFunc("GET /api/part/{part}", s.json(s.partStats))
}

func (s *Server) companies(r *http.Request) (any, error) { return s.db.Companies() }

func (s *Server) addCompany(r *http.Request) (any, error) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	id, err := s.db.AddCompany(body.Name)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return map[string]any{"id": id}, nil
}

func (s *Server) renameCompany(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if err := s.db.RenameCompany(id, body.Name); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) deleteCompany(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if err := s.db.DeleteCompany(id); err != nil {
		// Refusing because vehicles are still attached is a user error, not a
		// server fault, so it deserves a 409 the UI can show verbatim.
		return nil, fail(http.StatusConflict, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) registry(r *http.Request) (any, error) {
	companyID, _ := strconv.ParseInt(r.URL.Query().Get("company"), 10, 64)
	return s.db.RegisteredVehicles(companyID)
}

func (s *Server) unassigned(r *http.Request) (any, error) { return s.db.UnassignedVehicles() }

func (s *Server) saveVehicle(r *http.Request) (any, error) {
	reg := r.PathValue("reg")
	var p store.VehiclePatch
	if err := decode(r, &p); err != nil {
		return nil, err
	}
	if err := s.db.SaveVehicle(reg, p); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return s.db.GetVehicle(reg)
}

func (s *Server) deleteVehicle(r *http.Request) (any, error) {
	if err := s.db.DeleteVehicle(r.PathValue("reg")); err != nil {
		return nil, err
	}
	return okResponse(), nil
}

func (s *Server) vehicleStats(r *http.Request) (any, error) {
	return s.db.VehicleStats(r.PathValue("reg"))
}

func (s *Server) partStats(r *http.Request) (any, error) {
	return s.db.PartStats(r.PathValue("part"))
}

// ── shared helpers ────────────────────────────────────────────────────────

// decode reads a JSON body with a size cap, so a malformed or hostile request
// cannot exhaust memory.
func decode(r *http.Request, dst any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst); err != nil {
		return fail(http.StatusBadRequest, "bad JSON: %v", err)
	}
	return nil
}

func okResponse() map[string]bool { return map[string]bool{"ok": true} }
