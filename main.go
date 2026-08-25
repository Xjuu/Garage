// Command goldstar collects supplier invoices from a Hostinger mailbox,
// extracts vehicle registrations, part numbers and VAT figures with Gemini,
// and keeps everything in a local SQLite database with daily Excel exports
// and a local dashboard.
package main

import (
	// Embedded IANA timezone data, so "Europe/London" resolves on a static
	// binary and on machines without a system zoneinfo database.
	_ "time/tzdata"

	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"goldstar/internal/auth"
	"goldstar/internal/config"
	"goldstar/internal/extract"
	"goldstar/internal/pipeline"
	"goldstar/internal/repairsauth"
	"goldstar/internal/store"
	"goldstar/internal/web"
)

const usage = `goldstar — invoice collector for Garage Goldstar

Usage:
  goldstar           Open the dashboard (same as ` + "`serve`" + `). Makes no API calls
                     until you press Sync or upload a file.
  goldstar run       Fetch new invoices, then write today's Excel export (the daily job)
  goldstar fetch     Fetch and extract new invoices only
  goldstar ingest F  Extract invoices from local PDF or image files
  goldstar export    Write an Excel export from what is already stored
  goldstar backup    Snapshot the database into data/backups and prune old ones
  goldstar fleet-import F  Load a vehicle export (Callsign,Make,Model,Registration)
                     into the registry. Shows what it would do; add --apply to write.
                     --company "NAME" assigns every vehicle to that company.
  goldstar repairs-import F  Load a "Goldstar Service Record"-style workbook (one
                     sheet per vehicle) into the repairs log. Shows what it would
                     do; add --apply to write.
  goldstar examples  Scan the examples folder for new reference invoices
  goldstar eval      Re-extract every completed example and score the accuracy
  goldstar serve     Serve the dashboard (default 127.0.0.1:8787)
  goldstar passwd    Generate the dashboard password hash
  goldstar db-key-gen  Generate a database encryption key for GOLDSTAR_DB_KEY
  goldstar db-encrypt  One-time migration: encrypts data/goldstar.db in place with
                     GOLDSTAR_DB_KEY. Stop the service first. Safe to run any
                     time — a no-op once it's already encrypted with that key.
  goldstar repairs-pin-rehash  One-time migration: hashes data/repairs.pin in place
                     if it still holds a PIN from before this was hashed at rest.
                     Safe to run any time — a no-op once it's already hashed.
  goldstar totp-reset  Clear two-factor auth — the next login has to set it up again.
                     Use this if the device with the authenticator app is lost.
  goldstar user-add <username> <password> <admin|fleet> [email]
                     Create a dashboard account. Always starts with a forced
                     password change and 2FA setup required on its first login,
                     whatever password is given here.
  goldstar user-role <username> <admin|fleet>
                     Change an existing account's role. admin reaches every
                     page; fleet is everything except Training and Admin.
  goldstar user-list  List dashboard accounts and their role / 2FA status.
  goldstar user-passwd <username> [password]
                     Set an account's password directly — no old password
                     needed, unlike the Admin page's own change-password
                     form. With no password given, generates a random
                     256-character one (every letter, digit and UK-keyboard
                     special character) and prints it once; nothing else
                     stores it. The account's 2FA, if any, is untouched.
  goldstar doctor    Check configuration and connectivity without changing anything
  goldstar models    List the Gemini models this API key can call

Configuration is read from .env beside the binary, then $HOME/.config/garage-goldstar/.env.
See .env.example for the available settings.`

func main() {
	log.SetFlags(log.Ltime)

	// Bare `goldstar` opens the dashboard. It used to mean `run`, which
	// contacts the mailbox and spends Gemini credit — a bad thing to do by
	// accident. `serve` makes no API calls until a button is pressed, so it
	// is the safe thing to land on.
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		fmt.Println(usage)
		return
	}

	if err := realMain(cmd); err != nil {
		log.Fatalf("error: %v", err)
	}
}

// logf sends pipeline progress to the terminal.
var logf = pipeline.LogFunc(func(format string, args ...any) { log.Printf(format, args...) })

