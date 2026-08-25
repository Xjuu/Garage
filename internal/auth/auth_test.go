package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"goldstar/internal/store"
)

// fakeUsers is a tiny in-memory stand-in for *store.Store, satisfying the
// users interface without a real database — fast, and keeps this package's
// tests independent of internal/store's own test suite.
type fakeUsers struct {
	byID map[int64]*store.User
	next int64
}

func newFakeUsers() *fakeUsers { return &fakeUsers{byID: map[int64]*store.User{}} }

// addUser is the test-only convenience constructor: hashes password the same
// way a real signup would, and returns the row so a test can inspect or
// mutate it afterward (e.g. flipping MustChangePassword).
func (f *fakeUsers) addUser(t *testing.T, username, password, role string) *store.User {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	f.next++
	u := &store.User{ID: f.next, Username: username, Role: role, PasswordHash: hash}
	f.byID[u.ID] = u
	return u
}

func (f *fakeUsers) GetUserByIdentity(who string) (*store.User, error) {
	for _, u := range f.byID {
		if u.Username == who || (u.Email != "" && u.Email == who) {
			return u, nil
		}
	}
	return nil, store.ErrUserNotFound
}

func (f *fakeUsers) GetUserByID(id int64) (*store.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, store.ErrUserNotFound
}

func (f *fakeUsers) SetUserPasswordHash(id int64, hash string) error {
	u, ok := f.byID[id]
	if !ok {
		return store.ErrUserNotFound
	}
	u.PasswordHash = hash
	u.MustChangePassword = false
	return nil
}

func (f *fakeUsers) SetUserTOTPSecret(id int64, secret string) error {
	u, ok := f.byID[id]
	if !ok {
		return store.ErrUserNotFound
	}
	u.TOTPSecret = secret
	return nil
}

func (f *fakeUsers) UserCount() (int, error) { return len(f.byID), nil }

// newTestAuth builds an Auth backed by a single admin account named "alice"
// with the given password — the common case most tests below need.
func newTestAuth(t *testing.T, password string) (*Auth, *fakeUsers, *store.User) {
	t.Helper()
	users := newFakeUsers()
	u := users.addUser(t, "alice", password, store.RoleAdmin)
	a, err := New(users, filepath.Join(t.TempDir(), "session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, users, u
}

// cookieFrom pulls a named cookie's value out of a recorded response, or
// fails the test — every test below needs to know whether a particular
// cookie actually got set.
func cookieFrom(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	t.Fatalf("no %s cookie was set; got %v", name, rec.Result().Cookies())
	return ""
}

func hasCookie(rec *httptest.ResponseRecorder, name string) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}

func reqWithCookie(name, value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: name, Value: value})
	return r
}

// The whole point of mandatory 2FA: a correct password alone must never be
// enough to reach the dashboard. Login has to stop at a pending cookie, not
// a session, whether or not this account has finished 2FA setup yet.
func TestLoginNeverIssuesASessionDirectly(t *testing.T) {
	for _, totpConfigured := range []bool{false, true} {
		a, _, u := newTestAuth(t, "correct horse battery staple")
		if totpConfigured {
			u.TOTPSecret = "JBSWY3DPEHPK3PXP"
		}

		rec := httptest.NewRecorder()
		stage, err := a.Login(rec, httptest.NewRequest(http.MethodPost, "/api/login", nil),
			"alice", "correct horse battery staple")
		if err != nil {
			t.Fatalf("Login: %v", err)
		}

		wantStage := "setup"
		if totpConfigured {
			wantStage = "verify"
		}
		if stage != wantStage {
			t.Errorf("stage = %q, want %q", stage, wantStage)
		}
		if hasCookie(rec, sessionCookie) {
			t.Errorf("Login set a session cookie before 2FA was completed (totpConfigured=%v)", totpConfigured)
		}
		if !hasCookie(rec, pendingCookie) {
			t.Errorf("Login did not set a pending cookie")
		}

		// And that pending cookie, on its own, does not authenticate anything.
		pending := cookieFrom(t, rec, pendingCookie)
		if a.IsAuthenticated(reqWithCookie(sessionCookie, pending)) {
			t.Fatalf("a pending-purpose token was accepted as a session — 2FA can be bypassed")
		}
	}
}

