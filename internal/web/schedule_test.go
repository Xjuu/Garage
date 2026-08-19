package web

import (
	"strings"
	"testing"
	"time"

	"goldstar/internal/config"
)

// The point of naming a zone rather than fixing an offset is that 18:30 stays
// 18:30 when the clocks change. If this regresses, the sync silently drifts an
// hour twice a year.
func TestNextRunAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("Europe/London unavailable — is time/tzdata still imported? %v", err)
	}
	s := &scheduler{loc: loc, hour: 18, min: 30}

	cases := []struct {
		name, from, wantUTC string
	}{
		{"winter, before the slot", "2026-01-15T12:00:00Z", "2026-01-15T18:30:00Z"},
		{"winter, after the slot", "2026-01-15T19:00:00Z", "2026-01-16T18:30:00Z"},
		{"summer is an hour ahead", "2026-07-15T12:00:00Z", "2026-07-15T17:30:00Z"},
		{"day the clocks go forward", "2026-03-29T12:00:00Z", "2026-03-29T17:30:00Z"},
		{"day the clocks go back", "2026-10-25T12:00:00Z", "2026-10-25T18:30:00Z"},
		{"exactly on the slot rolls to tomorrow", "2026-01-15T18:30:00Z", "2026-01-16T18:30:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, _ := time.Parse(time.RFC3339, c.from)
			want, _ := time.Parse(time.RFC3339, c.wantUTC)
			got := s.nextRun(from)

			if !got.Equal(want) {
				t.Errorf("nextRun = %s, want %s",
					got.UTC().Format(time.RFC3339), c.wantUTC)
			}
			// Whatever the offset, the local wall clock must read 18:30.
			if h, m := got.In(loc).Hour(), got.In(loc).Minute(); h != 18 || m != 30 {
				t.Errorf("local time = %02d:%02d, want 18:30", h, m)
			}
		})
	}
}

func TestParseClock(t *testing.T) {
	ok := map[string][2]int{
		"18:30": {18, 30}, "00:00": {0, 0}, "23:59": {23, 59}, " 9:05 ": {9, 5},
	}
	for in, want := range ok {
		h, m, err := parseClock(in)
		if err != nil {
			t.Errorf("parseClock(%q) errored: %v", in, err)
			continue
		}
		if h != want[0] || m != want[1] {
			t.Errorf("parseClock(%q) = %d:%d, want %d:%d", in, h, m, want[0], want[1])
		}
	}
	// A malformed time must be reported, not silently treated as midnight —
	// that would move the sync without anyone noticing.
	for _, bad := range []string{"", "1830", "25:00", "18:70", "18:30:00", "six", "-1:00"} {
		if _, _, err := parseClock(bad); err == nil {
			t.Errorf("parseClock(%q) was accepted; it must be rejected", bad)
		}
	}
}

// A failed run must be counted and remembered, and a success must clear it —
// otherwise the alert either never fires or never stops.
func TestSchedulerFailureTracking(t *testing.T) {
	s := &scheduler{}

	s.recordFailure("imap: authentication failed")
	s.recordFailure("imap: authentication failed")
	if s.consecutiveFailures != 2 {
		t.Errorf("failures = %d, want 2", s.consecutiveFailures)
	}
	if s.lastOK {
		t.Error("lastOK should be false after a failure")
	}
	if !strings.Contains(s.lastResult, "authentication") {
		t.Errorf("lastResult = %q, want the reason kept", s.lastResult)
	}

	s.recordSuccess("scanned 3, stored 2")
	if s.consecutiveFailures != 0 {
		t.Errorf("a success must reset the counter, got %d", s.consecutiveFailures)
	}
	if !s.lastOK {
		t.Error("lastOK should be true after a success")
	}
	if s.lastError != "" {
		t.Errorf("lastError = %q, want it cleared", s.lastError)
	}
	if s.lastSuccess.IsZero() {
		t.Error("lastSuccess was not recorded")
	}
}

// Interval mode is what "check every hour" means. It must not consult the
// timezone-and-wall-clock path at all.
func TestIntervalScheduling(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	s := &scheduler{loc: loc, every: time.Hour}

	from, _ := time.Parse(time.RFC3339, "2026-08-19T14:20:00Z")
	got := s.nextRun(from)
	want := from.Add(time.Hour)
	if !got.Equal(want) {
		t.Errorf("nextRun = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	// Repeated calls must keep stepping forward, not stick on one instant.
	second := s.nextRun(got)
	if !second.Equal(got.Add(time.Hour)) {
		t.Errorf("second run = %s, want %s", second, got.Add(time.Hour))
	}
}

// An hourly sync must not chew through the retained snapshots. Backups are
// throttled independently of how often the mailbox is checked.
func TestBackupThrottledIndependentlyOfSync(t *testing.T) {
	s := &scheduler{every: time.Hour}

	if !s.dueForBackup() {
		t.Fatal("the first run should take a backup")
	}
	s.markBackedUp()

	if s.dueForBackup() {
		t.Error("a backup was taken moments ago; another is not due")
	}

	// Simulate a day passing.
	s.mu.Lock()
	s.lastBackup = time.Now().Add(-21 * time.Hour)
	s.mu.Unlock()
	if !s.dueForBackup() {
		t.Error("after 21 hours a backup should be due again")
	}
}

// A misconfigured interval must be rejected loudly rather than silently
// falling back to some other cadence.
func TestSyncIntervalValidation(t *testing.T) {
	for _, bad := range []string{"hourly", "1", "0s", "30s", "-1h"} {
		cfg := &config.Config{SyncEvery: bad, SyncTZ: "Europe/London"}
		if _, err := newScheduler(cfg, nil); err == nil {
			t.Errorf("interval %q was accepted; it must be rejected", bad)
		}
	}
	for _, good := range []string{"1h", "30m", "2h30m", "1m"} {
		cfg := &config.Config{SyncEvery: good, SyncTZ: "Europe/London"}
		s, err := newScheduler(cfg, nil)
		if err != nil {
			t.Errorf("interval %q was rejected: %v", good, err)
			continue
		}
		if s == nil || s.every == 0 {
			t.Errorf("interval %q did not enable interval mode", good)
		}
	}
}
