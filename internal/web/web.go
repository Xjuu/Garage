// Package web serves the dashboard: a JSON API plus a single-page front end
// embedded in the binary, so deployment stays one file.
package web

import (
	"context"
	"database/sql"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goldstar/internal/auth"
	"goldstar/internal/config"
	"goldstar/internal/export"
	"goldstar/internal/jobs"
	"goldstar/internal/pipeline"
	"goldstar/internal/store"
)

//go:embed assets
var assets embed.FS

// maxUploadBytes caps a single dashboard upload. Gemini will not accept much
// more inline anyway.
const maxUploadBytes = 25 << 20

type Server struct {
	cfg     *config.Config
	db      *store.Store
	auth    *auth.Auth
	jobs    *jobs.Runner
	exports *export.Catalogue
	sched   *scheduler
	logs    *logBuffer
}

func New(cfg *config.Config, db *store.Store) (*Server, error) {
	// A ring buffer alongside stderr, so the Admin page can show the
	// last few hundred lines the server has logged (scheduled syncs,
	// failures, startup) without needing SSH access to read the
	// journal. Temporary: a real log viewer would page through the
	// journal itself; this is the quick version.
	logs := newLogBuffer(500)
	log.SetOutput(io.MultiWriter(os.Stderr, logs))

	a, err := auth.New(cfg.PasswordHash, cfg.SessionKeyPath(), cfg.CookieSecure)
	if err != nil {
		return nil, err
	}
	a.SetIdentity(cfg.User, cfg.Email)
	// A password is required unless explicitly waived. Checking the bind
	// address is not enough: cloudflared connects over loopback, so a tunnel
	// publishes a 127.0.0.1 listener to the whole internet.
	if !a.Configured() && !cfg.AllowNoPassword {
		return nil, fmt.Errorf(
			"refusing to serve without a password.\n" +
				"  Run `goldstar passwd` and put the printed line in your .env.\n" +
				"  If this really is a private machine with no tunnel, set GOLDSTAR_ALLOW_NO_PASSWORD=true.")
	}
	if !a.Configured() && !cfg.LoopbackOnly() {
		return nil, fmt.Errorf(
			"refusing to bind %s without a password: that address is reachable from the network", cfg.WebAddr)
	}
	return &Server{
		cfg: cfg, db: db, auth: a, jobs: jobs.New(),
		exports: export.NewCatalogue(cfg.ExportsDir()),
		logs:    logs,
	}, nil
}

