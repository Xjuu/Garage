package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goldstar/internal/auth"
	"goldstar/internal/config"
	"goldstar/internal/extract"
	"goldstar/internal/pipeline"
)

// routesAdmin registers the management endpoints: what is configured, whether
// the connections work, and the data-level operations.
func (s *Server) routesAdmin(api *http.ServeMux) {
	api.HandleFunc("GET /api/admin/status", s.json(s.adminStatus))
	api.HandleFunc("POST /api/admin/test-imap", s.json(s.testIMAP))
	api.HandleFunc("POST /api/admin/test-gemini", s.json(s.testGemini))
	api.HandleFunc("GET /api/admin/models", s.json(s.adminModels))
	api.HandleFunc("POST /api/admin/password", s.json(s.changePassword))
	api.HandleFunc("POST /api/admin/mailbox", s.json(s.saveMailbox))
	api.HandleFunc("POST /api/admin/backup-now", s.json(s.backupNow))
	api.HandleFunc("POST /api/admin/vacuum", s.json(s.vacuum))
	api.HandleFunc("GET /api/admin/backup", s.backup)
}

// adminStatus is `goldstar doctor` rendered for the browser. Secrets are
// reported only as set/unset with a length — never echoed back.
func (s *Server) adminStatus(r *http.Request) (any, error) {
	invoices, items, err := s.db.Counts()
	if err != nil {
		return nil, err
	}
	examples, _ := s.db.Examples()
	ready := 0
	for _, e := range examples {
		if e.Status == "ready" {
			ready++
		}
	}
	hints, _ := s.db.Hints()

	return map[string]any{
		"backups":        pipeline.BackupStatus(s.cfg),
		"schedule":       s.sched.Status(),
		"data_dir":       s.cfg.DataDir,
		"database":       s.cfg.DBPath(),
		"exports_dir":    s.cfg.ExportsDir(),
		"examples_dir":   s.cfg.ExamplesDir(),
		"db_bytes":       fileSize(s.cfg.DBPath()),
		"imap_host":      s.cfg.IMAPHost,
		"imap_port":      s.cfg.IMAPPort,
		"imap_mailbox":   s.cfg.IMAPMailbox,
		"imap_user":      s.cfg.IMAPUser,
		"imap_pass_set":  s.cfg.IMAPPass != "",
		"gemini_set":     s.cfg.GeminiKey != "",
		"gemini_model":   s.cfg.GeminiModel,
		"lookback_days":  s.cfg.LookbackDays,
		"web_addr":       s.cfg.WebAddr,
		"cookie_secure":  s.cfg.CookieSecure,
		"password_set":   s.auth.Configured(),
		"invoices":       invoices,
		"items":          items,
		"examples":       len(examples),
		"examples_ready": ready,
		"hints":          len(hints),
	}, nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (s *Server) testIMAP(r *http.Request) (any, error) {
	if err := s.cfg.RequireMail(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	n, err := pipeline.CheckMail(s.cfg)
	if err != nil {
		// A failed credential check is a normal answer to "test this", not a
		// server error, so it returns 200 with ok:false and the reason.
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	return map[string]any{
		"ok":      true,
		"message": fmt.Sprintf("connected — %d message(s) in the last %d day(s)", n, s.cfg.LookbackDays),
	}, nil
}

func (s *Server) testGemini(r *http.Request) (any, error) {
	if err := s.cfg.RequireGemini(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	names, err := extract.ListModels(ctx, s.cfg.GeminiKey)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	for _, n := range names {
		if n == s.cfg.GeminiModel {
			return map[string]any{
				"ok":      true,
				"message": fmt.Sprintf("key valid, %d model(s) available, %s is callable", len(names), n),
			}, nil
		}
	}
	return map[string]any{
		"ok": false,
		"error": fmt.Sprintf("key works, but GEMINI_MODEL %q is not in the %d available models",
			s.cfg.GeminiModel, len(names)),
	}, nil
}

func (s *Server) adminModels(r *http.Request) (any, error) {
	if err := s.cfg.RequireGemini(); err != nil {
		return nil, fail(http.StatusPreconditionFailed, "%v", err)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	names, err := extract.ListModels(ctx, s.cfg.GeminiKey)
	if err != nil {
		return nil, fail(http.StatusBadGateway, "%v", err)
	}
	return map[string]any{"models": names, "current": s.cfg.GeminiModel}, nil
}

// changePassword requires the current password, so a session left open on an
// unattended screen cannot be used to lock the owner out.
func (s *Server) changePassword(r *http.Request) (any, error) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if s.auth.Configured() && !s.auth.Verify(body.Current) {
		return nil, fail(http.StatusForbidden, "current password is incorrect")
	}
	hash, err := auth.HashPassword(body.New)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	if err := s.cfg.WritePasswordHash(hash); err != nil {
		return nil, err
	}
	s.auth.SetHash(hash)
	return map[string]any{
		"ok":      true,
		"message": "password changed — existing sessions stay signed in",
	}, nil
}

// saveMailbox stores IMAP credentials entered on the Admin page and verifies
// them before saving, so a typo is caught immediately rather than at 19:00 when
// the timer next runs.
func (s *Server) saveMailbox(r *http.Request) (any, error) {
	var body struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		User    string `json:"user"`
		Pass    string `json:"pass"`
		Mailbox string `json:"mailbox"`
		Verify  bool   `json:"verify"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}

	body.Host = strings.TrimSpace(body.Host)
	body.User = strings.TrimSpace(body.User)
	body.Mailbox = strings.TrimSpace(body.Mailbox)
	if body.Host == "" {
		body.Host = "imap.hostinger.com"
	}
	if body.Port == 0 {
		body.Port = 993
	}
	if body.Mailbox == "" {
		body.Mailbox = "INBOX"
	}
	if body.User == "" {
		return nil, fail(http.StatusBadRequest, "the full email address is required")
	}
	// An empty password means "keep the stored one", so re-saving the mailbox
	// name does not force the password to be retyped.
	if body.Pass == "" {
		body.Pass = s.cfg.IMAPPass
	}
	if body.Pass == "" {
		return nil, fail(http.StatusBadRequest, "a mailbox password is required")
	}

	probe := *s.cfg
	probe.IMAPHost, probe.IMAPPort = body.Host, body.Port
	probe.IMAPUser, probe.IMAPPass, probe.IMAPMailbox = body.User, body.Pass, body.Mailbox

	if body.Verify {
		n, err := pipeline.CheckMail(&probe)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		if err := s.cfg.WriteMailSettings(body.Host, body.Port, body.User, body.Pass, body.Mailbox); err != nil {
			return nil, err
		}
		s.applyMail(&probe)
		return map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("connected and saved — %d message(s) in the last %d day(s)", n, s.cfg.LookbackDays),
		}, nil
	}

	if err := s.cfg.WriteMailSettings(body.Host, body.Port, body.User, body.Pass, body.Mailbox); err != nil {
		return nil, err
	}
	s.applyMail(&probe)
	return map[string]any{"ok": true, "message": "saved without testing"}, nil
}

// applyMail updates the running config so a Sync straight after saving uses the
// new credentials without a restart.
func (s *Server) applyMail(from *config.Config) {
	s.cfg.IMAPHost = from.IMAPHost
	s.cfg.IMAPPort = from.IMAPPort
	s.cfg.IMAPUser = from.IMAPUser
	s.cfg.IMAPPass = from.IMAPPass
	s.cfg.IMAPMailbox = from.IMAPMailbox
}

// backupNow takes a snapshot on demand, using the same code path as the
// nightly one so what you test by hand is what runs unattended.
func (s *Server) backupNow(r *http.Request) (any, error) {
	if s.cfg.BackupKeep <= 0 {
		return nil, fail(http.StatusBadRequest,
			"automatic backups are switched off (GOLDSTAR_BACKUP_KEEP=0)")
	}
	path, err := pipeline.RunBackup(s.cfg, s.db)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "name": filepath.Base(path), "bytes": info.Size(),
		"status": pipeline.BackupStatus(s.cfg),
	}, nil
}

func (s *Server) vacuum(r *http.Request) (any, error) {
	before := fileSize(s.cfg.DBPath())
	if err := s.db.Vacuum(); err != nil {
		return nil, err
	}
	after := fileSize(s.cfg.DBPath())
	return map[string]any{"ok": true, "before": before, "after": after}, nil
}

// backup streams a consistent copy of the database. VACUUM INTO produces a
// clean snapshot even while the app is running, which a plain file copy of a
// WAL-mode database would not.
func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	tmp, err := os.MkdirTemp("", "goldstar-backup-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmp)

	name := fmt.Sprintf("goldstar-backup-%s.db", time.Now().Format("2006-01-02-1504"))
	path := filepath.Join(tmp, name)
	if err := s.db.BackupTo(path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}
