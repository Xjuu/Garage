// Package partsauth guards parts.<domain> — the parts counter workers use to
// log a part taken for a vehicle. Deliberately independent of internal/auth:
// this is a different site with a different, much lower-ceremony audience
// (a shared 6-digit PIN, not individual accounts), and the two must never be
// able to affect each other. Rotating or clearing one never touches the
// other's cookies, secrets or sessions.
//
// This package only ever handles cryptography — minting and checking the
// device cookie, checking the PIN. Device revocation is a database fact
// (parts_devices.active), not something a signed cookie can express on its
// own, so the caller is expected to check that separately once a cookie has
// passed ValidateDevice.
package partsauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
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
	deviceCookie = "goldstar_parts_device"
	csrfCookie   = "goldstar_parts_csrf"
	// A device that has proved itself once stays trusted for a long time —
	// the whole point is not asking again on every visit. Revocation from
	// the admin page is the actual "log this device out" control; letting
	// the cookie itself also expire eventually is just a backstop.
	deviceTTL = 365 * 24 * time.Hour
)

var ErrNoPIN = errors.New("no PIN configured")

type Auth struct {
	pinMu   sync.RWMutex
	pin     string
	secret  []byte
	secure  bool
	limiter *limiter
}

// New builds the guard. secretPath is persisted so a device stays trusted
// across a restart; if the file is missing, a fresh secret is generated —
// which, as a side effect, signs out every previously-trusted device, so
// this file is worth backing up the same way the dashboard's is.
func New(pin, secretPath string, secure bool) (*Auth, error) {
	secret, err := loadOrCreateSecret(secretPath)
	if err != nil {
		return nil, err
	}
	return &Auth{
		pin:     strings.TrimSpace(pin),
		secret:  secret,
		secure:  secure,
		limiter: newLimiter(8, 15*time.Minute),
	}, nil
}

// Configured reports whether a PIN is set at all. Without one the parts site
// refuses every request — there is nothing to compare a typed code against.
func (a *Auth) Configured() bool {
	a.pinMu.RLock()
	defer a.pinMu.RUnlock()
	return a.pin != ""
}

// SetPIN swaps in a new PIN at runtime — called after the admin page saves
// one, so a change takes effect immediately rather than needing a restart.
func (a *Auth) SetPIN(pin string) {
	a.pinMu.Lock()
	defer a.pinMu.Unlock()
	a.pin = strings.TrimSpace(pin)
}

// CheckPIN verifies a candidate code in constant time and against a per-IP
// limiter — six digits is only a million possibilities, cheap to guess
// without one.
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
	code = strings.TrimSpace(code)
	if len(code) != len(pin) || subtle.ConstantTimeCompare([]byte(code), []byte(pin)) != 1 {
		a.limiter.fail(ip)
		return errors.New("incorrect code")
	}
	a.limiter.reset(ip)
	return nil
}

// IssueDevice mints a new, opaque device id and sets the cookie that
// remembers it. The id is what the caller stores in parts_devices — the
// cookie only ever proves "this browser was handed this id once", not
// anything about whether it is still allowed in.
func (a *Auth) IssueDevice(w http.ResponseWriter) (deviceID string) {
	deviceID = randomHex(16)
	a.setDeviceCookie(w, deviceID)

	// Readable by JS deliberately — the SPA echoes it back as a header on
	// every logging request, the same CSRF defence the dashboard uses. The
	// device cookie itself stays HttpOnly; this is the one piece of state
	// that has to be, so the front end can get at it.
	exp := time.Now().Add(deviceTTL)
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: randomHex(32), Path: "/",
		Expires: exp, HttpOnly: false, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	return deviceID
}

func (a *Auth) setDeviceCookie(w http.ResponseWriter, deviceID string) {
	exp := time.Now().Add(deviceTTL)
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie, Value: a.mint(deviceID, exp), Path: "/",
		Expires: exp, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

// ValidateDevice reports the device id carried by a correctly-signed,
// unexpired cookie. It says nothing about revocation — the caller checks
// parts_devices.active separately, since that can change at any time and a
// signature alone can never reflect it.
func (a *Auth) ValidateDevice(r *http.Request) (deviceID string, ok bool) {
	c, err := r.Cookie(deviceCookie)
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(c.Value, ".", 3)
	if len(parts) != 3 {
		return "", false
	}
	id, expStr, sig := parts[0], parts[1], parts[2]
	payload := id + "." + expStr
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return "", false
	}
	unix, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || !time.Now().Before(time.Unix(unix, 0)) {
		return "", false
	}
	return id, true
}

func (a *Auth) mint(deviceID string, exp time.Time) string {
	payload := deviceID + "." + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *Auth) ClearDevice(w http.ResponseWriter) {
	for _, name := range []string{deviceCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: name == deviceCookie, Secure: a.secure, SameSite: http.SameSiteLaxMode,
		})
	}
}

// CSRFOK reports whether a state-changing request carries the CSRF header
// matching this device's cookie. Checked in addition to the device cookie
// itself, not instead of it — a valid device cookie proves the browser was
// handed a token once; this proves the request came from the SPA's own
// fetch call and not a form on some other page riding along on that cookie.
func (a *Auth) CSRFOK(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	sent := r.Header.Get("X-CSRF-Token")
	return subtle.ConstantTimeCompare([]byte(sent), []byte(c.Value)) == 1
}

func loadOrCreateSecret(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	secret := make([]byte, 48)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
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
	rand.Read(b)
	return hex.EncodeToString(b)
}

// clientIP prefers Cloudflare's header, since behind a tunnel every request
// arrives from localhost and would otherwise share one rate-limit bucket —
// the same reasoning as internal/auth's identical helper, duplicated rather
// than shared because these two packages must not depend on each other.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if first, _, ok := strings.Cut(fwd, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ClientIP exposes the same extraction for the IP allow-list check, which
// belongs to the web layer (it is a database fact, not an auth concern) but
// needs to agree with this package on what "the client's IP" even means.
func ClientIP(r *http.Request) string { return clientIP(r) }

type limiter struct {
	mu     sync.Mutex
	fails  map[string]*entry
	max    int
	window time.Duration
}

type entry struct {
	count int
	until time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{fails: map[string]*entry{}, max: max, window: window}
}

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.fails[ip]
	if !ok {
		return true
	}
	if time.Now().After(e.until) {
		delete(l.fails, ip)
		return true
	}
	return e.count < l.max
}

func (l *limiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.fails[ip]
	if !ok || time.Now().After(e.until) {
		l.fails[ip] = &entry{count: 1, until: time.Now().Add(l.window)}
		return
	}
	e.count++
	e.until = time.Now().Add(l.window)
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}