// This is the property the "purpose" tag inside mint/valid exists for:
// without it, a pending token and a session token are byte-for-byte the same
// shape (userID + expiry + HMAC), so whoever captures the pending cookie
// mid-login could just resend it under the session cookie's name and skip
// the second factor entirely.
func TestPendingCookieCannotBeReplayedAsASession(t *testing.T) {
	a, _, _ := newTestAuth(t, "correct horse battery staple")
	rec := httptest.NewRecorder()
	if _, err := a.Login(rec, httptest.NewRequest(http.MethodPost, "/", nil),
		"alice", "correct horse battery staple"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	pending := cookieFrom(t, rec, pendingCookie)

	attack := reqWithCookie(sessionCookie, pending)
	if a.IsAuthenticated(attack) {
		t.Fatalf("pending cookie value accepted as a session cookie — mandatory 2FA is bypassable")
	}

	// The reverse must hold too: a real session token must not pass as a
	// pending one.
	a2, _, u2 := newTestAuth(t, "correct horse battery staple")
	u2.TOTPSecret = "JBSWY3DPEHPK3PXP"
	sessRec := httptest.NewRecorder()
	a2.IssueSession(sessRec, u2.ID)
	session := cookieFrom(t, sessRec, sessionCookie)
	if _, ok := a2.PendingUser(reqWithCookie(pendingCookie, session)); ok {
		t.Fatalf("session cookie value accepted as a pending cookie")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	a, _, _ := newTestAuth(t, "correct horse battery staple")
	rec := httptest.NewRecorder()
	_, err := a.Login(rec, httptest.NewRequest(http.MethodPost, "/", nil), "alice", "wrong password")
	if err == nil {
		t.Fatalf("want an error for a wrong password")
	}
	if hasCookie(rec, pendingCookie) || hasCookie(rec, sessionCookie) {
		t.Fatalf("a failed login must not set any cookie")
	}
}

// A username that doesn't exist at all must fail exactly like a wrong
// password — reporting anything more specific would let a login attempt
// enumerate which accounts exist.
func TestLoginRejectsUnknownUsername(t *testing.T) {
	a, _, _ := newTestAuth(t, "correct horse battery staple")
	rec := httptest.NewRecorder()
	_, err := a.Login(rec, httptest.NewRequest(http.MethodPost, "/", nil), "nobody", "correct horse battery staple")
	if err == nil {
		t.Fatalf("want an error for an unknown username")
	}
	if hasCookie(rec, pendingCookie) || hasCookie(rec, sessionCookie) {
		t.Fatalf("a failed login must not set any cookie")
	}
}

// Setting up 2FA for the first time: BeginTOTPSetup hands out a secret,
// ConfirmTOTPSetup only accepts the matching code, and success is what
// finally issues a real session for the right account.
func TestTOTPSetupFlow(t *testing.T) {
	a, _, u := newTestAuth(t, "correct horse battery staple")
	loginRec := httptest.NewRecorder()
	if _, err := a.Login(loginRec, httptest.NewRequest(http.MethodPost, "/", nil),
		"alice", "correct horse battery staple"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	pending := cookieFrom(t, loginRec, pendingCookie)
	reqFor := func() *http.Request { return reqWithCookie(pendingCookie, pending) }

	secret, err := a.BeginTOTPSetup(reqFor())
	if err != nil {
		t.Fatalf("BeginTOTPSetup: %v", err)
	}
	if u.TOTPSecret != "" {
		t.Fatalf("the account's TOTPSecret must stay empty until the setup code is confirmed")
	}

	code, err := hotp(secret, totpStep(time.Now()))
	if err != nil {
		t.Fatalf("hotp: %v", err)
	}

	if _, err := a.ConfirmTOTPSetup(reqFor(), "000000"); err == nil {
		t.Fatalf("a wrong code must not confirm setup")
	}
	userID, err := a.ConfirmTOTPSetup(reqFor(), code)
	if err != nil {
		t.Fatalf("ConfirmTOTPSetup with the correct code: %v", err)
	}
	if userID != u.ID {
		t.Fatalf("ConfirmTOTPSetup userID = %d, want %d", userID, u.ID)
	}
	if u.TOTPSecret == "" {
		t.Fatalf("the account's TOTPSecret should be set once the code is confirmed")
	}
}

// Everyday login once 2FA is already set up: right code passes, wrong code
// fails, and the same code cannot be replayed a second time.
func TestVerifyTOTPCodeAndReplayProtection(t *testing.T) {
	a, _, u := newTestAuth(t, "correct horse battery staple")
	secret, _ := generateTOTPSecret()
	u.TOTPSecret = secret

	loginRec := httptest.NewRecorder()
	if _, err := a.Login(loginRec, httptest.NewRequest(http.MethodPost, "/", nil),
		"alice", "correct horse battery staple"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	pending := cookieFrom(t, loginRec, pendingCookie)
	reqFor := func() *http.Request { return reqWithCookie(pendingCookie, pending) }

	code, err := hotp(secret, totpStep(time.Now()))
	if err != nil {
		t.Fatalf("hotp: %v", err)
	}

	if _, err := a.VerifyTOTPCode(reqFor(), "000000"); err == nil {
		t.Fatalf("a wrong code must be rejected")
	}
	userID, err := a.VerifyTOTPCode(reqFor(), code)
	if err != nil {
		t.Fatalf("the correct current code should be accepted: %v", err)
	}
	if userID != u.ID {
		t.Fatalf("VerifyTOTPCode userID = %d, want %d", userID, u.ID)
	}
	if _, err := a.VerifyTOTPCode(reqFor(), code); err == nil {
		t.Fatalf("the same code must not be accepted twice")
	}
}

// TOTPSecretForDisplay is the "add 2FA to another device" feature's data
// source: it must report nothing without a real session, and must return the
// same secret an authenticated session's own account has confirmed — not a
// freshly generated one — since the whole point is scanning the existing
// enrollment onto a second device.
func TestTOTPSecretForDisplay(t *testing.T) {
	a, _, u := newTestAuth(t, "correct horse battery staple")

	anon := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := a.TOTPSecretForDisplay(anon); ok {
		t.Fatalf("no session at all should report ok=false")
	}

	secret, _ := generateTOTPSecret()
	u.TOTPSecret = secret

	sessRec := httptest.NewRecorder()
	a.IssueSession(sessRec, u.ID)
	session := cookieFrom(t, sessRec, sessionCookie)

	got, ok := a.TOTPSecretForDisplay(reqWithCookie(sessionCookie, session))
	if !ok {
		t.Fatalf("ok=false for a session whose account has 2FA set up")
	}
	if got != secret {
		t.Fatalf("TOTPSecretForDisplay = %q, want the same secret %q", got, secret)
	}
}

// A temporary password blocks everything else until it's replaced — the new
// admin account flow: Login stops at "change_password" before ever offering
// 2FA setup, ChangePasswordPending clears the flag and the pending cookie
// still identifies the same account afterward, and only then does the
// normal "setup" stage (this account has no 2FA yet either) show up.
func TestMustChangePasswordBlocksLoginUntilReplaced(t *testing.T) {
	users := newFakeUsers()
	u := users.addUser(t, "klon", "1234567890", store.RoleAdmin)
	u.MustChangePassword = true
	a, err := New(users, filepath.Join(t.TempDir(), "session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	loginRec := httptest.NewRecorder()
	stage, err := a.Login(loginRec, httptest.NewRequest(http.MethodPost, "/", nil), "klon", "1234567890")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if stage != "change_password" {
		t.Fatalf("stage = %q, want %q", stage, "change_password")
	}
	pending := cookieFrom(t, loginRec, pendingCookie)

	// The pending cookie alone must not be enough to skip straight to 2FA
	// setup while the account is still on its temporary password — even a
	// client that never goes through the HTTP layer's own stage check.
	if _, err := a.BeginTOTPSetup(reqWithCookie(pendingCookie, pending)); err == nil {
		t.Fatalf("BeginTOTPSetup must refuse an account that still has MustChangePassword set")
	}

	newStage, err := a.ChangePasswordPending(httptest.NewRecorder(), reqWithCookie(pendingCookie, pending), "a genuinely new password")
	if err != nil {
		t.Fatalf("ChangePasswordPending: %v", err)
	}
	if newStage != "setup" {
		t.Fatalf("stage after changing password = %q, want %q (no 2FA on this account yet)", newStage, "setup")
	}
	if u.MustChangePassword {
		t.Fatalf("MustChangePassword should be cleared once a new password is set")
	}

	// The old temporary password must no longer work, and the new one must.
	if !VerifyPassword(u.PasswordHash, "a genuinely new password") {
		t.Fatalf("the new password was not actually saved")
	}
	if VerifyPassword(u.PasswordHash, "1234567890") {
		t.Fatalf("the old temporary password should no longer verify")
	}
}

// Configured() reflects whatever the store reports, live — not a value
// captured once at startup, since an account added with `goldstar user-add`
// to a running server has to work without a restart.
func TestConfiguredReflectsLiveUserCount(t *testing.T) {
	users := newFakeUsers()
	a, err := New(users, filepath.Join(t.TempDir(), "session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Configured() {
		t.Fatalf("Configured() should be false with zero accounts")
	}
	users.addUser(t, "alice", "correct horse battery staple", store.RoleAdmin)
	if !a.Configured() {
		t.Fatalf("Configured() should be true once an account exists")
	}
}

// authenticatedRequest builds a request carrying both the session and CSRF
// cookies IssueSession sets, plus the matching X-CSRF-Token header — the
// full shape a real signed-in browser sends on a mutating call, so Protect's
// CSRF check passes and whatever it decides next (read-only or not) is what
// is actually being tested.
func authenticatedRequest(t *testing.T, a *Auth, userID int64, method, path string) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	a.IssueSession(rec, userID)
	session := cookieFrom(t, rec, sessionCookie)
	csrf := cookieFrom(t, rec, csrfCookie)

	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	r.Header.Set("X-CSRF-Token", csrf)
	return r
}

// Protect is the actual enforcement point for ReadOnly — not any one route.
// A read-only account's mutating requests must be refused regardless of a
// valid session and a correct CSRF token; its GET requests must go through
// exactly as normal ("view only" has to mean every view still works).
func TestProtectBlocksMutatingRequestsForAReadOnlyAccount(t *testing.T) {
	users := newFakeUsers()
	u := users.addUser(t, "temporary", "GoldStar1234!", store.RoleFleet)
	u.ReadOnly = true
	a, err := New(users, filepath.Join(t.TempDir(), "session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	protected := a.Protect(next)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		reached = false
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, authenticatedRequest(t, a, u.ID, method, "/api/invoices/1"))
		if reached {
			t.Errorf("%s: a read-only account's mutating request reached the handler", method)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want %d", method, rec.Code, http.StatusForbidden)
		}
	}

	reached = false
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, authenticatedRequest(t, a, u.ID, http.MethodGet, "/api/invoices"))
	if !reached {
		t.Fatalf("GET: a read-only account's read request should still reach the handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: status = %d, want 200 (net effect of an unwritten header)", rec.Code)
	}
}

// The counterpart: an ordinary (non-read-only) account's mutating request,
// carrying a valid CSRF token, must pass straight through.
func TestProtectAllowsMutatingRequestsForAnOrdinaryAccount(t *testing.T) {
	users := newFakeUsers()
	u := users.addUser(t, "faz", "correct horse battery staple", store.RoleFleet)
	a, err := New(users, filepath.Join(t.TempDir(), "session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	reached := false
	protected := a.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, authenticatedRequest(t, a, u.ID, http.MethodPost, "/api/invoices/1"))
	if !reached {
		t.Fatalf("an ordinary account's mutating request should reach the handler")
	}
}

// A TOTP-exempt account — a deliberately shared "Temporary" login — signs
// straight in on the password alone: Login issues a real session directly,
// with no pending cookie and no 2FA step, unlike every other account.
func TestTOTPExemptAccountSkipsStraightToASession(t *testing.T) {
	users := newFakeUsers()
	u := users.addUser(t, "temporary", "GoldStar1234!", store.RoleFleet)
	u.TOTPExempt = true
	a, err := New(users, filepath.Join(t.TempDir(), "session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	stage, err := a.Login(rec, httptest.NewRequest(http.MethodPost, "/", nil), "temporary", "GoldStar1234!")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if stage != "ok" {
		t.Fatalf("stage = %q, want %q", stage, "ok")
	}
	if hasCookie(rec, pendingCookie) {
		t.Fatalf("a TOTP-exempt account should never get a pending cookie — nothing is pending")
	}
	session := cookieFrom(t, rec, sessionCookie)
	if !a.IsAuthenticated(reqWithCookie(sessionCookie, session)) {
		t.Fatalf("Login should have issued a real, working session directly")
	}

	// A second login is exactly as immediate — this isn't a one-time
	// allowance, the account is permanently exempt.
	rec2 := httptest.NewRecorder()
	stage2, err := a.Login(rec2, httptest.NewRequest(http.MethodPost, "/", nil), "temporary", "GoldStar1234!")
	if err != nil || stage2 != "ok" || !hasCookie(rec2, sessionCookie) {
		t.Fatalf("a second login should behave identically: stage=%q err=%v", stage2, err)
	}
}

// A TOTP-exempt account still on a forced temporary password (an unusual
// combination, but not one the type system rules out) must change its
// password first — exemption from 2FA is not exemption from that.
func TestTOTPExemptAccountStillMustChangeATemporaryPasswordFirst(t *testing.T) {
	users := newFakeUsers()
	u := users.addUser(t, "temporary", "GoldStar1234!", store.RoleFleet)
	u.TOTPExempt = true
	u.MustChangePassword = true
	a, err := New(users, filepath.Join(t.TempDir(), "session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	loginRec := httptest.NewRecorder()
	stage, err := a.Login(loginRec, httptest.NewRequest(http.MethodPost, "/", nil), "temporary", "GoldStar1234!")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if stage != "change_password" {
		t.Fatalf("stage = %q, want %q — a temporary password still has to be replaced first", stage, "change_password")
	}
	if hasCookie(loginRec, sessionCookie) {
		t.Fatalf("no session yet — the password still has to be changed")
	}
	pending := cookieFrom(t, loginRec, pendingCookie)

	changeRec := httptest.NewRecorder()
	newStage, err := a.ChangePasswordPending(changeRec, reqWithCookie(pendingCookie, pending), "a genuinely new password")
	if err != nil {
		t.Fatalf("ChangePasswordPending: %v", err)
	}
	if newStage != "ok" {
		t.Fatalf("stage after changing password = %q, want %q — exempt from 2FA, so nothing else is pending", newStage, "ok")
	}
	if !hasCookie(changeRec, sessionCookie) {
		t.Fatalf("ChangePasswordPending should have issued the session once the password was fixed")
	}
}
