package web

import (
	"net/http"

	"goldstar/internal/repairsauth"
)

// routesRepairsAdmin registers the dashboard-side management for the
// repairs log — everything here sits behind the normal dashboard auth
// (password + 2FA), same as the rest of /api/admin/*. It has nothing to do
// with repairsauth itself; an admin managing the repairs site does so from
// the main dashboard, not from repairs.<domain>.
func (s *Server) routesRepairsAdmin(api *http.ServeMux) {
	api.HandleFunc("GET /api/admin/repairs-devices", s.json(s.listRepairsDevices))
	api.HandleFunc("POST /api/admin/repairs-devices/revoke", s.json(s.revokeRepairsDevice))
	api.HandleFunc("GET /api/admin/repairs-recent", s.json(s.recentRepairsAdmin))
	api.HandleFunc("POST /api/admin/repairs-pin", s.json(s.changeRepairsPIN))
}

func (s *Server) listRepairsDevices(r *http.Request) (any, error) {
	return s.db.ListRepairsDevices()
}

func (s *Server) revokeRepairsDevice(r *http.Request) (any, error) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if body.ID == "" {
		return nil, fail(http.StatusBadRequest, "id is required")
	}
	if err := s.db.RevokeRepairsDevice(body.ID); err != nil {
		return nil, err
	}
	return okResponse(), nil
}

func (s *Server) recentRepairsAdmin(r *http.Request) (any, error) {
	return s.db.RecentRepairs(200)
}

// changeRepairsPIN doesn't ask for the current PIN first, the same as the
// parts site before it — this is a shared code for a shelf, not an
// individual account, and reaching this endpoint already means the caller
// passed the dashboard's own password-and-2FA login.
//
// Hashes the PIN itself, right here, before it ever reaches either the file
// or the in-memory Auth — so repairs.pin never has the raw digits written
// to it even for an instant, the same as how the dashboard's own
// password-change flow hashes before persisting.
func (s *Server) changeRepairsPIN(r *http.Request) (any, error) {
	var body struct {
		PIN string `json:"pin"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if len(body.PIN) < 4 || len(body.PIN) > 12 {
		return nil, fail(http.StatusBadRequest, "PIN should be 4-12 digits")
	}
	hash, err := repairsauth.HashPIN(body.PIN)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	if err := s.cfg.WriteRepairsPIN(hash); err != nil {
		return nil, err
	}
	s.repairsAuth.SetPIN(hash)
	return map[string]any{"ok": true, "message": "PIN changed"}, nil
}
