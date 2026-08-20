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
	pendingCookie = "goldstar_pending"
	sessionTTL    = 7 * 24 * time.Hour
	// The window to finish a 2FA step once the password has been accepted.
	// Long enough to type a code without rushing, short enough that a
	// half-finished login left open in a background tab is not a standing
	// way in.
	pendingTTL = 10 * time.Minute

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

	totpMu         sync.Mutex
	totpSecret     string    // confirmed base32 secret; empty means 2FA is not yet set up
	totpPending    string    // secret generated for an in-progress, unconfirmed setup
	totpPendingExp time.Time
	totpLastStep   int64 // most recently accepted code's time-step, so it cannot be replayed
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

// AccountLabel is the name shown next to "Garage Goldstar" in an
// authenticator app's account list, so a phone carrying several accounts can
// tell this one apart from the others.
func (a *Auth) AccountLabel() string {
	switch {
	case a.user != "":
		return a.user
	case a.email != "":
		return a.email
	default:
		return "dashboard"
	}
}

// SetHash swaps in a new password hash at runtime. Existing sessions survive:
// they are signed with the separate session secret, not the password.
func (a *Auth) SetHash(hash string) { a.hash = strings.TrimSpace(hash) }

// Login validates the password and, on success, issues a short-lived pending
// cookie rather than a real session — a second factor is mandatory once a
// password is configured, so nothing here grants dashboard access on its
// own. The caller uses the returned stage to decide what to show next:
// "setup" when this account has never completed 2FA, "verify" once it has.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request, who, password string) (stage string, err error) {
	ip := clientIP(r)
	if !a.limiter.allow(ip) {
		return "", errors.New("too many attempts, try again later")
	}
	if !a.Configured() {
		return "", ErrNoPassword
	}
	// The username and password are checked together and reported together.
	// Saying which half was wrong would tell an attacker whether an account
	// exists, and costs a legitimate user nothing.
	if !a.identityOK(who) || !a.verify(password) {
		a.limiter.fail(ip)
		return "", errors.New("incorrect username or password")
	}
	a.limiter.reset(ip)

	exp := time.Now().Add(pendingTTL)
	http.SetCookie(w, &http.Cookie{
		Name: pendingCookie, Value: a.mint("pending", exp), Path: "/",
		Expires: exp, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	if !a.TOTPConfigured() {
		return "setup", nil
	}
	return "verify", nil
}

// IssueSession grants the real, fully-authenticated session — called only
// once a second factor has actually been verified (ConfirmTOTPSetup or
// VerifyTOTPCode), never directly from Login.
func (a *Auth) IssueSession(w http.ResponseWriter) {
	exp := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: a.mint("session", exp), Path: "/",
		Expires: exp, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})

	// CSRF token is deliberately readable by JS so the SPA can echo it back
	// in a header; the session cookie stays HttpOnly.
	token := randomHex(32)
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: token, Path: "/",
		Expires: exp, HttpOnly: false, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})

	a.clearPending(w)
}

func (a *Auth) clearPending(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: pendingCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) Logout(w http.ResponseWriter) {
	for _, name := range []string{sessionCookie, csrfCookie, pendingCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: name != csrfCookie, Secure: a.secure, SameSite: http.SameSiteLaxMode,
		})
	}
}

// mint builds "purpose.expiry.signature", where the signature covers both the
// purpose and the expiry. Binding the purpose into the signed payload — not
// just the cookie name — is what stops a pending-login token (issued after
// the password alone) from being replayed as a full session cookie: valid
// checks that the purpose it recovers matches the one it was asked for, so a
// token minted for one purpose is worthless for the other even though both
// share the same signing key.
func (a *Auth) mint(purpose string, exp time.Time) string {
	payload := purpose + "." + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *Auth) valid(purpose, value string) bool {
	parts := strings.SplitN(value, ".", 3)
	if len(parts) != 3 || parts[0] != purpose {
		return false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(want)) != 1 {
		return false
	}
	unix, err := strconv.ParseInt(parts[1], 10, 64)
	return err == nil && time.Now().Before(time.Unix(unix, 0))
}

func (a *Auth) IsAuthenticated(r *http.Request) bool {
	if !a.Configured() {
		return true // loopback-only mode, enforced at bind time
	}
	c, err := r.Cookie(sessionCookie)
	return err == nil && a.valid("session", c.Value)
}

// PendingOK reports whether this request just cleared the password step and
// is mid-way through a 2FA setup or verification — the gate the totp/* HTTP
// handlers use in place of IsAuthenticated, since no real session exists yet.
func (a *Auth) PendingOK(r *http.Request) bool {
	c, err := r.Cookie(pendingCookie)
	return err == nil && a.valid("pending", c.Value)
}