// Listen serves until ctx is cancelled, then shuts down cleanly.
//
// The clean shutdown is not cosmetic. Without it the process ignores SIGTERM
// entirely — signal.NotifyContext removes Go's default terminate behaviour, so
// a captured-but-unwatched signal leaves the process running until systemd
// gives up after 90 seconds and sends SIGKILL. A hard kill mid-write is
// exactly the situation a database should never be put in.
func (s *Server) Listen(ctx context.Context, addr string) error {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		return err
	}

	// The daily mailbox pull runs for as long as the dashboard does. A bad
	// time or zone is reported and skipped rather than refusing to start: the
	// dashboard is still useful without the timer.
	sched, err := newScheduler(s.cfg, s)
	if err != nil {
		log.Printf("automatic sync disabled: %v", err)
	} else if sched != nil {
		s.sched = sched
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go sched.run(ctx)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(sub)))
	// "/{$}" matches only the exact root; a bare "GET /" would conflict with
	// the "/api/" subtree registration below.
	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/overview", s.json(s.overview))
	api.HandleFunc("GET /api/invoices", s.json(s.invoices))
	api.HandleFunc("GET /api/invoices/{id}", s.json(s.invoice))
	api.HandleFunc("PATCH /api/invoices/{id}", s.json(s.patchInvoice))
	api.HandleFunc("DELETE /api/invoices/{id}", s.json(s.deleteInvoice))
	api.HandleFunc("GET /api/vehicles", s.json(s.vehicles))
	api.HandleFunc("GET /api/suppliers", s.json(s.suppliers))
	api.HandleFunc("GET /api/parts", s.json(s.parts))
	api.HandleFunc("GET /api/months", s.json(s.months))
	api.HandleFunc("GET /api/filters", s.json(s.filters))
	api.HandleFunc("GET /api/search", s.json(s.globalSearch))
	api.HandleFunc("GET /api/spending", s.json(s.spending))
	api.HandleFunc("GET /api/schedule", s.json(s.scheduleStatus))
	api.HandleFunc("GET /api/ping", s.ping)
	api.HandleFunc("GET /api/invoices/{id}/file", s.invoiceFile)
	api.HandleFunc("GET /api/job", s.json(s.jobStatus))
	api.HandleFunc("POST /api/job/cancel", s.json(s.jobCancel))
	api.HandleFunc("POST /api/fetch", s.json(s.startFetch))
	api.HandleFunc("POST /api/upload", s.json(s.upload))
	api.HandleFunc("GET /api/export.xlsx", s.exportXLSX)
	api.HandleFunc("GET /api/export.csv", s.exportCSV)
	s.routeExports(api)
	s.routeIcons(api)
	api.HandleFunc("GET /api/doc", s.serveDoc)
	s.routesFleet(api)
	s.routesTraining(api)
	s.routesAdmin(api)
	mux.Handle("/api/", s.auth.Protect(api))

	srv := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	invoices, _, err := s.db.Counts()
	if err != nil {
		return err
	}

	// Claim the port before advertising it, so a failed bind reports the
	// error instead of printing a URL that was never served.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", addr, err)
	}

	fmt.Printf("\n  Goldstar dashboard\n  → http://%s\n\n", addr)
	fmt.Printf("  %d invoice(s) stored. Nothing is sent to Gemini until you press\n", invoices)
	fmt.Printf("  Sync mailbox or drop a file in — browsing is free.\n")
	if invoices == 0 {
		fmt.Printf("\n  The database is empty, so most views will be blank.\n")
		fmt.Printf("  Drag an invoice PDF onto the Invoices tab to fill it.\n")
	}
	if !s.auth.Configured() {
		fmt.Printf("\n  WARNING: NO PASSWORD SET (GOLDSTAR_ALLOW_NO_PASSWORD is on).\n")
		fmt.Printf("  Anyone who can reach this address can read and delete every invoice.\n")
	} else if !s.cfg.CookieSecure {
		fmt.Printf("\n  Note: set GOLDSTAR_COOKIE_SECURE=true before serving through a tunnel.\n")
	}
	fmt.Printf("\n  Ctrl-C to stop.\n\n")
	// Serve in the background so this goroutine can wait on the signal.
	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errc <- err
		}
		close(errc)
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Print("shutting down")
	// Bounded, so a wedged request cannot hold the process past systemd's
	// patience and turn a clean stop back into a SIGKILL.
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}

	// Checkpoint the write-ahead log so the database file is complete on disk
	// rather than depending on recovery at next start.
	if err := s.db.Checkpoint(); err != nil {
		log.Printf("wal checkpoint: %v", err)
	}
	return nil
}

// securityHeaders sets a strict CSP. The front end ships no inline scripts, so
// no unsafe-inline is needed for JS.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; "+
				"object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// ── plumbing ──────────────────────────────────────────────────────────────

type handler func(*http.Request) (any, error)

type httpError struct {
	code int
	msg  string
}

func (e httpError) Error() string { return e.msg }

func fail(code int, format string, args ...any) error {
	return httpError{code: code, msg: fmt.Sprintf(format, args...)}
}

// json adapts a handler that returns a value into an HTTP handler, so each
// endpoint below deals only in data and errors.
func (s *Server) json(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := h(r)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			var he httpError
			code := http.StatusInternalServerError
			if errors.As(err, &he) {
				code = he.code
			}
			if errors.Is(err, sql.ErrNoRows) {
				code = http.StatusNotFound
			}
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(v)
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page := "assets/index.html"
	if !s.auth.IsAuthenticated(r) {
		page = "assets/login.html"
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

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := s.auth.Login(w, r, body.User, body.Password); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.Logout(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{
		"authenticated": s.auth.IsAuthenticated(r),
		"password_set":  s.auth.Configured(),
	})
}

// ── data endpoints ────────────────────────────────────────────────────────

// overview carries the month-to-date figures alongside the totals, so the
// front page answers "what have we spent this month" without a second request.
func (s *Server) overview(r *http.Request) (any, error) {
	o, err := s.db.Overview()
	if err != nil {
		return nil, err
	}
	month, err := s.db.ThisMonth(time.Now())
	if err != nil {
		return nil, err
	}
	return map[string]any{"overview": o, "this_month": month}, nil
}
func (s *Server) vehicles(r *http.Request) (any, error)  { return s.db.Vehicles() }
func (s *Server) suppliers(r *http.Request) (any, error) { return s.db.Suppliers() }
func (s *Server) parts(r *http.Request) (any, error)     { return s.db.Parts() }
func (s *Server) months(r *http.Request) (any, error)    { return s.db.Months() }

// invoiceFile serves the original attachment straight off disk. Every invoice
// is kept locally when it is first ingested, so opening one costs nothing and
// needs no API key — the model is only ever called once per document.
func (s *Server) invoiceFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	inv, err := s.db.Get(id)
	if err != nil {
		http.Error(w, "no such invoice", http.StatusNotFound)
		return
	}

	// Serve only from inside the attachments folder, whatever the stored path
	// claims, so a tampered row cannot read arbitrary files.
	root, err := filepath.Abs(s.cfg.AttachmentsDir())
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	path, err := filepath.Abs(inv.SourceFile)
	if err != nil || !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		http.Error(w, "original is not stored locally", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "the original file is missing from disk", http.StatusNotFound)
		return
	}

	// inline, so a PDF opens in the browser's viewer rather than downloading.
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(filepath.Base(path)))
	http.ServeFile(w, r, path)
}

