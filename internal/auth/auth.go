// Package auth guards the dashboard. It exists because this service is meant
// to be published through a Cloudflare tunnel: once the tunnel is up, the
// dashboard is reachable by anyone who learns the hostname, and it serves
// financial records and can spend money on the Gemini API.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

	"golang.org/x/crypto/argon2"
)

const (
	sessionCookie = "goldstar_session"
	csrfCookie    = "goldstar_csrf"
	sessionTTL    = 7 * 24 * time.Hour

	// Argon2id parameters. 64 MiB and 3 passes is comfortably above the
	// OWASP minimum and costs a few hundred ms once per login.
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

var ErrNoPassword = errors.New("no password configured")

type Auth struct {
	user    string
	email   string
	hash    string // encoded argon2id hash; empty disables login entirely
	secret  []byte // HMAC key for session cookies
	secure  bool   // set Secure on cookies (true behind a tunnel)
	limiter *limiter
}

// New builds the guard. secret is persisted so sessions survive a restart;
// if it is missing one is generated.
func New(passwordHash, secretPath string, secure bool) (*Auth, error) {
	secret, err := loadOrCreateSecret(secretPath)
	if err != nil {
		return nil, err
	}
	return &Auth{
		hash:    strings.TrimSpace(passwordHash),
		secret:  secret,
		secure:  secure,
		limiter: newLimiter(10, 15*time.Minute),
	}, nil
}

// Configured reports whether a password is set. Without one the server refuses
// to bind to anything but loopback.
func (a *Auth) Configured() bool { return a.hash != "" }

// HashPassword produces the encoded string stored in GOLDSTAR_PASSWORD_HASH.
func HashPassword(password string) (string, error) {
	if len(password) < 10 {
		return "", errors.New("password must be at least 10 characters")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s",
		argonTime, argonMemory, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verify checks a candidate password against the stored hash in constant time.
func (a *Auth) verify(password string) bool {
	parts := strings.Split(a.hash, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false
	}
	t, err1 := strconv.Atoi(parts[1])
	m, err2 := strconv.Atoi(parts[2])
	p, err3 := strconv.Atoi(parts[3])
	salt, err4 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err5 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Verify exposes the constant-time password check for the change-password
// flow, which must confirm the current password before accepting a new one.
func (a *Auth) Verify(password string) bool { return a.verify(password) }

// SetIdentity records who may sign in. Both a username and an email are
// accepted for the same account; either one plus the password gets you in.
// Leaving both empty keeps the older password-only behaviour rather than
// locking out an existing install on upgrade.
func (a *Auth) SetIdentity(user, email string) {
	a.user = strings.ToLower(strings.TrimSpace(user))
	a.email = strings.ToLower(strings.TrimSpace(email))
}

// identityOK reports whether the supplied name matches the configured user or
// email. Comparison is case-insensitive because neither is case-sensitive in
// practice, and a rejected login over a capital letter is pure friction.
func (a *Auth) identityOK(who string) bool {
	if a.user == "" && a.email == "" {
		return true
	}
	who = strings.ToLower(strings.TrimSpace(who))
	return who != "" && (who == a.user || who == a.email)
}

// SetHash swaps in a new password hash at runtime. Existing sessions survive:
// they are signed with the separate session secret, not the password.
func (a *Auth) SetHash(hash string) { a.hash = strings.TrimSpace(hash) }

// Login validates the password and, on success, issues session and CSRF cookies.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request, who, password string) error {
	ip := clientIP(r)
	if !a.limiter.allow(ip) {
		return errors.New("too many attempts, try again later")
	}
	if !a.Configured() {
		return ErrNoPassword
	}
	// The username and password are checked together and reported together.
	// Saying which half was wrong would tell an attacker whether an account
	// exists, and costs a legitimate user nothing.
	if !a.identityOK(who) || !a.verify(password) {
		a.limiter.fail(ip)
		return errors.New("incorrect username or password")
	}
	a.limiter.reset(ip)

	exp := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: a.mint(exp), Path: "/",
		Expires: exp, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})

	// CSRF token is deliberately readable by JS so the SPA can echo it back
	// in a header; the session cookie stays HttpOnly.
	token := randomHex(32)
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: token, Path: "/",
		Expires: exp, HttpOnly: false, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (a *Auth) Logout(w http.ResponseWriter) {
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: name == sessionCookie, Secure: a.secure, SameSite: http.SameSiteLaxMode,
		})
	}
}

// mint builds "expiry.signature" where the signature covers the expiry.
func (a *Auth) mint(exp time.Time) string {
	payload := strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *Auth) valid(value string) bool {
	payload, sig, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return false
	}
	unix, err := strconv.ParseInt(payload, 10, 64)
	return err == nil && time.Now().Before(time.Unix(unix, 0))
}

func (a *Auth) IsAuthenticated(r *http.Request) bool {
	if !a.Configured() {
		return true // loopback-only mode, enforced at bind time
	}
	c, err := r.Cookie(sessionCookie)
	return err == nil && a.valid(c.Value)
}

// Protect rejects unauthenticated requests, and rejects state-changing ones
// whose CSRF header does not match the cookie.
func (a *Auth) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.IsAuthenticated(r) {
			http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
			return
		}
		if a.Configured() && isMutating(r.Method) && !a.csrfOK(r) {
			http.Error(w, `{"error":"bad CSRF token"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Auth) csrfOK(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	sent := r.Header.Get("X-CSRF-Token")
	return subtle.ConstantTimeCompare([]byte(sent), []byte(c.Value)) == 1
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
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
// arrives from localhost and would otherwise share one rate-limit bucket.
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

// limiter throttles failed logins per client IP.
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