func realMain(cmd string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// passwd and totp-reset need no database and must work before anything
	// else is configured.
	if cmd == "passwd" {
		return passwd()
	}
	if cmd == "totp-reset" {
		if err := cfg.ClearTOTPSecret(); err != nil {
			return err
		}
		fmt.Println("Two-factor authentication has been cleared.")
		fmt.Println("The next login will need to set it up again from a QR code.")
		fmt.Println("Restart `goldstar serve` (or the systemd service) for this to take effect —")
		fmt.Println("a running process keeps the old secret in memory until it reloads.")
		return nil
	}
	if cmd == "repairs-pin-rehash" {
		return repairsPinRehash(cfg)
	}
	if cmd == "db-key-gen" {
		return dbKeyGen()
	}
	if cmd == "db-encrypt" {
		return dbEncrypt(cfg)
	}

	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	// A key is required unless explicitly waived — same shape as the
	// password check in internal/web.New, but this one gates DB access
	// itself, so it applies to every subcommand that opens the database
	// (backup, fleet-import, serve, …), not just the web server.
	if cfg.DBKey == "" && !cfg.AllowUnencryptedDB {
		return fmt.Errorf(
			"refusing to open the database without an encryption key.\n" +
				"  Run `goldstar db-key-gen` and put the printed line in your .env.\n" +
				"  If this really is a throwaway local database, set GOLDSTAR_ALLOW_UNENCRYPTED_DB=true.")
	}

	db, err := store.Open(cfg.DBPath(), cfg.DBKey)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := migrateLegacyAccount(cfg, db); err != nil {
		return fmt.Errorf("migrate legacy account: %w", err)
	}

	// Ctrl-C and systemd stop both unwind cleanly mid-fetch.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "run":
		if err := runFetch(ctx, cfg, db); err != nil {
			return err
		}
		return runExport(cfg, db)
	case "fetch":
		return runFetch(ctx, cfg, db)
	case "ingest":
		files := os.Args[2:]
		if len(files) == 0 {
			return fmt.Errorf("usage: goldstar ingest <file.pdf> [more files…]")
		}
		st, err := pipeline.IngestFile(ctx, cfg, db, files, logf)
		if st != nil {
			log.Print(st.Summary())
		}
		return err
	case "export":
		return runExport(cfg, db)
	case "fleet-import":
		if len(os.Args) < 3 {
			return fmt.Errorf("usage: goldstar fleet-import <vehicles.csv> [--apply]")
		}
		apply := false
		company := ""
		for i := 3; i < len(os.Args); i++ {
			switch {
			case os.Args[i] == "--apply":
				apply = true
			case os.Args[i] == "--company" && i+1 < len(os.Args):
				company = os.Args[i+1]
				i++
			case strings.HasPrefix(os.Args[i], "--company="):
				company = strings.TrimPrefix(os.Args[i], "--company=")
			}
		}
		rep, err := pipeline.ImportFleetCSV(db, os.Args[2], company, apply, logf)
		if rep != nil {
			printFleetReport(rep)
		}
		return err
	case "repairs-import":
		if len(os.Args) < 3 {
			return fmt.Errorf("usage: goldstar repairs-import <workbook.xlsx> [--apply]")
		}
		apply := false
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--apply" {
				apply = true
			}
		}
		rep, err := pipeline.ImportRepairsXLSX(db, os.Args[2], apply, logf)
		if rep != nil {
			printRepairsReport(rep)
		}
		return err
	case "backup":
		path, err := pipeline.RunBackup(cfg, db)
		if err != nil {
			return err
		}
		if path == "" {
			return fmt.Errorf("backups are disabled (GOLDSTAR_BACKUP_KEEP=0)")
		}
		log.Printf("wrote %s", path)
		return nil
	case "examples":
		added, seen, err := pipeline.ScanExamples(cfg, db, logf)
		if err != nil {
			return err
		}
		log.Printf("examples folder %s: %d file(s), %d newly registered", cfg.ExamplesDir(), seen, added)
		if added > 0 {
			log.Printf("enter their correct values on the Training page to activate them")
		}
		return nil
	case "eval":
		results, err := pipeline.Eval(ctx, cfg, db, logf)
		if err != nil {
			return err
		}
		ok, all := 0, 0
		for _, r := range results {
			ok += r.FieldsOK
			all += r.FieldsAll
		}
		if all == 0 {
			return fmt.Errorf("no comparable fields in the stored ground truth")
		}
		log.Printf("accuracy %.1f%% (%d/%d fields)", float64(ok)/float64(all)*100, ok, all)
		return nil
	case "serve":
		srv, err := web.New(cfg, db)
		if err != nil {
			return err
		}
		return srv.Listen(ctx, cfg.WebAddr)
	case "models":
		if err := cfg.RequireGemini(); err != nil {
			return err
		}
		names, err := extract.ListModels(ctx, cfg.GeminiKey)
		if err != nil {
			return err
		}
		for _, n := range names {
			marker := "  "
			if n == cfg.GeminiModel {
				marker = "* "
			}
			fmt.Println(marker + n)
		}
		return nil
	case "doctor":
		return doctor(cfg, db)
	case "user-add":
		return userAdd(db)
	case "user-role":
		return userRole(db)
	case "user-list":
		return userList(db)
	case "user-passwd":
		return userPasswd(db)
	default:
		fmt.Println(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// migrateLegacyAccount seeds the users table from the old single-account
// env/file-based config (GOLDSTAR_USER, GOLDSTAR_PASSWORD_HASH, the
// totp.secret file) the first time this runs against a database that
// predates multi-user accounts — so an existing install's login keeps
// working, unchanged, the moment it upgrades, with no manual step. A no-op
// on every later run: once any user row exists, this never touches the
// table again, so an operator who explicitly deletes every account is not
// silently undone.
func migrateLegacyAccount(cfg *config.Config, db *store.Store) error {
	n, err := db.UserCount()
	if err != nil {
		return err
	}
	if n > 0 || cfg.PasswordHash == "" {
		return nil
	}
	username := cfg.User
	if username == "" {
		username = cfg.Email
	}
	if username == "" {
		username = "admin"
	}
	id, err := db.CreateUser(username, cfg.Email, cfg.PasswordHash, store.RoleAdmin, false)
	if err != nil {
		return err
	}
	if cfg.TOTPSecret != "" {
		if err := db.SetUserTOTPSecret(id, cfg.TOTPSecret); err != nil {
			return err
		}
	}
	log.Printf("migrated the existing dashboard login into the accounts table as %q", username)
	return nil
}

// userAdd is `goldstar user-add <username> <password> <admin|fleet> [email]`.
// The account always starts requiring a forced password change and 2FA
// setup on its first login — whatever password is typed here to create it
// was necessarily chosen by whoever is running this command, not by the
// account's own owner, so it can never be treated as a real, permanent one.
func userAdd(db *store.Store) error {
	if len(os.Args) < 5 {
		return fmt.Errorf("usage: goldstar user-add <username> <password> <admin|fleet> [email]")
	}
	username, password, role := os.Args[2], os.Args[3], os.Args[4]
	email := ""
	if len(os.Args) > 5 {
		email = os.Args[5]
	}
	if role != store.RoleAdmin && role != store.RoleFleet {
		return fmt.Errorf("role must be %q or %q, got %q", store.RoleAdmin, store.RoleFleet, role)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	id, err := db.CreateUser(username, email, hash, role, true)
	if err != nil {
		return err
	}
	fmt.Printf("Created account %q (role %s, id %d).\n", username, role, id)
	fmt.Println("Its next login will be required to set a real password and then set up two-factor authentication before reaching the dashboard.")
	return nil
}

// userRole is `goldstar user-role <username> <admin|fleet>`.
func userRole(db *store.Store) error {
	if len(os.Args) < 4 {
		return fmt.Errorf("usage: goldstar user-role <username> <admin|fleet>")
	}
	u, err := db.GetUserByIdentity(os.Args[2])
	if err != nil {
		return fmt.Errorf("no such account %q", os.Args[2])
	}
	if err := db.SetUserRole(u.ID, os.Args[3]); err != nil {
		return err
	}
	fmt.Printf("%q is now role %q.\n", u.Username, os.Args[3])
	return nil
}

// userList is `goldstar user-list`.
func userList(db *store.Store) error {
	users, err := db.ListUsers()
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Println("No accounts yet.")
		return nil
	}
	for _, u := range users {
		totp := "no 2FA yet"
		if u.TOTPSecret != "" {
			totp = "2FA set up"
		}
		change := ""
		if u.MustChangePassword {
			change = ", must change password on next login"
		}
		fmt.Printf("%-20s role=%-6s %s%s\n", u.Username, u.Role, totp, change)
	}
	return nil
}

// passwordCharset is every upper- and lower-case letter, digit, and the
// special characters a standard UK English keyboard actually types (the
// shifted number row plus the other punctuation keys) — used only by the
// random generator below, not by HashPassword's own validation, which
// accepts anything at least 10 characters long.
const passwordCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"abcdefghijklmnopqrstuvwxyz" +
	"0123456789" +
	"!\"£$%^&*()_+-=[]{};:'@#~,.<>/?\\|`"

// randomPassword picks n characters uniformly from passwordCharset using
// crypto/rand — rejection-free, since big.Int's bound already avoids the
// modulo bias a plain `% len` would introduce. Built as []rune, not indexed
// as a plain string: £ is two bytes in UTF-8, and byte-indexing would slice
// straight through the middle of it.
func randomPassword(n int) (string, error) {
	runes := []rune(passwordCharset)
	bound := big.NewInt(int64(len(runes)))
	out := make([]rune, n)
	for i := range out {
		idx, err := rand.Int(rand.Reader, bound)
		if err != nil {
			return "", err
		}
		out[i] = runes[idx.Int64()]
	}
	return string(out), nil
}

// userPasswd is `goldstar user-passwd <username> [password]` — sets an
// account's password directly. Unlike the Admin page's own change-password
// form, this needs no current password: the whole point is being usable
// when nobody has one to give, whether that's a lockout or just a
// deliberate rotation.
func userPasswd(db *store.Store) error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: goldstar user-passwd <username> [password]")
	}
	u, err := db.GetUserByIdentity(os.Args[2])
	if err != nil {
		return fmt.Errorf("no such account %q", os.Args[2])
	}

	password := ""
	if len(os.Args) > 3 {
		password = os.Args[3]
	} else {
		password, err = randomPassword(256)
		if err != nil {
			return fmt.Errorf("generate password: %w", err)
		}
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := db.SetUserPasswordHash(u.ID, hash); err != nil {
		return err
	}

	if len(os.Args) > 3 {
		fmt.Printf("Password for %q has been changed.\n", u.Username)
	} else {
		fmt.Printf("New password for %q (shown once — save it now):\n\n%s\n\n", u.Username, password)
	}
	fmt.Println("Existing sessions on every device stay signed in — delete data/session.key (and data/repairs-session.key for the repairs site too) and restart if you also need to force a fresh sign-in everywhere.")
	return nil
}