// ── Two-factor authentication ────────────────────────────────────────────
//
// 2FA is mandatory the moment a password is configured: Login above never
// grants a session by itself, only a pending cookie, and the only paths to a
// real session are ConfirmTOTPSetup and VerifyTOTPCode below. There is
// deliberately no "disable 2FA" here — that has to be an operator action on
// the server (see `goldstar totp-reset`), or anyone who only ever learned the
// password could strip the second factor off through the same login form it
// exists to guard.

// TOTPConfigured reports whether this account has finished 2FA setup.
func (a *Auth) TOTPConfigured() bool {
	a.totpMu.Lock()
	defer a.totpMu.Unlock()
	return a.totpSecret != ""
}

// TOTPSecretForDisplay returns the confirmed secret so an already fully
// authenticated session can re-display it — e.g. to add the same 2FA to a
// second device. ok is false when 2FA has not been set up yet.
//
// This is safe to expose to a signed-in session specifically because
// reaching it already proves the session passed both factors: unlike the
// login-time setup/confirm endpoints, showing the existing secret back to
// its own already-authenticated owner adds no new way in.
func (a *Auth) TOTPSecretForDisplay() (secret string, ok bool) {
	a.totpMu.Lock()
	defer a.totpMu.Unlock()
	return a.totpSecret, a.totpSecret != ""
}

// SetTOTPSecret loads the confirmed secret at startup (from config) or after
// a fresh setup is confirmed.
func (a *Auth) SetTOTPSecret(secret string) {
	a.totpMu.Lock()
	defer a.totpMu.Unlock()
	a.totpSecret = strings.TrimSpace(secret)
}

// BeginTOTPSetup returns the secret to encode as a QR code. Calling it again
// while a setup is already in progress returns the same secret rather than a
// new one, so refreshing the setup page — or opening it in a second tab —
// does not invalidate a code the QR was already scanned for.
func (a *Auth) BeginTOTPSetup() (secret string, err error) {
	a.totpMu.Lock()
	defer a.totpMu.Unlock()
	if a.totpSecret != "" {
		return "", errors.New("2FA is already set up on this account")
	}
	if a.totpPending != "" && time.Now().Before(a.totpPendingExp) {
		return a.totpPending, nil
	}
	secret, err = generateTOTPSecret()
	if err != nil {
		return "", err
	}
	a.totpPending = secret
	a.totpPendingExp = time.Now().Add(pendingTTL)
	return secret, nil
}

// ConfirmTOTPSetup checks code against the secret handed out by
// BeginTOTPSetup and, on success, promotes it to the account's permanent
// secret. The caller is responsible for persisting the returned secret and
// then issuing the real session — this only updates in-memory state.
func (a *Auth) ConfirmTOTPSetup(r *http.Request, code string) (secret string, err error) {
	ip := clientIP(r)
	if !a.limiter.allow(ip) {
		return "", errors.New("too many attempts, try again later")
	}
	a.totpMu.Lock()
	pending, expired := a.totpPending, time.Now().After(a.totpPendingExp)
	a.totpMu.Unlock()
	if pending == "" || expired {
		return "", errors.New("no 2FA setup in progress — reload and scan the QR code again")
	}

	ok, step := checkTOTP(pending, code, time.Now())
	if !ok {
		a.limiter.fail(ip)
		return "", errors.New("incorrect code")
	}
	a.limiter.reset(ip)

	a.totpMu.Lock()
	a.totpSecret, a.totpPending = pending, ""
	a.totpLastStep = step
	a.totpMu.Unlock()
	return pending, nil
}

// VerifyTOTPCode checks a code from an account whose 2FA is already set up.
// Refuses to accept the same code twice — without that, a code good for up
// to a minute could be reused by anyone who saw it once.
func (a *Auth) VerifyTOTPCode(r *http.Request, code string) error {
	ip := clientIP(r)
	if !a.limiter.allow(ip) {
		return errors.New("too many attempts, try again later")
	}
	a.totpMu.Lock()
	secret, lastStep := a.totpSecret, a.totpLastStep
	a.totpMu.Unlock()
	if secret == "" {
		return errors.New("2FA is not set up")
	}

	ok, step := checkTOTP(secret, code, time.Now())
	if !ok || step <= lastStep {
		a.limiter.fail(ip)
		return errors.New("incorrect code")
	}
	a.limiter.reset(ip)

	a.totpMu.Lock()
	a.totpLastStep = step
	a.totpMu.Unlock()
	return nil
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
