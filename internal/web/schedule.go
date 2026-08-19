package web

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goldstar/internal/config"
	"goldstar/internal/pipeline"
)

// scheduler pulls the mailbox once a day at a local wall-clock time.
//
// The time is interpreted in a named zone rather than a fixed offset, so
// "18:30 London" stays 18:30 across the BST/GMT changeover instead of drifting
// by an hour twice a year. Each wait is recomputed from the current time, so a
// laptop that slept through the moment picks the next one up correctly rather
// than firing a burst of missed runs.
type scheduler struct {
	cfg   *config.Config
	srv   *Server
	loc   *time.Location
	every time.Duration // interval mode; zero means daily-at-a-time mode
	hour  int
	min   int

	mu                  sync.Mutex
	next                time.Time
	last                time.Time
	lastResult          string
	lastOK              bool
	lastError           string
	lastSuccess         time.Time
	lastBackup          time.Time
	consecutiveFailures int
}

func newScheduler(cfg *config.Config, srv *Server) (*scheduler, error) {
	loc, err := time.LoadLocation(cfg.SyncTZ)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %w", cfg.SyncTZ, err)
	}

	// An interval wins over a fixed time. Both being set is a contradiction,
	// and silently picking one without saying so is how people end up
	// convinced the timer is broken.
	if every := strings.TrimSpace(cfg.SyncEvery); every != "" {
		d, err := time.ParseDuration(every)
		if err != nil {
			return nil, fmt.Errorf("sync interval %q must look like 1h or 30m: %w", every, err)
		}
		// A very short interval hammers the mailbox for no benefit: invoices
		// arrive minutes apart at best, and every run opens an IMAP session.
		if d < time.Minute {
			return nil, fmt.Errorf("sync interval %s is too short; use 1m or more", d)
		}
		if strings.TrimSpace(cfg.SyncAt) != "" {
			log.Printf("both GOLDSTAR_SYNC_EVERY and GOLDSTAR_SYNC_AT are set; "+
				"syncing every %s and ignoring the daily time %s", d, cfg.SyncAt)
		}
		return &scheduler{cfg: cfg, srv: srv, loc: loc, every: d}, nil
	}

	if strings.TrimSpace(cfg.SyncAt) == "" {
		return nil, nil // scheduling switched off
	}
	hour, min, err := parseClock(cfg.SyncAt)
	if err != nil {
		return nil, err
	}
	return &scheduler{cfg: cfg, srv: srv, loc: loc, hour: hour, min: min}, nil
}

func parseClock(s string) (hour, min int, err error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("sync time %q must look like 18:30", s)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("sync time %q has an invalid hour", s)
	}
	min, err = strconv.Atoi(parts[1])
	if err != nil || min < 0 || min > 59 {
		return 0, 0, fmt.Errorf("sync time %q has an invalid minute", s)
	}
	return hour, min, nil
}

// nextRun returns the next occurrence of the configured time strictly after
// `from`. Constructing the time in the target zone is what makes DST correct:
// the zone decides what offset 18:30 has on that particular date.
func (s *scheduler) nextRun(from time.Time) time.Time {
	if s.every > 0 {
		return from.Add(s.every)
	}
	local := from.In(s.loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), s.hour, s.min, 0, 0, s.loc)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
		// Adding a day to a Date in a zone can land on a wall-clock time that
		// does not exist (spring forward). Rebuilding from the calendar date
		// lets the zone resolve it rather than silently shifting an hour.
		next = time.Date(next.Year(), next.Month(), next.Day(), s.hour, s.min, 0, 0, s.loc)
	}
	return next
}

