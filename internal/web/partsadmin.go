package web

import (
	"net/http"
)

// routesPartsAdmin registers the dashboard-side management for the parts
// counter — everything here sits behind the normal dashboard auth
// (password + 2FA), same as the rest of /api/admin/*. It has nothing to do
// with partsauth or the IP allow-list; an admin managing the parts site does
// so from the main dashboard, not from parts.<domain> itself.
func (s *Server) routesPartsAdmin(api *http.ServeMux) {
	api.HandleFunc("GET /api/admin/parts-ips", s.json(s.listAllowedIPs))
	api.HandleFunc("POST /api/admin/parts-ips", s.json(s.addAllowedIP))
	api.HandleFunc("DELETE /api/admin/parts-ips", s.json(s.removeAllowedIP))

	api.HandleFunc("GET /api/admin/parts-devices", s.json(s.listPartsDevices))
	api.HandleFunc("POST /api/admin/parts-devices/revoke", s.json(s.revokePartsDevice))

	api.HandleFunc("GET /api/admin/parts-takes", s.json(s.recentStockTakes))
	api.HandleFunc("POST /api/admin/parts-pin", s.json(s.changePartsPIN))

	api.HandleFunc("GET /api/admin/parts-catalog", s.json(s.listManualParts))
	api.HandleFunc("POST /api/admin/parts-catalog", s.json(s.addManualPart))
	api.HandleFunc("DELETE /api/admin/parts-catalog", s.json(s.removeManualPart))

	api.HandleFunc("GET /api/admin/parts-photo/{part}", s.servePartPhoto)
	api.HandleFunc("POST /api/admin/parts-photo/{part}", s.json(s.uploadPartPhoto))
}

func (s *Server) listAllowedIPs(r *http.Request) (any, error) {
	return s.db.ListAllowedIPs()
}

func (s *Server) addAllowedIP(r *http.Request) (any, error) {
	var body struct {
		IP    string `json:"ip"`
		Label string `json:"label"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if err := s.db.AddAllowedIP(body.IP, body.Label); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) removeAllowedIP(r *http.Request) (any, error) {
	ip := r.URL.Query().Get("ip")
	if ip == "" {
		return nil, fail(http.StatusBadRequest, "ip is required")
	}
	if err := s.db.RemoveAllowedIP(ip); err != nil {
		return nil, err
	}
	return okResponse(), nil
}

func (s *Server) listPartsDevices(r *http.Request) (any, error) {
	return s.db.ListPartsDevices()
}

func (s *Server) revokePartsDevice(r *http.Request) (any, error) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if body.ID == "" {
		return nil, fail(http.StatusBadRequest, "id is required")
	}
	if err := s.db.RevokePartsDevice(body.ID); err != nil {
		return nil, err
	}
	return okResponse(), nil
}

func (s *Server) recentStockTakes(r *http.Request) (any, error) {
	return s.db.RecentStockTakes(200)
}

func (s *Server) listManualParts(r *http.Request) (any, error) {
	return s.db.ListManualParts()
}

// addManualPart is "add a part to the list" from the main dashboard — for
// something kept on the shelf that hasn't gone through a tracked supplier
// invoice yet. Calling it again for a part number that's already registered
// edits it in place.
func (s *Server) addManualPart(r *http.Request) (any, error) {
	var body struct {
		PartNumber    string  `json:"part_number"`
		Description   string  `json:"description"`
		StartingStock float64 `json:"starting_stock"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if err := s.db.AddManualPart(body.PartNumber, body.Description, body.StartingStock); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) removeManualPart(r *http.Request) (any, error) {
	part := r.URL.Query().Get("part")
	if part == "" {
		return nil, fail(http.StatusBadRequest, "part is required")
	}
	if err := s.db.RemoveManualPart(part); err != nil {
		return nil, err
	}
	return okResponse(), nil
}

// changePartsPIN doesn't ask for the current PIN first the way the
// dashboard password change does — this is a shared code for a shelf, not
// an individual account, and reaching this endpoint already means the
// caller passed the dashboard's own password-and-2FA login.
func (s *Server) changePartsPIN(r *http.Request) (any, error) {
	var body struct {
		PIN string `json:"pin"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if len(body.PIN) < 4 || len(body.PIN) > 12 {
		return nil, fail(http.StatusBadRequest, "PIN should be 4-12 digits")
	}
	if err := s.cfg.WritePartsPIN(body.PIN); err != nil {
		return nil, err
	}
	s.partsAuth.SetPIN(body.PIN)
	return map[string]any{"ok": true, "message": "PIN changed"}, nil
}
