package web

import (
	"database/sql"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"

	"goldstar/internal/store"
)

// partsRoutes builds the site served at parts.<domain> — the parts store
// counter. A barcode scanner is a keyboard, so the whole thing is driven
// from one field: scan a box, see what it is and how many are on the
// shelf, add or take some off.
//
// Same accounts as the dashboard, for the same reason rentals uses them:
// every movement records who made it, and a shared PIN could not answer
// "who took the last set of pads".
func (s *Server) partsRoutes(sub fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(sub)))
	mux.HandleFunc("GET /{$}", s.handlePartsRoot)

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/login/change-password", s.handleChangePendingPassword)
	mux.HandleFunc("POST /api/login/totp/setup", s.handleTOTPSetup)
	mux.HandleFunc("POST /api/login/totp/confirm", s.handleTOTPConfirm)
	mux.HandleFunc("POST /api/login/totp/verify", s.handleTOTPVerify)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/stock/overview", s.json(s.stockOverview))
	api.HandleFunc("GET /api/stock/parts", s.json(s.stockParts))
	api.HandleFunc("POST /api/stock/parts", s.json(s.addStockPart))
	api.HandleFunc("GET /api/stock/parts/{id}", s.json(s.stockPart))
	api.HandleFunc("PATCH /api/stock/parts/{id}", s.json(s.updateStockPart))
	api.HandleFunc("DELETE /api/stock/parts/{id}", s.json(s.deleteStockPart))
	api.HandleFunc("GET /api/stock/parts/{id}/movements", s.json(s.stockPartMovements))
	api.HandleFunc("POST /api/stock/parts/{id}/adjust", s.json(s.adjustStock))
	api.HandleFunc("GET /api/stock/scan", s.json(s.scanStock))
	api.HandleFunc("GET /api/stock/movements", s.json(s.recentStockMovements))

	mux.Handle("/api/stock/", s.auth.Protect(api))
	return mux
}

func (s *Server) handlePartsRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.serveAppPage(w, r, "assets/parts/index.html")
}

// serveAppPage is handleRoot's body, extracted: serve this page to a
// signed-in session and the ordinary login page to anyone else, with the
// account's role and read-only state stamped onto <body> either way. Three
// sites now need exactly this, and three copies of it would be three
// places to forget the read-only attribute.
func (s *Server) serveAppPage(w http.ResponseWriter, r *http.Request, page string) {
	authed := s.auth.IsAuthenticated(r)
	if !authed {
		page = "assets/login.html"
	}
	b, err := assets.ReadFile(page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if authed {
		role := store.RoleAdmin
		temp, readOnly := false, false
		if u, ok := s.auth.CurrentUser(r); ok {
			role = u.Role
			temp = u.TOTPExempt
			readOnly = u.ReadOnly
		}
		attrs := `data-role="` + role + `"`
		if temp {
			attrs += ` data-temp="true"`
		}
		if readOnly {
			attrs += ` data-readonly="true"`
		}
		// Where the sibling sites live, worked out from the host this
		// request actually arrived on — so a link between them is correct
		// on any domain, and on a local machine, without anything being
		// configured anywhere.
		attrs += ` data-rentals="` + siblingHost(r.Host, "rentals") + `"`
		attrs += ` data-parts="` + siblingHost(r.Host, "parts") + `"`
		b = []byte(strings.Replace(string(b), "<body>", "<body "+attrs+">", 1))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(versionAssets(b))
}

func (s *Server) stockOverview(r *http.Request) (any, error) { return s.db.StockOverview() }

func (s *Server) stockParts(r *http.Request) (any, error) {
	return s.db.StockParts(r.URL.Query().Get("q"))
}

func (s *Server) stockPart(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.db.StockPart(id)
}

// scanStock is what the scanner's Enter key triggers. An unknown barcode is
// a 404 rather than an error: the counter screen turns that into "not on
// file — add it", which is a normal thing to happen with a new box.
func (s *Server) scanStock(r *http.Request) (any, error) {
	p, err := s.db.StockPartByBarcode(r.URL.Query().Get("barcode"))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fail(http.StatusNotFound, "that barcode is not on file yet")
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Server) addStockPart(r *http.Request) (any, error) {
	var p store.StockPart
	if err := decode(r, &p); err != nil {
		return nil, err
	}
	id, err := s.db.AddStockPart(p)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return map[string]any{"id": id}, nil
}

func (s *Server) updateStockPart(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var p store.StockPart
	if err := decode(r, &p); err != nil {
		return nil, err
	}
	if err := s.db.UpdateStockPart(id, p); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) deleteStockPart(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if err := s.db.DeleteStockPart(id); err != nil {
		return nil, err
	}
	return okResponse(), nil
}

func (s *Server) stockPartMovements(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return s.db.StockMovements(id, limit)
}

func (s *Server) recentStockMovements(r *http.Request) (any, error) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return s.db.RecentStockMovements(limit)
}

// adjustStock is the counter's add/take action. The signed-in account's own
// username is stamped on the movement here rather than taken from the
// request body — who did something is not a thing the client gets to claim.
func (s *Server) adjustStock(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Delta      float64 `json:"delta"`
		Reason     string  `json:"reason"`
		VehicleReg string  `json:"vehicle_reg"`
		Note       string  `json:"note"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}

	by := ""
	if u, ok := s.auth.CurrentUser(r); ok {
		by = u.Username
	}

	p, err := s.db.AdjustStock(id, body.Delta, body.Reason, body.VehicleReg, by, body.Note)
	if err != nil {
		// A part that does not fit the car is a refusal, not a malformed
		// request — 409, so the counter can show the reason verbatim.
		var fe store.FitmentError
		if errors.As(err, &fe) {
			return nil, fail(http.StatusConflict, "%v", err)
		}
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return p, nil
}

// siblingHost rewrites the host a request arrived on into the equivalent
// for another of the sites this process serves: www.example.co.uk and a
// bare example.co.uk both give parts.example.co.uk, and being on one
// sibling gives another. Protocol-relative on purpose — behind a tunnel
// the origin request is plain HTTP while the page the browser has is
// HTTPS, so inheriting the page's own scheme is the only correct answer.
func siblingHost(host, label string) string {
	h, port := host, ""
	if hh, pp, err := net.SplitHostPort(host); err == nil {
		h, port = hh, ":"+pp
	}
	for _, p := range []string{"www.", "repairs.", "rentals.", "parts."} {
		if strings.HasPrefix(h, p) {
			h = strings.TrimPrefix(h, p)
			break
		}
	}
	return "//" + label + "." + h + port
}
