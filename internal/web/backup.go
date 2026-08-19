package web

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"goldstar/internal/config"
	"goldstar/internal/store"
)

// backupPrefix identifies snapshots this code owns, so pruning can never touch
// a file someone put in the folder by hand.
const backupPrefix = "goldstar-backup-"

// runBackup writes a snapshot of the database and prunes old ones.
//
// VACUUM INTO is used rather than copying the file, because a plain copy of a
// live SQLite database in WAL mode can capture a torn state — the .db without
// the pages still sitting in the write-ahead log. VACUUM INTO produces a
// complete, already-compacted database and is safe while the app is running.
func runBackup(cfg *config.Config, db *store.Store) (string, error) {
	if cfg.BackupKeep <= 0 {
		return "", nil // disabled
	}
	dir := cfg.BackupsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	name := backupPrefix + time.Now().Format("2006-01-02-1504") + ".db"
	path := filepath.Join(dir, name)

	// VACUUM INTO refuses to overwrite, so a second run in the same minute
	// would fail on the name alone. Removing first keeps it idempotent.
	os.Remove(path)
	if err := db.BackupTo(path); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		log.Printf("backup written but permissions could not be tightened: %v", err)
	}

	if err := pruneBackups(dir, cfg.BackupKeep); err != nil {
		// A failed prune is not a failed backup; the snapshot is already safe.
		log.Printf("could not prune old backups: %v", err)
	}
	return path, nil
}

// pruneBackups keeps the newest `keep` snapshots and deletes the rest.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), backupPrefix) && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	// The timestamp in the name sorts chronologically as text, so this needs
	// no stat call per file.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	for _, name := range names[min(keep, len(names)):] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// backupStatus describes the snapshots for the Admin page.
func backupStatus(cfg *config.Config) map[string]any {
	out := map[string]any{
		"enabled": cfg.BackupKeep > 0,
		"keep":    cfg.BackupKeep,
		"folder":  cfg.BackupsDir(),
		"count":   0,
	}
	entries, err := os.ReadDir(cfg.BackupsDir())
	if err != nil {
		return out
	}
	var newest os.FileInfo
	count := 0
	var bytes int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), backupPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		count++
		bytes += info.Size()
		if newest == nil || info.ModTime().After(newest.ModTime()) {
			newest = info
		}
	}
	out["count"] = count
	out["bytes"] = bytes
	if newest != nil {
		out["latest"] = newest.Name()
		out["latest_at"] = newest.ModTime().Format(time.RFC3339)
	}
	return out
}
