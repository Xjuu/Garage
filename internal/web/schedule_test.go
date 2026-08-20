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

// Hourly-at-a-fixed-minute mode is what GOLDSTAR_SYNC_MINUTE means: the same
// real clock time every hour, unlike interval mode which counts forward from
// whenever the process happened to start (and so drifts with every restart).
func TestHourlyMinuteScheduling(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	minute := 30
	s := &scheduler{loc: loc, hourlyMinute: &minute}

	cases := []struct {
		name, from, want string
	}{
		{"before the slot this hour", "2026-08-19T14:10:00Z", "2026-08-19T14:30:00Z"},
		{"after the slot rolls to next hour", "2026-08-19T14:45:00Z", "2026-08-19T15:30:00Z"},
		{"exactly on the slot rolls to next hour", "2026-08-19T14:30:00Z", "2026-08-19T15:30:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, _ := time.Parse(time.RFC3339, c.from)
			want, _ := time.Parse(time.RFC3339, c.want)
			got := s.nextRun(from)
			if !got.Equal(want) {
				t.Errorf("nextRun(%s) = %s, want %s", c.from, got.Format(time.RFC3339), c.want)
			}
		})
	}
}

// A restart must not push the sync later: whatever time hourlyMinute mode
// computes has to be independent of when the process happens to boot,
// which is the entire reason this mode exists over a plain interval.
func TestHourlyMinuteSchedulingIsRestartInvariant(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	minute := 30
	s := &scheduler{loc: loc, hourlyMinute: &minute}

	restartA, _ := time.Parse(time.RFC3339, "2026-08-19T14:05:00Z")
	restartB, _ := time.Parse(time.RFC3339, "2026-08-19T14:25:00Z")
	if got, want := s.nextRun(restartA), s.nextRun(restartB); !got.Equal(want) {
		t.Errorf("two restarts within the same hour computed different next runs: %s vs %s", got, want)
	}
}

// config.SyncMinute must win over both SyncEvery and SyncAt when set, and
// GOLDSTAR_SYNC_MINUTE=0 (xx:00:00) must not be mistaken for "unset" — the
// exact ambiguity a plain int with a -1 sentinel would risk.
func TestSyncMinuteConfigTakesPriority(t *testing.T) {
	zero := 0
	cfg := &config.Config{
		SyncMinute: &zero,
		SyncEvery:  "1h",
		SyncAt:     "18:30",
		SyncTZ:     "Europe/London",
	}
	s, err := newScheduler(cfg, nil)
	if err != nil {
		t.Fatalf("newScheduler: %v", err)
	}
	if s.hourlyMinute == nil || *s.hourlyMinute != 0 {
		t.Fatalf("hourlyMinute = %v, want a pointer to 0", s.hourlyMinute)
	}
	if s.every != 0 {
		t.Errorf("every = %s, want interval mode disabled", s.every)
	}
}

func TestSyncMinuteValidation(t *testing.T) {
	for _, bad := range []int{-1, 60, 120} {
		cfg := &config.Config{SyncMinute: &bad, SyncTZ: "Europe/London"}
		if _, err := newScheduler(cfg, nil); err == nil {
			t.Errorf("SyncMinute %d was accepted; it must be rejected", bad)
		}
	}
}

// Plain zero-value config.Config{} (what an easy-to-write test or a bug
// would produce) must not accidentally activate hourly-at-minute-0 mode —
// this is exactly what a *int nil default buys over an int with a sentinel.
func TestZeroValueConfigDoesNotEnableHourlyMode(t *testing.T) {
	cfg := &config.Config{SyncEvery: "1h", SyncTZ: "Europe/London"}
	s, err := newScheduler(cfg, nil)
	if err != nil {
		t.Fatalf("newScheduler: %v", err)
	}
	if s.hourlyMinute != nil {
		t.Fatalf("hourlyMinute = %v, want nil — SyncMinute was never set", s.hourlyMinute)
	}
	if s.every != time.Hour {
		t.Errorf("every = %s, want 1h", s.every)
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