// Status is what the dashboard shows about the timer.
func (s *scheduler) Status() map[string]any {
	if s == nil {
		return map[string]any{"enabled": false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{
		"enabled":    true,
		"timezone":   s.loc.String(),
		"next":       s.next.Format(time.RFC3339),
		"next_local": s.next.In(s.loc).Format("Mon 2 Jan 15:04 MST"),
	}
	if s.every > 0 {
		out["every"] = s.every.String()
		out["at"] = "every " + s.every.String()
	} else {
		out["at"] = fmt.Sprintf("%02d:%02d", s.hour, s.min)
	}
	if !s.last.IsZero() {
		out["last"] = s.last.In(s.loc).Format("Mon 2 Jan 15:04 MST")
		out["last_result"] = s.lastResult
		out["last_ok"] = s.lastOK
		out["failures"] = s.consecutiveFailures
		if s.lastError != "" {
			out["last_error"] = s.lastError
		}
	}
	if !s.lastSuccess.IsZero() {
		out["last_success"] = s.lastSuccess.In(s.loc).Format("Mon 2 Jan 15:04 MST")
		out["days_since_success"] = int(time.Since(s.lastSuccess).Hours() / 24)
	}
	// Surfaced so the dashboard can show a banner rather than leaving a silent
	// failure buried in a log nobody reads.
	out["alert"] = s.consecutiveFailures > 0
	return out
}

// run blocks until ctx is cancelled, syncing once per day at the set time.
func (s *scheduler) run(ctx context.Context) {
	if s.every > 0 {
		log.Printf("mailbox sync scheduled every %s", s.every)
	} else {
		log.Printf("mailbox sync scheduled for %02d:%02d %s daily", s.hour, s.min, s.loc)
	}

	for {
		next := s.nextRun(time.Now())
		s.mu.Lock()
		s.next = next
		s.mu.Unlock()

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		s.fire()
	}
}

// fire starts a sync through the same job runner the dashboard button uses, so
// a scheduled run and a manual one cannot overlap and the log is visible in
// the UI either way.
func (s *scheduler) fire() {
	now := time.Now()
	s.mu.Lock()
	s.last = now
	s.mu.Unlock()

	if err := s.cfg.RequireMail(); err != nil {
		s.recordFailure("mailbox not configured: " + err.Error())
		log.Printf("scheduled sync skipped: %v", err)
		return
	}

	err := s.srv.jobs.Start("scheduled sync", func(ctx context.Context, logLine func(string)) (string, error) {
		logf := pipeline.LogFunc(func(format string, args ...any) {
			logLine(fmt.Sprintf(format, args...))
		})

		// Snapshot before ingesting, not after: if a bad run corrupts or
		// pollutes the data, the backup you want is the one taken beforehand.
		//
		// Throttled to once a day. With an hourly sync an unthrottled backup
		// would take 24 snapshots a day and the retention limit would evict
		// everything older than about half a day — leaving a "backup" that
		// only ever covers this morning.
		if s.dueForBackup() {
			if path, bErr := pipeline.RunBackup(s.cfg, s.srv.db); bErr != nil {
				logLine("backup failed: " + bErr.Error())
				s.recordFailure("backup failed: " + bErr.Error())
			} else if path != "" {
				s.markBackedUp()
				logLine("backed up to " + filepath.Base(path))
			}
		}

		st, err := pipeline.Fetch(ctx, s.cfg, s.srv.db, logf)
		if err != nil {
			s.recordFailure(err.Error())
			if st == nil {
				return "", err
			}
			logLine(st.Summary())
			return st.Summary(), err
		}
		s.recordSuccess(st.Summary())
		logLine(st.Summary())
		return st.Summary(), nil
	})
	if err != nil {
		// A manual sync already running is not a failure; the mail it is
		// fetching is the same mail this run would have fetched.
		s.mu.Lock()
		s.lastResult = "skipped: " + err.Error()
		s.mu.Unlock()
		log.Printf("scheduled sync skipped: %v", err)
		return
	}
	log.Print("scheduled mailbox sync started")
}

// backupInterval is how often a snapshot is taken, independent of how often
// the mailbox is checked.
const backupInterval = 20 * time.Hour

func (s *scheduler) dueForBackup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastBackup) >= backupInterval
}

func (s *scheduler) markBackedUp() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBackup = time.Now()
}

func (s *scheduler) recordSuccess(result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastResult = result
	s.lastOK = true
	s.lastError = ""
	s.consecutiveFailures = 0
	s.lastSuccess = time.Now()
}

// recordFailure keeps the reason and counts how many runs in a row have
// failed. A single failure is usually a blip; several in a row means invoices
// have quietly stopped arriving, which is the thing nobody notices until a VAT
// quarter is due.
func (s *scheduler) recordFailure(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastResult = "failed: " + reason
	s.lastOK = false
	s.lastError = reason
	s.consecutiveFailures++
	log.Printf("ALERT: scheduled sync failed (%d in a row): %s", s.consecutiveFailures, reason)
}
