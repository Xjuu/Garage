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

	// SyncAt is a local clock time ("18:30") in SyncTZ at which the mailbox is
	// pulled automatically while the dashboard is running.
	SyncAt string
	SyncTZ string

	// PasswordHash gates the dashboard. Generate it with `goldstar passwd`.
	PasswordHash string
	// CookieSecure marks session cookies Secure. Required behind a Cloudflare
	// tunnel, which always terminates TLS.
	CookieSecure bool
	// AllowNoPassword is the explicit opt-out that lets the dashboard run
	// unauthenticated. It has to be deliberate: a Cloudflare tunnel reaches
	// the app over loopback, so binding to 127.0.0.1 is no evidence at all
	// that the site is private.
	AllowNoPassword bool
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
		SyncAt:          envStr("GOLDSTAR_SYNC_AT", "18:30"),
		SyncTZ:          envStr("GOLDSTAR_SYNC_TZ", "Europe/London"),
		DataDir:         envStr("DATA_DIR", defaultDataDir(home)),
		WebAddr:         envStr("WEB_ADDR", "127.0.0.1:8787"),
		LookbackDays:    envInt("LOOKBACK_DAYS", 7),
		PasswordHash:    envStr("GOLDSTAR_PASSWORD_HASH", ""),
		CookieSecure:    envBool("GOLDSTAR_COOKIE_SECURE", false),
		AllowNoPassword: envBool("GOLDSTAR_ALLOW_NO_PASSWORD", false),
	}

	// Mailbox settings saved from the admin page override .env for the same
	// reason the password hash does.
	if kv, err := readKeyValues(c.MailFilePath()); err == nil {
		if v := kv["IMAP_HOST"]; v != "" {
			c.IMAPHost = v
		}
		if v := kv["IMAP_PORT"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				c.IMAPPort = n
			}
		}
		if v := kv["IMAP_USER"]; v != "" {
			c.IMAPUser = v
		}
		if v := kv["IMAP_PASS"]; v != "" {
			c.IMAPPass = v
		}
		if v := kv["IMAP_MAILBOX"]; v != "" {
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
func (c *Config) ExportsDir() string     { return filepath.Join(c.DataDir, "exports") }

// ExamplesDir is the drop folder for reference invoices used to teach the
// extractor. Files placed here are picked up by `goldstar examples scan` and
// by the Training page.
func (c *Config) ExamplesDir() string { return filepath.Join(c.DataDir, "examples") }

func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.AttachmentsDir(), c.ExportsDir(), c.ExamplesDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// readKeyValues parses a KEY=VALUE file without touching the process
// environment, unlike loadEnvFile.
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
