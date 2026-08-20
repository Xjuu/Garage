// Package repairsauth is the access control for repairs.<domain> — a shared
// 6-digit PIN instead of an account, plus a signed "remembered device"
// cookie so the crew doesn't retype it every visit. Deliberately independent
// from internal/auth (the dashboard's password+2FA) and from any other
// site's own PIN system: separate signing key, separate cookie names,
// separate session state. Sharing code across them would only ever save a
// few dozen lines at the cost of one subdomain's auth bug being able to
// reach another's.
package repairsauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	deviceCookie = "goldstar_repairs_device"
	csrfCookie   = "goldstar_repairs_csrf"
	deviceTTL    = 365 * 24 * time.Hour
)

// ErrNoPIN means the site has nothing configured yet — every code is
// rejected, not just any code that happens not to match.
var ErrNoPIN = errors.New("no PIN configured")

type Auth struct {
	pinMu sync.RWMutex
	pin   string

	secret  []byte
	secure  bool
	limiter *limiter
}

// New loads (or creates) the signing key at secretPath and returns an Auth
// gating on pin. An empty pin is valid — it just means CheckPIN rejects
// everything until SetPIN is called later, e.g. once an admin sets one from
// the dashboard.
func New(pin, secretPath string, secure bool) (*Auth, error) {
	secret, err := loadOrCreateSecret(secretPath)
	if err != nil {
		return nil, err
	}
	return &Auth{
		pin: strings.TrimSpace(pin), secret: secret, secure: secure,
		limiter: newLimiter(8, 15*time.Minute),
	}, nil
}

// Configured reports whether a PIN is set at all.
func (a *Auth) Configured() bool {
	a.pinMu.RLock()
	defer a.pinMu.RUnlock()
	return a.pin != ""
}

// SetPIN changes the PIN live, without a service restart — mirroring how
// the dashboard's own password can be changed from the Admin page.
func (a *Auth) SetPIN(pin string) {
	a.pinMu.Lock()
	defer a.pinMu.Unlock()
	a.pin = strings.TrimSpace(pin)
}

// CheckPIN validates a submitted code, rate-limited per client IP so a
// 6-digit code isn't just brute-forceable from the internet.
func (a *Auth) CheckPIN(r *http.Request, code string) error {
	ip := clientIP(r)
	if !a.limiter.allow(ip) {
		return errors.New("too many attempts, try again later")
	}
	a.pinMu.RLock()
	pin := a.pin
	a.pinMu.RUnlock()
	if pin == "" {
		return ErrNoPIN
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(code)), []byte(pin)) != 1 {
		a.limiter.fail(ip)
		return errors.New("incorrect code")
	}
	a.limiter.reset(ip)
	return nil
}

// IssueDevice mints a fresh device id, signs it into a long-lived cookie,
// and issues a matching CSRF cookie for this browser to echo back on every
// mutating request. Returns the device id so the caller can register it.
func (a *Auth) IssueDevice(w http.ResponseWriter) string {
	id := randomHex(16)
	exp := time.Now().Add(deviceTTL)
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie, Value: a.sign(id, exp), Path: "/",
		Expires: exp, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	token := randomHex(32)
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: token, Path: "/",
		Expires: exp, HttpOnly: false, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	return id
}

// ValidateDevice checks the cookie's signature and expiry only — purely
// cryptographic, no database lookup. A cryptographically valid cookie from
// a device the admin has since revoked still fails, but that check happens
// one layer up (the store knows about revocation; this package never does).
func (a *Auth) ValidateDevice(r *http.Request) (deviceID string, ok bool) {
	c, err := r.Cookie(deviceCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	id, exp, ok := a.verify(c.Value)
	if !ok || time.Now().After(exp) {
		return "", false
	}
	return id, true
}

// ClearDevice forgets this browser only — does not revoke the device
// server-side (that's an admin action, see the store's RevokeRepairsDevice).
func (a *Auth) ClearDevice(w http.ResponseWriter) {
	past := time.Unix(0, 0)
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie, Value: "", Path: "/", Expires: past, MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: "", Path: "/", Expires: past, MaxAge: -1,
		HttpOnly: false, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

// CSRFOK checks the JS-readable CSRF cookie against the X-CSRF-Token header
// a mutating request must echo — the same shape as the dashboard's own CSRF
// defence, with its own separate cookie and no shared state.
func (a *Auth) CSRFOK(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	sent := r.Header.Get("X-CSRF-Token")
	return subtle.ConstantTimeCompare([]byte(sent), []byte(c.Value)) == 1
}

// sign/verify: "deviceID.expiryUnix.hexHMAC", the same shape used for the
// dashboard's own session cookie.
func (a *Auth) sign(id string, exp time.Time) string {
	payload := id + "." + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *Auth) verify(cookie string) (id string, exp time.Time, ok bool) {
	parts := strings.SplitN(cookie, ".", 3)
	if len(parts) != 3 {
		return "", time.Time{}, false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(want)) != 1 {
		return "", time.Time{}, false
	}
	unix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	return parts[0], time.Unix(unix, 0), true
}

func loadOrCreateSecret(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, err
	}
	return secret, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// clientIP prefers the client's own address over any proxy header, except
// when the connection genuinely comes from the local machine — the
// Cloudflare tunnel case, where cloudflared sets X-Forwarded-For to the
// real visitor and the TCP peer is always loopback.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return host
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return host
}

// ── rate limiter ─────────────────────────────────────────────────────────

type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-l.window)
	kept := l.fails[ip][:0]
	for _, t := range l.fails[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.fails[ip] = kept
	return len(kept) < l.max
}

func (l *limiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip] = append(l.fails[ip], time.Now())
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}