func runFetch(ctx context.Context, cfg *config.Config, db *store.Store) error {
	st, err := pipeline.Fetch(ctx, cfg, db, logf)
	if st != nil {
		log.Print(st.Summary())
	}
	return err
}

func runExport(cfg *config.Config, db *store.Store) error {
	path, n, err := pipeline.Export(cfg, db)
	if err != nil {
		return err
	}
	log.Printf("wrote %d invoice(s) to %s", n, path)
	return nil
}

// repairsPinRehash migrates data/repairs.pin from the old plaintext format
// (raw digits, from before the PIN was hashed at rest) to the hashed one, in
// place — a one-time step for an install whose PIN was set before this
// version. Idempotent and safe to run any number of times: an
// already-hashed file, or no file at all (GOLDSTAR_REPAIRS_PIN-only setups
// never had one), is left untouched either way. Never prints the PIN
// itself, on success or failure.
func repairsPinRehash(cfg *config.Config) error {
	b, err := os.ReadFile(cfg.RepairsPINFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No repairs.pin file on disk — nothing to migrate.")
			return nil
		}
		return err
	}
	raw := strings.TrimSpace(string(b))
	if raw == "" {
		fmt.Println("repairs.pin is empty — nothing to migrate.")
		return nil
	}
	if strings.HasPrefix(raw, "argon2id$") {
		fmt.Println("repairs.pin is already hashed — nothing to do.")
		return nil
	}
	hash, err := repairsauth.HashPIN(raw)
	if err != nil {
		return err
	}
	if err := cfg.WriteRepairsPIN(hash); err != nil {
		return err
	}
	fmt.Println("repairs.pin has been rehashed in place.")
	fmt.Println("A running `goldstar serve` already hashes it in memory on its own and needs no restart for this — the file just now matches what's already true in memory.")
	return nil
}

