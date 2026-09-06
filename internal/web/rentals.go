package web

import (
	"io/fs"
	"net/http"
	"strconv"

	"goldstar/internal/store"
)

// rentalsRoutes builds the site served at rentals.<domain> — the hire desk:
// who has which car, until when, what is free, and who to call.
//
// Unlike repairs.<domain>, which shares one PIN across a workshop tablet,
// this uses the dashboard's own named accounts. Hires carry customer names,
// phone numbers and addresses, and later card payments; "which of us handed
// out that car" is a question a shared code can never answer. Reusing the
// same handlers costs nothing: cookies are scoped to the host that set
// them, so signing in here is its own session on the same accounts, with
// no change to the dashboard's auth at all.
func (s *Server) rentalsRoutes(sub fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(sub)))
	mux.HandleFunc("GET /{$}", s.handleRentalsRoot)

	// The whole sign-in flow, verbatim — password, forced change, 2FA setup
	// and verify — so login.html works here unmodified.
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/login/change-password", s.handleChangePendingPassword)
	mux.HandleFunc("POST /api/login/totp/setup", s.handleTOTPSetup)
	mux.HandleFunc("POST /api/login/totp/confirm", s.handleTOTPConfirm)
	mux.HandleFunc("POST /api/login/totp/verify", s.handleTOTPVerify)

	// Everything below is behind the same Protect the dashboard's own API
	// uses, so it inherits all of it: 401 without a session, CSRF on every
	// mutating call, and a read-only account blocked from changing anything.
	api := http.NewServeMux()
	api.HandleFunc("GET /api/rentals/overview", s.json(s.rentalsOverview))

	api.HandleFunc("GET /api/rentals/customers", s.json(s.rentalCustomers))
	api.HandleFunc("POST /api/rentals/customers", s.json(s.addRentalCustomer))
	api.HandleFunc("PATCH /api/rentals/customers/{id}", s.json(s.updateRentalCustomer))
	api.HandleFunc("DELETE /api/rentals/customers/{id}", s.json(s.deleteRentalCustomer))

	api.HandleFunc("GET /api/rentals/vehicles", s.json(s.rentalVehicles))
	api.HandleFunc("POST /api/rentals/vehicles", s.json(s.addRentalVehicle))
	api.HandleFunc("PATCH /api/rentals/vehicles/{id}", s.json(s.updateRentalVehicle))
	api.HandleFunc("DELETE /api/rentals/vehicles/{id}", s.json(s.deleteRentalVehicle))
	api.HandleFunc("GET /api/rentals/available", s.json(s.availableRentalVehicles))

	api.HandleFunc("GET /api/rentals/agreements", s.json(s.rentalAgreements))
	api.HandleFunc("POST /api/rentals/agreements", s.json(s.createRentalAgreement))
	api.HandleFunc("PATCH /api/rentals/agreements/{id}/status", s.json(s.setRentalAgreementStatus))
	api.HandleFunc("DELETE /api/rentals/agreements/{id}", s.json(s.deleteRentalAgreement))

	mux.Handle("/api/rentals/", s.auth.Protect(api))
	return mux
}

// handleRentalsRoot serves the hire desk to a signed-in session and the
// ordinary login page to anyone else — the same shape as the dashboard's
// own handleRoot, including stamping the account's role and read-only
// state onto <body> so the page can gate itself without a second request.
func (s *Server) handleRentalsRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.serveAppPage(w, r, "assets/rentals/index.html")
}

func (s *Server) rentalsOverview(r *http.Request) (any, error) { return s.db.RentalOverview() }

// ── customers ─────────────────────────────────────────────────────────────

func (s *Server) rentalCustomers(r *http.Request) (any, error) {
	return s.db.RentalCustomers(r.URL.Query().Get("q"))
}

func (s *Server) addRentalCustomer(r *http.Request) (any, error) {
	var c store.RentalCustomer
	if err := decode(r, &c); err != nil {
		return nil, err
	}
	id, err := s.db.AddRentalCustomer(c)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return map[string]any{"id": id}, nil
}

func (s *Server) updateRentalCustomer(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var c store.RentalCustomer
	if err := decode(r, &c); err != nil {
		return nil, err
	}
	if err := s.db.UpdateRentalCustomer(id, c); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) deleteRentalCustomer(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if err := s.db.DeleteRentalCustomer(id); err != nil {
		// Refusing because hires still reference them is a user-level
		// answer, not a server fault — 409 so the UI can show it verbatim.
		return nil, fail(http.StatusConflict, "%v", err)
	}
	return okResponse(), nil
}

// ── the hire pool ─────────────────────────────────────────────────────────

func (s *Server) rentalVehicles(r *http.Request) (any, error) { return s.db.RentalVehicles() }

func (s *Server) addRentalVehicle(r *http.Request) (any, error) {
	var v store.RentalVehicle
	if err := decode(r, &v); err != nil {
		return nil, err
	}
	id, err := s.db.AddRentalVehicle(v)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return map[string]any{"id": id}, nil
}

func (s *Server) updateRentalVehicle(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var v store.RentalVehicle
	if err := decode(r, &v); err != nil {
		return nil, err
	}
	if err := s.db.UpdateRentalVehicle(id, v); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) deleteRentalVehicle(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if err := s.db.DeleteRentalVehicle(id); err != nil {
		return nil, fail(http.StatusConflict, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) availableRentalVehicles(r *http.Request) (any, error) {
	v := r.URL.Query()
	cars, err := s.db.AvailableRentalVehicles(v.Get("from"), v.Get("to"))
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return cars, nil
}

// ── hires ─────────────────────────────────────────────────────────────────

func (s *Server) rentalAgreements(r *http.Request) (any, error) {
	v := r.URL.Query()
	vehicleID, _ := strconv.ParseInt(v.Get("vehicle"), 10, 64)
	customerID, _ := strconv.ParseInt(v.Get("customer"), 10, 64)
	return s.db.RentalAgreements(store.RentalAgreementsFilter{
		Status:     v.Get("status"),
		VehicleID:  vehicleID,
		CustomerID: customerID,
		OnDate:     v.Get("on"),
	})
}

func (s *Server) createRentalAgreement(r *http.Request) (any, error) {
	var a store.RentalAgreement
	if err := decode(r, &a); err != nil {
		return nil, err
	}
	id, err := s.db.CreateRentalAgreement(a)
	if err != nil {
		// A double-booking is the expected refusal here, not a fault: 409,
		// so the desk sees "already booked over those dates" as itself.
		return nil, fail(http.StatusConflict, "%v", err)
	}
	return map[string]any{"id": id}, nil
}

func (s *Server) setRentalAgreementStatus(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Status     string `json:"status"`
		ReturnedOn string `json:"returned_on"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if err := s.db.SetRentalAgreementStatus(id, body.Status, body.ReturnedOn); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) deleteRentalAgreement(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if err := s.db.DeleteRentalAgreement(id); err != nil {
		return nil, err
	}
	return okResponse(), nil
}
