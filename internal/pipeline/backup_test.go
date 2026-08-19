package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// Pruning must keep the newest snapshots and never touch anything it did not
// write. A backup folder that quietly deletes an operator's own file, or that
// keeps the oldest instead of the newest, is worse than no pruning at all.
func TestPruneBackups(t *testing.T) {
	dir := t.TempDir()

	// Names sort chronologically as text, which is what prune relies on.
	made := []string{
		"goldstar-backup-2026-08-01-1200.db",
		"goldstar-backup-2026-08-02-1200.db",
		"goldstar-backup-2026-08-03-1200.db",
		"goldstar-backup-2026-08-04-1200.db",
		"goldstar-backup-2026-08-05-1200.db",
	}
	for _, n := range made {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Files this code does not own must survive untouched.
	foreign := []string{"my-own-copy.db", "notes.txt", "goldstar.db"}
	for _, n := range foreign {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := pruneBackups(dir, 2); err != nil {
		t.Fatalf("prune: %v", err)
	}

	left := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left[e.Name()] = true
	}

	for _, n := range []string{
		"goldstar-backup-2026-08-05-1200.db",
		"goldstar-backup-2026-08-04-1200.db",
	} {
		if !left[n] {
			t.Errorf("newest snapshot %s was deleted", n)
		}
	}
	for _, n := range []string{
		"goldstar-backup-2026-08-01-1200.db",
		"goldstar-backup-2026-08-02-1200.db",
		"goldstar-backup-2026-08-03-1200.db",
	} {
		if left[n] {
			t.Errorf("old snapshot %s should have been pruned", n)
		}
	}
	for _, n := range foreign {
		if !left[n] {
			t.Errorf("prune deleted %s, which it does not own", n)
		}
	}
}

// Keeping more than exist must not error or delete anything.
func TestPruneKeepsAllWhenUnderLimit(t *testing.T) {
	dir := t.TempDir()
	name := "goldstar-backup-2026-08-01-1200.db"
	os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600)

	if err := pruneBackups(dir, 14); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Errorf("the only snapshot was deleted: %v", err)
	}
}