// dbKeyGen prints a fresh, properly random database encryption key in the
// line to add to .env — mirroring passwd's UX, the same reason it exists:
// generating a high-entropy key by hand is the wrong thing to ask anyone to
// do themselves.
func dbKeyGen() error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	fmt.Fprintf(os.Stderr, "\nAdd this line to the .env beside the binary:\n\n")
	fmt.Printf("GOLDSTAR_DB_KEY=%s\n", hex.EncodeToString(key))
	fmt.Fprintf(os.Stderr, "\nKeep a copy of this somewhere other than the server — the database is\n"+
		"permanently unreadable without it, backups included. Once .env has the\n"+
		"key, run `goldstar db-encrypt` (with the service stopped) to migrate an\n"+
		"existing plaintext database in place.\n")
	return nil
}

// sqlitePlainMagic is the first 16 bytes of every ordinary, unencrypted
// SQLite file — an encrypted (or corrupt) one will never start with this.
const sqlitePlainMagic = "SQLite format 3\x00"

// dbEncrypt migrates data/goldstar.db from plaintext to encrypted in place,
// for an install whose database predates DBKey being required. Idempotent
// in the safe direction: a database that's already encrypted with the
// configured key is left alone rather than re-migrated; one that looks
// encrypted with a DIFFERENT key (or is simply corrupt) is refused rather
// than guessed at. Must run with the service stopped — this opens the file
// directly, outside anything `goldstar serve` itself manages.
func dbEncrypt(cfg *config.Config) error {
	if cfg.DBKey == "" {
		return fmt.Errorf("GOLDSTAR_DB_KEY is not set — run `goldstar db-key-gen` first and add the printed line to .env")
	}
	path := cfg.DBPath()
	header, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No database file yet at", path, "— nothing to migrate. It will be created encrypted on first run.")
			return nil
		}
		return err
	}
	if len(header) < 16 || string(header[:16]) != sqlitePlainMagic {
		// Not plaintext. Confirm it's already encrypted with THIS key before
		// declaring victory — a file that fails to open with the current
		// key is a different, more serious problem this must not paper
		// over.
		probe, err := sql.Open("sqlite3", path+"?_pragma_key=x'"+cfg.DBKey+"'")
		if err == nil {
			_, err = probe.Exec(`SELECT count(*) FROM sqlite_master`)
		}
		if probe != nil {
			probe.Close()
		}
		if err == nil {
			fmt.Println("Database is already encrypted with the configured key — nothing to do.")
			return nil
		}
		return fmt.Errorf(
			"the database at %s is neither plain SQLite nor readable with the configured key.\n"+
				"Refusing to touch it — this needs a human to look at it, not a guess.", path)
	}

	tmpPath := path + ".encrypting"
	os.Remove(tmpPath) // a stale leftover from an interrupted previous attempt

	plain, err := sql.Open("sqlite3", path)
	if err != nil {
		return fmt.Errorf("open plaintext db: %w", err)
	}
	defer plain.Close()

	counts, err := tableCounts(plain)
	if err != nil {
		return fmt.Errorf("count rows before migrating: %w", err)
	}

	if _, err := plain.Exec(`ATTACH DATABASE ? AS encrypted KEY "x'` + cfg.DBKey + `'"`, tmpPath); err != nil {
		return fmt.Errorf("attach encrypted target: %w", err)
	}
	if _, err := plain.Exec(`SELECT sqlcipher_export('encrypted')`); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("sqlcipher_export: %w", err)
	}
	if _, err := plain.Exec(`DETACH DATABASE encrypted`); err != nil {
		return fmt.Errorf("detach: %w", err)
	}
	plain.Close()

	enc, err := sql.Open("sqlite3", tmpPath+"?_pragma_key=x'"+cfg.DBKey+"'")
	if err != nil {
		return fmt.Errorf("open the newly encrypted copy: %w", err)
	}
	newCounts, err := tableCounts(enc)
	enc.Close()
	if err != nil {
		return fmt.Errorf("count rows after migrating: %w", err)
	}
	for table, want := range counts {
		if newCounts[table] != want {
			os.Remove(tmpPath)
			return fmt.Errorf(
				"row count mismatch after migrating — aborted without touching the original.\n"+
					"  %s: had %d row(s), encrypted copy has %d", table, want, newCounts[table])
		}
	}

	backupPath := path + ".preencrypt-backup"
	if err := os.Rename(path, backupPath); err != nil {
		return fmt.Errorf("back up the original before swapping in the encrypted copy: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// Put the original back rather than leave neither file at the real path.
		os.Rename(backupPath, path)
		return fmt.Errorf("swap in the encrypted copy: %w", err)
	}

	fmt.Printf("Migrated %s to encrypted-at-rest, verified against %d table(s) with matching row counts.\n", path, len(counts))
	fmt.Println("The original plaintext file is kept at", backupPath, "— delete it once you've")
	fmt.Println("confirmed `goldstar serve` starts and reads correctly with the new file.")
	return nil
}

