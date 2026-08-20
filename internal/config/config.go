// Package config loads runtime settings from the environment and an optional
// .env file, so no credential ever needs to live in source.
package config

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	IMAPHost    string
	IMAPPort    int
	IMAPUser    string
	IMAPPass    string
	IMAPMailbox string

	GeminiKey   string
	GeminiModel string

	DataDir      string
	WebAddr      string
	LookbackDays int

	// User and Email are the two names accepted at sign-in; either one works
	// with the password.
	User  string
	Email string

	// SyncEvery is a repeat interval ("1h", "30m"). When set it wins over
	// SyncAt, because "check often" and "check at a fixed time" are different
	// intentions and one of them has to take precedence predictably.
	SyncEvery string

	// SyncMinute, when set, runs the sync every hour at that minute past the
	// hour (0-59, e.g. 30 for xx:30:00) instead of SyncEvery's "N hours after
	// whenever this process last started". That distinction matters in
	// practice: a redeploy restarts the process, and SyncEvery's countdown
	// restarts with it, so the actual sync time silently drifts by however
	// long ago the last deploy happened. A fixed minute is the same real
	// clock time every hour no matter how many times the process has
	// restarted since.
	//
	// A pointer, not an int with -1 for "unset": xx:00:00 (minute 0) is a
	// legitimate value, and an int default cannot tell "unset" apart from it
	// without a sentinel someone eventually forgets to check for — the exact
	// mistake a zero-value config.Config{} in a test would otherwise make
	// silently. Wins over both SyncEvery and SyncAt when non-nil.
	SyncMinute *int

	// SyncAt is a local clock time ("18:30") in SyncTZ for once-a-day syncing,
	// used only when SyncEvery is empty.
	SyncAt string
	SyncTZ string

	// BackupKeep is how many nightly database snapshots to retain. Zero
	// disables automatic backups.
	BackupKeep int

	// PasswordHash gates the dashboard. Generate it with `goldstar passwd`.
	PasswordHash string
	// TOTPSecret is the confirmed base32 two-factor secret. Empty means this
	// account has not finished 2FA setup yet — mandatory once PasswordHash is
	// set, so the next login is required to complete it before reaching the
	// dashboard.
	TOTPSecret string
	// CookieSecure marks session cookies Secure. Required behind a Cloudflare
	// tunnel, which always terminates TLS.
	CookieSecure bool
	// AllowNoPassword is the explicit opt-out that lets the dashboard run
	// unauthenticated. It has to be deliberate: a Cloudflare tunnel reaches
	// the app over loopback, so binding to 127.0.0.1 is no evidence at all
	// that the site is private.
	AllowNoPassword bool

	// PartsPIN gates parts.<domain> — the 6-digit code a worker types once
	// per device. Empty disables the parts site entirely rather than
	// falling back to no PIN at all.
	PartsPIN string
}

