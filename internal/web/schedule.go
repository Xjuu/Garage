package web

import (
	"context"
	"fmt"
	"log"
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
	cfg  *config.Config
	srv  *Server
	loc  *time.Location
	hour int
	min  int

	mu         sync.Mutex
	next       time.Time
	last       time.Time
	lastResult string
}

func newScheduler(cfg *config.Config, srv *Server) (*scheduler, error) {
	if strings.TrimSpace(cfg.SyncAt) == "" {
		return nil, nil // scheduling switched off
	}
	hour, min, err := parseClock(cfg.SyncAt)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(cfg.SyncTZ)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %w", cfg.SyncTZ, err)
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
		"at":         fmt.Sprintf("%02d:%02d", s.hour, s.min),
		"timezone":   s.loc.String(),
		"next":       s.next.Format(time.RFC3339),
		"next_local": s.next.In(s.loc).Format("Mon 2 Jan 15:04 MST"),
	}
	if !s.last.IsZero() {
		out["last"] = s.last.In(s.loc).Format("Mon 2 Jan 15:04 MST")
		out["last_result"] = s.lastResult
	}
	return out
}

// run blocks until ctx is cancelled, syncing once per day at the set time.
func (s *scheduler) run(ctx context.Context) {
	log.Printf("mailbox sync scheduled for %02d:%02d %s daily", s.hour, s.min, s.loc)

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
		s.record("skipped: " + err.Error())
		log.Printf("scheduled sync skipped: %v", err)
		return
	}

	err := s.srv.jobs.Start("scheduled sync", func(ctx context.Context, logLine func(string)) (string, error) {
		logf := pipeline.LogFunc(func(format string, args ...any) {
			logLine(fmt.Sprintf(format, args...))
		})
		st, err := pipeline.Fetch(ctx, s.cfg, s.srv.db, logf)
		if st == nil {
			return "", err
		}
		logLine(st.Summary())
		return st.Summary(), err
	})
	if err != nil {
		// A manual sync already running is not a failure; the mail it is
		// fetching is the same mail this run would have fetched.
		s.record("skipped: " + err.Error())
		log.Printf("scheduled sync skipped: %v", err)
		return
	}
	s.record("started")
	log.Print("scheduled mailbox sync started")
}

func (s *scheduler) record(result string) {
	s.mu.Lock()
	s.lastResult = result
	s.mu.Unlock()
}
