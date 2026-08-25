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

	"goldstar/internal/store"
)

const (
	sessionCookie = "goldstar_session"
	csrfCookie    = "goldstar_csrf"
	pendingCookie = "goldstar_pending"
	sessionTTL    = 7 * 24 * time.Hour
	// The window to finish the rest of a login once the password has been
	// accepted — however many steps that account still has left (a forced
	// password change, 2FA setup, or just a code). Long enough to type
	// without rushing, short enough that a half-finished login left open in
	// a background tab is not a standing way in.
	pendingTTL = 10 * time.Minute

	// Argon2id parameters. 64 MiB and 3 passes is comfortably above the
	// OWASP minimum and costs a few hundred ms once per login.
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// users is the persistence Auth needs. Satisfied by *store.Store — declared
// here as its own interface, narrowed to just the handful of methods this
// package actually calls, so what auth depends on stays visible at a glance
// and a test can supply a fake without a real database.
type users interface {
	GetUserByIdentity(who string) (*store.User, error)
	GetUserByID(id int64) (*store.User, error)
	SetUserPasswordHash(id int64, hash string) error
	SetUserTOTPSecret(id int64, secret string) error
	UserCount() (int, error)
}

// pendingSetup is a 2FA secret generated for an in-progress, unconfirmed
// setup — see BeginTOTPSetup.
type pendingSetup struct {
	secret string
	exp    time.Time
}

type Auth struct {
	users   users
	secret  []byte // HMAC key for session cookies
	secure  bool   // set Secure on cookies (true behind a tunnel)
	limiter *limiter

	mu       sync.Mutex
	setups   map[int64]pendingSetup // userID -> in-progress unconfirmed 2FA setup
	lastStep map[int64]int64        // userID -> most recently accepted code's time-step (replay guard)
}

// New builds the guard. secret is persisted so sessions survive a restart;
// if it is missing one is generated. u is normally the same *store.Store the
// rest of the server already has open.
func New(u users, secretPath string, secure bool) (*Auth, error) {
	secret, err := loadOrCreateSecret(secretPath)
	if err != nil {
		return nil, err
	}
	return &Auth{
		users:    u,
		secret:   secret,
		secure:   secure,
		limiter:  newLimiter(10, 15*time.Minute),
		setups:   map[int64]pendingSetup{},
		lastStep: map[int64]int64{},
	}, nil
}

// Configured reports whether any account exists. Checked fresh against the
// database on every call, not cached, so an account added with `goldstar
// user-add` while the server keeps running takes effect immediately —
// without a password configured at all, the server refuses to bind to
// anything but loopback (see internal/web.New).
func (a *Auth) Configured() bool {
	n, err := a.users.UserCount()
	return err == nil && n > 0
}

// HashPassword produces the encoded string stored as an account's password.
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

// VerifyPassword checks a candidate password against an encoded hash in
// constant time. Exported so the Admin page's own "change password" flow
// (which already holds the current session's user and its hash) can check
// the current password directly, without going through a second account
// lookup or a login-shaped API.
func VerifyPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
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

// AccountLabel names an account in whatever authenticator app scans its QR
// code — the same account list a phone carrying several might show.
func AccountLabel(u *store.User) string {
	switch {
	case u.Username != "":
		return u.Username
	case u.Email != "":
		return u.Email
	default:
		return "dashboard"
	}
}

// Login validates the password for the named account and, on success,
// issues a short-lived pending cookie identifying that account — never a
// real session, with one deliberate exception (see "ok" below). The caller
// uses the returned stage to decide what to show next:
//
//	"change_password" — this account is still carrying a temporary
//	                     password and must set a real one before anything else
//	"setup"            — this account has never completed 2FA
//	"verify"           — 2FA is set up; a code is needed
//	"ok"               — nothing left to do; a real session has already
//	                     been issued (a TOTP-exempt account with no
//	                     password change pending — see SetUserTOTPExempt)
//
// A second factor is mandatory for every account except one narrow, explicit
// exception — nothing here grants dashboard access on its own otherwise.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request, who, password string) (stage string, err error) {
	ip := clientIP(r)
	if !a.limiter.allow(ip) {
		return "", errors.New("too many attempts, try again later")
	}
	// The username and password are checked together and reported together.
	// Saying which half was wrong would tell an attacker whether an account
	// exists, and costs a legitimate user nothing. u is only read once we
	// know the lookup succeeded — short-circuit && guarantees that.
	u, lookupErr := a.users.GetUserByIdentity(who)
	if lookupErr != nil || !VerifyPassword(u.PasswordHash, password) {
		a.limiter.fail(ip)
		return "", errors.New("incorrect username or password")
	}
	a.limiter.reset(ip)

	// A TOTP-exempt account with nothing else pending (see stageFor) signs
	// straight in on the password alone — the one deliberate exception to
	// "Login never grants a session directly". Every other account still
	// only ever gets a pending cookie here.
	if stage = stageFor(u); stage == stageOK {
		a.IssueSession(w, u.ID)
		return stage, nil
	}

	exp := time.Now().Add(pendingTTL)
	http.SetCookie(w, &http.Cookie{
		Name: pendingCookie, Value: a.mint(u.ID, "pending", exp), Path: "/",
		Expires: exp, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	return stage, nil
}

// stageOK signals a login has nothing left to do — the caller has already
// had IssueSession called for it, and should treat this exactly like a
// successful 2FA confirmation, not like "setup" or "verify".
const stageOK = "ok"

// stageFor is the single place that decides what a pending login still
// needs: a real password first if the current one is temporary, then 2FA
// (setup if it has never been done, otherwise a code) — checked in that
// order so a brand new account with neither ever skips straight to 2FA on a
// password nobody but its creator has typed. TOTPExempt accounts skip 2FA
// entirely once their password is sorted — see SetUserTOTPExempt.
func stageFor(u *store.User) string {
	switch {
	case u.MustChangePassword:
		return "change_password"
	case u.TOTPExempt:
		return stageOK
	case u.TOTPSecret == "":
		return "setup"
	default:
		return "verify"
	}
}

// ChangePasswordPending sets a real password for a still-pending login whose
// account is required to replace its temporary one. Requires a live pending
// cookie. If nothing else is pending afterward (a TOTP-exempt account,
// stageFor's stageOK case), this issues the session itself, the same as
// Login does above — the caller just relays whatever stage comes back.
func (a *Auth) ChangePasswordPending(w http.ResponseWriter, r *http.Request, newPassword string) (stage string, err error) {
	u, ok := a.PendingUser(r)
	if !ok {
		return "", errors.New("sign in again")
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return "", err
	}
	if err := a.users.SetUserPasswordHash(u.ID, hash); err != nil {
		return "", err
	}
	u.MustChangePassword = false
	stage = stageFor(u)
	if stage == stageOK {
		a.IssueSession(w, u.ID)
	}
	return stage, nil
}

// IssueSession grants the real, fully-authenticated session for the given
// account — called only once every step a pending login still needed
// (forced password change, 2FA) has actually completed.
func (a *Auth) IssueSession(w http.ResponseWriter, userID int64) {
	exp := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: a.mint(userID, "session", exp), Path: "/",
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

// mint builds "purpose.userID.expiry.signature", where the signature covers
// the whole payload. Binding the purpose into it — not just the cookie name
// — is what stops a pending-login token (issued after the password alone)
// from being replayed as a full session cookie: valid checks that the
// purpose it recovers matches the one it was asked for, so a token minted
// for one purpose is worthless for the other even though both share the same
// signing key. Binding the user id is what lets a signed, stateless cookie
// name which account it belongs to at all, now that more than one exists.
func (a *Auth) mint(userID int64, purpose string, exp time.Time) string {
	payload := purpose + "." + strconv.FormatInt(userID, 10) + "." + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *Auth) valid(purpose, value string) (userID int64, ok bool) {
	parts := strings.SplitN(value, ".", 4)
	if len(parts) != 4 || parts[0] != purpose {
		return 0, false
	}
	payload := parts[0] + "." + parts[1] + "." + parts[2]
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(parts[3]), []byte(want)) != 1 {
		return 0, false
	}
	unixExp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || !time.Now().Before(time.Unix(unixExp, 0)) {
		return 0, false
	}
	uid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return uid, true
}

// IsAuthenticated reports whether the request carries a real session — cheap
// enough to call on every request, since CurrentUser is only needed by
// handlers that actually use who signed in.
func (a *Auth) IsAuthenticated(r *http.Request) bool {
	if !a.Configured() {
		return true // loopback-only mode, enforced at bind time
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	_, ok := a.valid("session", c.Value)
	return ok
}

// CurrentUser loads the account behind a fully-authenticated session. ok is
// false with no valid session — including "no password configured" mode,
// where nothing gates the dashboard and there is no real account to name.
func (a *Auth) CurrentUser(r *http.Request) (*store.User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, false
	}
	id, ok := a.valid("session", c.Value)
	if !ok {
		return nil, false
	}
	u, err := a.users.GetUserByID(id)
	if err != nil {
		return nil, false
	}
	return u, true
}

// PendingUser loads the account mid-login — the gate the totp/* and
// change-password HTTP handlers use in place of CurrentUser, since no real
// session exists yet.
func (a *Auth) PendingUser(r *http.Request) (*store.User, bool) {
	c, err := r.Cookie(pendingCookie)
	if err != nil {
		return nil, false
	}
	id, ok := a.valid("pending", c.Value)
	if !ok {
		return nil, false
	}
	u, err := a.users.GetUserByID(id)
	if err != nil {
		return nil, false
	}
	return u, true
}

// ── Two-factor authentication ────────────────────────────────────────────
//
// 2FA is mandatory for every account: Login above never grants a session by
// itself, only a pending cookie, and the only paths to a real session are
// ConfirmTOTPSetup and VerifyTOTPCode below. There is deliberately no
// "disable 2FA" here — that has to be an operator action on the server (see
// `goldstar totp-reset`), or anyone who only ever learned the password could
// strip the second factor off through the same login form it exists to guard.

// BeginTOTPSetup returns the secret to encode as a QR code for the
// currently-pending login. Calling it again while a setup is already in
// progress returns the same secret rather than a new one, so refreshing the
// setup page — or opening it in a second tab — does not invalidate a code
// the QR was already scanned for.
func (a *Auth) BeginTOTPSetup(r *http.Request) (secret string, err error) {
	u, ok := a.PendingUser(r)
	if !ok {
		return "", errors.New("sign in again")
	}
	// Enforced here too, not just by the HTTP layer following stageFor's
	// order: an account carrying a temporary password must replace it
	// before 2FA can be locked in on top of it — otherwise a client that
	// skips straight to this endpoint could leave 2FA guarding an account
	// whose password nobody but its creator was ever meant to see stay set
	// to that temporary value indefinitely.
	if u.MustChangePassword {
		return "", errors.New("set a real password before setting up two-factor authentication")
	}
	if u.TOTPSecret != "" {
		return "", errors.New("2FA is already set up on this account")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if p, exists := a.setups[u.ID]; exists && time.Now().Before(p.exp) {
		return p.secret, nil
	}
	secret, err = generateTOTPSecret()
	if err != nil {
		return "", err
	}
	a.setups[u.ID] = pendingSetup{secret: secret, exp: time.Now().Add(pendingTTL)}
	return secret, nil
}

// ConfirmTOTPSetup checks code against the secret BeginTOTPSetup handed out
// for the currently-pending login and, on success, saves it as the
// account's permanent secret and returns the user id so the caller can
// issue the real session.
func (a *Auth) ConfirmTOTPSetup(r *http.Request, code string) (userID int64, err error) {
	u, ok := a.PendingUser(r)
	if !ok {
		return 0, errors.New("sign in again")
	}
	ip := clientIP(r)
	if !a.limiter.allow(ip) {
		return 0, errors.New("too many attempts, try again later")
	}

	a.mu.Lock()
	pending, exists := a.setups[u.ID]
	a.mu.Unlock()
	if !exists || time.Now().After(pending.exp) {
		return 0, errors.New("no 2FA setup in progress — reload and scan the QR code again")
	}

	ok2, step := checkTOTP(pending.secret, code, time.Now())
	if !ok2 {
		a.limiter.fail(ip)
		return 0, errors.New("incorrect code")
	}
	a.limiter.reset(ip)

	if err := a.users.SetUserTOTPSecret(u.ID, pending.secret); err != nil {
		return 0, err
	}
	a.mu.Lock()
	delete(a.setups, u.ID)
	a.lastStep[u.ID] = step
	a.mu.Unlock()
	return u.ID, nil
}

// VerifyTOTPCode checks a code from an account whose 2FA is already set up
// and, on success, returns the user id so the caller can issue the real
// session. Refuses to accept the same code twice — without that, a code
// good for up to a minute could be reused by anyone who saw it once.
func (a *Auth) VerifyTOTPCode(r *http.Request, code string) (userID int64, err error) {
	u, ok := a.PendingUser(r)
	if !ok {
		return 0, errors.New("sign in again")
	}
	ip := clientIP(r)
	if !a.limiter.allow(ip) {
		return 0, errors.New("too many attempts, try again later")
	}
	if u.TOTPSecret == "" {
		return 0, errors.New("2FA is not set up")
	}

	a.mu.Lock()
	lastStep := a.lastStep[u.ID]
	a.mu.Unlock()

	ok2, step := checkTOTP(u.TOTPSecret, code, time.Now())
	if !ok2 || step <= lastStep {
		a.limiter.fail(ip)
		return 0, errors.New("incorrect code")
	}
	a.limiter.reset(ip)

	a.mu.Lock()
	a.lastStep[u.ID] = step
	a.mu.Unlock()
	return u.ID, nil
}

// TOTPSecretForDisplay returns the confirmed secret for the current
// session's own account, so an already fully authenticated session can
// re-display it — e.g. to add the same 2FA to a second device. ok is false
// when there is no session, or 2FA has not been set up yet.
//
// This is safe to expose to a signed-in session specifically because
// reaching it already proves the session passed both factors: unlike the
// login-time setup/confirm endpoints, showing the existing secret back to
// its own already-authenticated owner adds no new way in.
func (a *Auth) TOTPSecretForDisplay(r *http.Request) (secret string, ok bool) {
	u, authed := a.CurrentUser(r)
	if !authed || u.TOTPSecret == "" {
		return "", false
	}
	return u.TOTPSecret, true
}

// Protect rejects unauthenticated requests, and rejects state-changing ones
// whose CSRF header does not match the cookie.
func (a *Auth) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.IsAuthenticated(r) {
			http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
			return
		}
		if isMutating(r.Method) {
			if a.Configured() && !a.csrfOK(r) {
				http.Error(w, `{"error":"bad CSRF token"}`, http.StatusForbidden)
				return
			}
			// Checked centrally here, not per-route, so ReadOnly applies to
			// every mutating endpoint uniformly the moment it's set on an
			// account — nothing to remember to add it to later. Reads
			// (GET) are never touched: "view only" means every GET still
			// works exactly as normal.
			if u, ok := a.CurrentUser(r); ok && u.ReadOnly {
				http.Error(w, `{"error":"this is a view-only account — changes are disabled"}`, http.StatusForbidden)
				return
			}
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