// Load reads .env files (first found wins) then overlays real environment
// variables, which always take precedence.
func Load() (*Config, error) {
	for _, p := range envFileCandidates() {
		if loadEnvFile(p) == nil {
			break
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}

	c := &Config{
		IMAPHost:        envStr("IMAP_HOST", "imap.hostinger.com"),
		IMAPPort:        envInt("IMAP_PORT", 993),
		IMAPUser:        envStr("IMAP_USER", ""),
		IMAPPass:        envStr("IMAP_PASS", ""),
		IMAPMailbox:     envStr("IMAP_MAILBOX", "INBOX"),
		GeminiKey:       envStr("GEMINI_API_KEY", envStr("GOOGLE_API_KEY", "")),
		GeminiModel:     envStr("GEMINI_MODEL", "gemini-3.1-flash-lite"),
		User:            envStr("GOLDSTAR_USER", ""),
		Email:           envStr("GOLDSTAR_EMAIL", ""),
		SyncEvery:       envStr("GOLDSTAR_SYNC_EVERY", "1h"),
		SyncMinute:      envIntPtr("GOLDSTAR_SYNC_MINUTE"),
		SyncAt:          envStr("GOLDSTAR_SYNC_AT", ""),
		SyncTZ:          envStr("GOLDSTAR_SYNC_TZ", "Europe/London"),
		BackupKeep:      envInt("GOLDSTAR_BACKUP_KEEP", 14),
		DataDir:         envStr("DATA_DIR", defaultDataDir(home)),
		WebAddr:         envStr("WEB_ADDR", "127.0.0.1:8787"),
		LookbackDays:    envInt("LOOKBACK_DAYS", 7),
		PasswordHash:    envStr("GOLDSTAR_PASSWORD_HASH", ""),
		CookieSecure:    envBool("GOLDSTAR_COOKIE_SECURE", false),
		AllowNoPassword: envBool("GOLDSTAR_ALLOW_NO_PASSWORD", false),
		PartsPIN:        envStr("GOLDSTAR_PARTS_PIN", ""),
	}

	// Mailbox settings saved from the admin page override the .env file, since
	// they are the more recent deliberate choice. They do NOT override a
	// variable set in the actual environment: that is the most explicit signal
	// available, and an operator overriding one for a test or a systemd unit
	// must not be silently ignored.
	if kv, err := readKeyValues(c.MailFilePath()); err == nil {
		if v := kv["IMAP_HOST"]; v != "" && !envSet("IMAP_HOST") {
			c.IMAPHost = v
		}
		if v := kv["IMAP_PORT"]; v != "" && !envSet("IMAP_PORT") {
			if n, err := strconv.Atoi(v); err == nil {
				c.IMAPPort = n
			}
		}
		if v := kv["IMAP_USER"]; v != "" && !envSet("IMAP_USER") {
			c.IMAPUser = v
		}
		if v := kv["IMAP_PASS"]; v != "" && !envSet("IMAP_PASS") {
			c.IMAPPass = v
		}
		if v := kv["IMAP_MAILBOX"]; v != "" && !envSet("IMAP_MAILBOX") {
			c.IMAPMailbox = v
		}
	}

	// A hash written from the admin page lives beside the data and wins over
	// the .env value: it is the more recent, deliberate choice, and the app
	// cannot rewrite .env safely while systemd may also be reading it.
	if b, err := os.ReadFile(c.PasswordFilePath()); err == nil {
		if h := bytes.TrimSpace(b); len(h) > 0 {
			c.PasswordHash = string(h)
		}
	}
	if b, err := os.ReadFile(c.TOTPFilePath()); err == nil {
		if v := bytes.TrimSpace(b); len(v) > 0 {
			c.TOTPSecret = string(v)
		}
	}
	if b, err := os.ReadFile(c.PartsPINFilePath()); err == nil {
		if v := bytes.TrimSpace(b); len(v) > 0 {
			c.PartsPIN = string(v)
		}
	}
	return c, nil
}

// MailFilePath holds mailbox credentials entered on the Admin page. Like the
// password hash, a value set here wins over .env because it is the more recent
// deliberate choice.
//
// The IMAP password is stored in plain text (mode 0600): IMAP authentication
// needs the original secret, so it cannot be hashed the way the dashboard
// password is.
func (c *Config) MailFilePath() string { return filepath.Join(c.DataDir, "mailbox.env") }

// WriteMailSettings persists mailbox credentials from the dashboard.
func (c *Config) WriteMailSettings(host string, port int, user, pass, mailbox string) error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf("IMAP_HOST=%s\nIMAP_PORT=%d\nIMAP_USER=%s\nIMAP_PASS=%s\nIMAP_MAILBOX=%s\n",
		host, port, user, pass, mailbox)
	return os.WriteFile(c.MailFilePath(), []byte(body), 0o600)
}

// PasswordFilePath holds a hash set from the dashboard, overriding .env.
func (c *Config) PasswordFilePath() string { return filepath.Join(c.DataDir, "password.hash") }

// WritePasswordHash persists a new hash, replacing any earlier one.
func (c *Config) WritePasswordHash(hash string) error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.PasswordFilePath(), []byte(hash+"\n"), 0o600)
}

// TOTPFilePath holds the confirmed two-factor secret, alongside the password
// hash it is required to accompany.
func (c *Config) TOTPFilePath() string { return filepath.Join(c.DataDir, "totp.secret") }

// WriteTOTPSecret persists the secret once its setup code has been verified.
func (c *Config) WriteTOTPSecret(secret string) error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.TOTPFilePath(), []byte(strings.TrimSpace(secret)+"\n"), 0o600)
}

// ClearTOTPSecret removes 2FA from the account — the "lost my phone"
// recovery path. Deliberately a server-side-only operation (`goldstar
// totp-reset`, run over SSH), not a dashboard control: exposing this as a
// button in the web UI would let anyone holding just the password strip off
// the second factor through the same login form it exists to guard.
func (c *Config) ClearTOTPSecret() error {
	err := os.Remove(c.TOTPFilePath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// PartsPINFilePath holds a PIN set from the admin page, overriding .env —
// same pattern as the dashboard password hash.
func (c *Config) PartsPINFilePath() string { return filepath.Join(c.DataDir, "parts.pin") }

// WritePartsPIN persists a new PIN, replacing any earlier one.
func (c *Config) WritePartsPIN(pin string) error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.PartsPINFilePath(), []byte(strings.TrimSpace(pin)+"\n"), 0o600)
}