// ping answers as cheaply as possible: no database, no work, just enough to
// prove the round trip. Anything it did would be measured as latency and
// misreported as network time.
func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Must never be cached, or the reading is of the cache rather than the link.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Write([]byte(`{"ok":true}`))
}

// scheduleStatus reports the daily timer so the dashboard can show when the
// next automatic sync happens.
func (s *Server) scheduleStatus(r *http.Request) (any, error) {
	return s.sched.Status(), nil
}

// spending backs the Spending tab: a window, the window before it for
// comparison, a daily series and a day-by-day breakdown.
func (s *Server) spending(r *http.Request) (any, error) {
	v := r.URL.Query()
	t, err := s.db.Spending(store.TrendQuery{
		Period:  v.Get("period"),
		From:    v.Get("from"),
		To:      v.Get("to"),
		Vehicle: v.Get("reg"),
		Scope:   v.Get("scope"),
	})
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return t, nil
}

// globalSearch backs the header search bar: one query across invoices,
// vehicles, parts and suppliers, narrowed by the same date range.
func (s *Server) globalSearch(r *http.Request) (any, error) {
	v := r.URL.Query()
	return s.db.GlobalSearch(v.Get("q"), v.Get("from"), v.Get("to"))
}

func (s *Server) filters(r *http.Request) (any, error) {
	suppliers, regs, err := s.db.DistinctValues()
	if err != nil {
		return nil, err
	}
	return map[string]any{"suppliers": suppliers, "vehicles": regs}, nil
}

func queryFrom(r *http.Request) store.Query {
	v := r.URL.Query()
	page, _ := strconv.Atoi(v.Get("page"))
	per, _ := strconv.Atoi(v.Get("per"))
	return store.Query{
		Text:       v.Get("q"),
		From:       v.Get("from"),
		To:         v.Get("to"),
		Supplier:   v.Get("supplier"),
		VehicleReg: v.Get("reg"),
		Review:     v.Get("review"),
		Scope:      v.Get("scope"),
		Sort:       v.Get("sort"),
		Dir:        v.Get("dir"),
		Page:       page,
		PerPage:    per,
	}
}

func (s *Server) invoices(r *http.Request) (any, error) {
	page, err := s.db.Search(queryFrom(r))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"invoices": page.Invoices,
		"total":    page.Total,
		"netto":    page.Netto,
		"vat":      page.VAT,
		"brutto":   page.Brutto,
	}, nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, fail(http.StatusBadRequest, "bad id")
	}
	return id, nil
}

func (s *Server) invoice(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.db.Get(id)
}

func (s *Server) patchInvoice(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var p store.Patch
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
		return nil, fail(http.StatusBadRequest, "bad JSON: %v", err)
	}
	if err := s.db.Update(id, p); err != nil {
		return nil, err
	}
	return s.db.Get(id)
}

func (s *Server) deleteInvoice(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	file, err := s.db.Delete(id)
	if err != nil {
		return nil, err
	}
	// The archived PDF stays on disk on purpose — see store.Delete.
	return map[string]any{"ok": true, "archived_file_kept": filepath.Base(file)}, nil
}

// ── jobs ──────────────────────────────────────────────────────────────────

func (s *Server) jobStatus(r *http.Request) (any, error) { return s.jobs.Status(), nil }

func (s *Server) jobCancel(r *http.Request) (any, error) {
	s.jobs.Cancel()
	return map[string]bool{"ok": true}, nil
}