// tableCounts is dbEncrypt's before/after sanity check — every table the
// schema defines, not just the obviously important ones, so a mismatch
// anywhere aborts rather than only catching it in the tables someone
// thought to check.
func tableCounts(db *sql.DB) (map[string]int, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	rows.Close()

	counts := make(map[string]int, len(tables))
	for _, t := range tables {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM "` + t + `"`).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		counts[t] = n
	}
	return counts, nil
}

// passwd prompts twice without echoing and prints the line to add to .env.
// When stdin is not a terminal it reads a single line instead, so the hash can
// be generated from a script.
func passwd() error {
	var password string

	if term.IsTerminal(int(syscall.Stdin)) {
		fmt.Fprint(os.Stderr, "New dashboard password (min 10 chars): ")
		first, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		fmt.Fprint(os.Stderr, "Repeat: ")
		second, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if string(first) != string(second) {
			return fmt.Errorf("passwords do not match")
		}
		password = string(first)
	} else {
		sc := bufio.NewScanner(os.Stdin)
		if !sc.Scan() {
			return fmt.Errorf("no password on stdin")
		}
		password = strings.TrimSpace(sc.Text())
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nAdd this line to the .env beside the binary:\n\n")
	fmt.Printf("GOLDSTAR_PASSWORD_HASH=%s\n", hash)
	return nil
}

// doctor reports what is configured and what is missing, without sending any
// invoice to Gemini or writing any row.
func doctor(cfg *config.Config, db *store.Store) error {
	fmt.Printf("data dir     %s\n", cfg.DataDir)
	fmt.Printf("database     %s\n", cfg.DBPath())
	fmt.Printf("exports      %s\n", cfg.ExportsDir())
	fmt.Printf("imap         %s:%d mailbox=%s\n", cfg.IMAPHost, cfg.IMAPPort, cfg.IMAPMailbox)
	fmt.Printf("imap user    %s\n", orUnset(cfg.IMAPUser))
	fmt.Printf("imap pass    %s\n", secretState(cfg.IMAPPass))
	fmt.Printf("gemini key   %s\n", secretState(cfg.GeminiKey))
	fmt.Printf("gemini model %s\n", cfg.GeminiModel)
	fmt.Printf("web addr     %s\n", cfg.WebAddr)
	fmt.Printf("cookie secure %v\n", cfg.CookieSecure)

	invoices, items, err := db.Counts()
	if err != nil {
		return err
	}
	fmt.Printf("stored       %d invoice(s), %d line item(s)\n", invoices, items)

	// Accounts live in the database now, not a single hash in .env — the
	// real answer to "is this dashboard guarded" is how many rows exist,
	// checked live rather than read off cfg.PasswordHash, which after this
	// install's first run only ever reflects the legacy bootstrap value.
	users, err := db.ListUsers()
	if err != nil {
		return err
	}

	// The combination that would publish financial records to the internet.
	switch {
	case len(users) > 0:
		fmt.Printf("\nDASHBOARD   %d account(s):\n", len(users))
		for _, u := range users {
			totp := "no 2FA yet"
			if u.TOTPSecret != "" {
				totp = "2FA set up"
			}
			change := ""
			if u.MustChangePassword {
				change = ", must change password on next login"
			}
			fmt.Printf("            %-20s role=%-6s %s%s\n", u.Username, u.Role, totp, change)
		}
		if !cfg.CookieSecure {
			fmt.Printf("            note: set GOLDSTAR_COOKIE_SECURE=true when serving through a tunnel\n")
		}
	case cfg.AllowNoPassword:
		fmt.Printf("\nDASHBOARD   NO PASSWORD (explicitly allowed). Anyone who can reach %s sees every invoice.\n", cfg.WebAddr)
	default:
		fmt.Printf("\nDASHBOARD   REFUSES TO START: no account exists.\n")
		fmt.Printf("            Run `goldstar user-add <username> <password> admin` to create one.\n")
	}

	if err := cfg.RequireMail(); err != nil {
		fmt.Printf("IMAP        NOT CONFIGURED: %v\n", err)
		return nil
	}
	fmt.Printf("connecting to IMAP…\n")
	n, err := pipeline.CheckMail(cfg)
	if err != nil {
		fmt.Printf("IMAP        FAILED: %v\n", err)
		return nil
	}
	fmt.Printf("IMAP        OK — %d message(s) in the last %d day(s)\n", n, cfg.LookbackDays)
	return nil
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func secretState(s string) string {
	if s == "" {
		return "(unset)"
	}
	return fmt.Sprintf("set, %d chars", len(s))
}

// printFleetReport shows the effect of an import before or after it happens.
// Problems are listed in full rather than summarised: a registration that did
// not import is a car whose costs will go unattributed, and a count alone
// gives the operator no way to find it.
func printFleetReport(r *pipeline.FleetReport) {
	mode := "DRY RUN — nothing written"
	if r.Applied {
		mode = "applied"
	}
	fmt.Printf("\n  %d row(s) read, %d distinct vehicle(s)  [%s]\n", r.Rows, r.Vehicles, mode)
	fmt.Printf("  %d new, %d already in the registry\n", r.Created, r.Updated)

	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Printf("\n  %s (%d)\n", title, len(lines))
		for _, l := range lines {
			fmt.Printf("    %s\n", l)
		}
	}
	section("Spelling normalised", r.Corrections)
	section("Not imported", r.Rejected)
	section("Same plate, several callsigns", r.Duplicates)
	section("Worth checking", r.Odd)
	section("Invoiced but missing from this export", r.Unlisted)
	fmt.Println()
}

// printRepairsReport mirrors printFleetReport's shape: every skipped row is
// listed by name, not just counted, since each one is a real service visit
// that otherwise silently goes missing from the vehicle's history.
func printRepairsReport(r *pipeline.RepairsReport) {
	mode := "DRY RUN — nothing written"
	if r.Applied {
		mode = "applied"
	}
	fmt.Printf("\n  %d sheet(s) read, %d skipped (blank templates), %d vehicle(s)  [%s]\n",
		r.Sheets, r.SheetsSkipped, r.Vehicles, mode)
	fmt.Printf("  %d service row(s) + %d timing-belt row(s) found, %d imported\n",
		r.MainRows, r.BeltRows, r.Imported)

	if len(r.Skipped) > 0 {
		fmt.Printf("\n  Skipped (%d)\n", len(r.Skipped))
		for _, l := range r.Skipped {
			fmt.Printf("    %s\n", l)
		}
	}
	fmt.Println()
}