// PartsSessionKeyPath holds the HMAC key that signs parts-device cookies —
// deliberately its own file, separate from the dashboard's SessionKeyPath:
// the two auth systems have nothing to do with each other, and losing or
// rotating one must never sign the other's users out.
func (c *Config) PartsSessionKeyPath() string { return filepath.Join(c.DataDir, "parts-session.key") }

// SessionKeyPath holds the HMAC key that signs session cookies. Deleting this
// file logs everyone out.
func (c *Config) SessionKeyPath() string { return filepath.Join(c.DataDir, "session.key") }

// LoopbackOnly reports whether WEB_ADDR is bound to localhost. A dashboard
// without a password may only ever listen there.
func (c *Config) LoopbackOnly() bool {
	host, _, err := net.SplitHostPort(c.WebAddr)
	if err != nil {
		host = c.WebAddr
	}
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RequireMail reports whether mail credentials are usable, so `export` and
// `serve` can run offline without them.
func (c *Config) RequireMail() error {
	if c.IMAPUser == "" || c.IMAPPass == "" {
		return fmt.Errorf("IMAP_USER and IMAP_PASS must be set (see .env.example)")
	}
	return nil
}

func (c *Config) RequireGemini() error {
	if c.GeminiKey == "" {
		return fmt.Errorf("GEMINI_API_KEY must be set (see .env.example)")
	}
	return nil
}

func (c *Config) DBPath() string         { return filepath.Join(c.DataDir, "goldstar.db") }
func (c *Config) AttachmentsDir() string { return filepath.Join(c.DataDir, "attachments") }

// BackupsDir holds automatic nightly snapshots of the database.
func (c *Config) BackupsDir() string { return filepath.Join(c.DataDir, "backups") }
func (c *Config) ExportsDir() string { return filepath.Join(c.DataDir, "exports") }

// ExamplesDir is the drop folder for reference invoices used to teach the
// extractor. Files placed here are picked up by `goldstar examples scan` and
// by the Training page.
func (c *Config) ExamplesDir() string { return filepath.Join(c.DataDir, "examples") }

func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.AttachmentsDir(), c.ExportsDir(), c.ExamplesDir(), c.BackupsDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// readKeyValues parses a KEY=VALUE file without touching the process
// environment, unlike loadEnvFile.
// envSet reports whether a variable came from the real environment rather than
// from a .env file. loadEnvFile records what it set so the two can be told
// apart, which is the whole basis of the precedence rule above.
func envSet(key string) bool {
	_, fromFile := envFromFile[key]
	_, present := os.LookupEnv(key)
	return present && !fromFile
}

// envFromFile records keys that loadEnvFile put into the environment.
var envFromFile = map[string]bool{}

func readKeyValues(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, val, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(key)] = strings.TrimSpace(val)
		}
	}
	return out, sc.Err()
}

// defaultDataDir keeps everything — database, attachments, exports, examples,
// credentials — inside the program's own folder, so the whole installation is
// one directory you can copy, back up or move. The older location under
// ~/.local/share is still honoured if it already holds a database, so an
// existing install is not orphaned by an upgrade.
func defaultDataDir(home string) string {
	if dir := installDir(); dir != "" {
		local := filepath.Join(dir, "data")
		legacy := filepath.Join(home, ".local", "share", "garage-goldstar")
		if _, err := os.Stat(filepath.Join(local, "goldstar.db")); err == nil {
			return local
		}
		if _, err := os.Stat(filepath.Join(legacy, "goldstar.db")); err == nil {
			return legacy
		}
		return local
	}
	return filepath.Join(home, ".local", "share", "garage-goldstar")
}

// installDir is the directory holding the executable. Falls back to the working
// directory when the path cannot be resolved (an unusual but possible case).
func installDir() string {
	exe, err := os.Executable()
	if err != nil {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// envFileCandidates looks beside the program first, so a copied folder carries
// its own settings, then falls back to the older per-user location.
func envFileCandidates() []string {
	var out []string
	if p := os.Getenv("GOLDSTAR_ENV_FILE"); p != "" {
		out = append(out, p)
	}
	if dir := installDir(); dir != "" {
		out = append(out, filepath.Join(dir, ".env"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".config", "garage-goldstar", ".env"))
	}
	return append(out, ".env")
}

// loadEnvFile parses KEY=VALUE lines. Existing environment variables win, so a
// systemd unit or shell export can always override the file.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
			// Remembered so a value that came from a file is not mistaken for
			// one the operator set in the real environment.
			envFromFile[key] = true
		}
	}
	return sc.Err()
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envIntPtr returns nil when the variable is unset, empty, or malformed —
// distinguishing "not configured" from "configured as zero", which a plain
// int with a default cannot do without a sentinel value.
func envIntPtr(key string) *int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}