// startFetch is the button: it returns immediately and the browser polls
// /api/job for progress.
func (s *Server) startFetch(r *http.Request) (any, error) {
	if err := s.cfg.RequireMail(); err != nil {
		return nil, fail(http.StatusPreconditionFailed, "%v", err)
	}
	err := s.jobs.Start("fetch", func(ctx context.Context, logLine func(string)) (string, error) {
		logf := pipeline.LogFunc(func(format string, args ...any) {
			logLine(fmt.Sprintf(format, args...))
		})
		st, err := pipeline.Fetch(ctx, s.cfg, s.db, logf)
		if st == nil {
			return "", err
		}
		logLine(st.Summary())
		return st.Summary(), err
	})
	if err != nil {
		return nil, fail(http.StatusConflict, "%v", err)
	}
	return map[string]bool{"started": true}, nil
}

// upload accepts dropped files, saves them to a temp dir, and ingests them
// through the same path as mail attachments.
func (s *Server) upload(r *http.Request) (any, error) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		return nil, fail(http.StatusBadRequest, "bad upload: %v", err)
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		return nil, fail(http.StatusBadRequest, "no files")
	}

	tmp, err := os.MkdirTemp("", "goldstar-upload-*")
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, fh := range files {
		if fh.Size > maxUploadBytes {
			os.RemoveAll(tmp)
			return nil, fail(http.StatusRequestEntityTooLarge, "%s is too large", fh.Filename)
		}
		src, err := fh.Open()
		if err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		// filepath.Base defeats a "../.." filename from a crafted client.
		dst := filepath.Join(tmp, filepath.Base(fh.Filename))
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			src.Close()
			os.RemoveAll(tmp)
			return nil, err
		}
		_, err = io.Copy(out, io.LimitReader(src, maxUploadBytes))
		src.Close()
		out.Close()
		if err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		paths = append(paths, dst)
	}

	err = s.jobs.Start("upload", func(ctx context.Context, logLine func(string)) (string, error) {
		defer os.RemoveAll(tmp)
		logf := pipeline.LogFunc(func(format string, args ...any) {
			logLine(fmt.Sprintf(format, args...))
		})
		st, err := pipeline.IngestFile(ctx, s.cfg, s.db, paths, logf)
		if st == nil {
			return "", err
		}
		logLine(st.Summary())
		return st.Summary(), err
	})
	if err != nil {
		os.RemoveAll(tmp)
		return nil, fail(http.StatusConflict, "%v", err)
	}
	return map[string]any{"started": true, "files": len(paths)}, nil
}

// ── exports ───────────────────────────────────────────────────────────────

// exportXLSX honours the same filters as the table, so a download matches
// what is on screen.
func (s *Server) exportXLSX(w http.ResponseWriter, r *http.Request) {
	invoices, err := s.db.AllMatching(queryFrom(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	path := export.DefaultPath(s.cfg.ExportsDir(), time.Now())
	if _, err := export.Write(path, invoices); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(path)))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, path)
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	invoices, err := s.db.AllMatching(queryFrom(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := fmt.Sprintf("goldstar-items-%s.csv", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")

	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{"invoice_date", "supplier", "invoice_number", "vehicle_reg", "part_number",
		"description", "quantity", "unit_price", "netto", "vat_rate", "vat", "brutto", "currency"})

	for _, inv := range invoices {
		for _, it := range inv.Items {
			reg := it.VehicleReg
			if reg == "" {
				reg = inv.VehicleReg
			}
			cw.Write([]string{
				inv.InvoiceDate, inv.Supplier, inv.InvoiceNumber, reg, it.PartNumber, it.Desc,
				num(it.Quantity), num(it.UnitPrice), num(it.Netto), num(it.VATRate),
				num(it.VATAmount), num(it.Brutto), inv.Currency,
			})
		}
	}
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// serveDoc streams an archived original. The path comes from the database and
// is checked against the attachments directory, never taken from the URL.
func (s *Server) serveDoc(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	inv, err := s.db.Get(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	abs, err := filepath.Abs(inv.SourceFile)
	root, rerr := filepath.Abs(s.cfg.AttachmentsDir())
	if err != nil || rerr != nil || !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := os.Stat(abs); err != nil {
		http.Error(w, "archived file missing", http.StatusNotFound)
		return
	}
	// inline so the browser previews the PDF rather than downloading it
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(filepath.Base(abs)))
	http.ServeFile(w, r, abs)
}
