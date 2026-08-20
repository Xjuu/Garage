package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func newTestAuth(t *testing.T, password string) *Auth {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	a, err := New(hash, filepath.Join(t.TempDir(), "session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
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
		a := newTestAuth(t, "correct horse battery staple")
		if totpConfigured {
			a.SetTOTPSecret("JBSWY3DPEHPK3PXP")
		}

		rec := httptest.NewRecorder()
		stage, err := a.Login(rec, httptest.NewRequest(http.MethodPost, "/api/login", nil),
			"", "correct horse battery staple")
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
// shape (expiry + HMAC), so whoever captures the pending cookie mid-login
// could just resend it under the session cookie's name and skip the second
// factor entirely.
func TestPendingCookieCannotBeReplayedAsASession(t *testing.T) {
	a := newTestAuth(t, "correct horse battery staple")
	rec := httptest.NewRecorder()
	if _, err := a.Login(rec, httptest.NewRequest(http.MethodPost, "/", nil),
		"", "correct horse battery staple"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	pending := cookieFrom(t, rec, pendingCookie)

	attack := reqWithCookie(sessionCookie, pending)
	if a.IsAuthenticated(attack) {
		t.Fatalf("pending cookie value accepted as a session cookie — mandatory 2FA is bypassable")
	}

	// The reverse must hold too: a real session token must not pass as a
	// pending one.
	a2 := newTestAuth(t, "correct horse battery staple")
	a2.SetTOTPSecret("JBSWY3DPEHPK3PXP")
	sessRec := httptest.NewRecorder()
	a2.IssueSession(sessRec)
	session := cookieFrom(t, sessRec, sessionCookie)
	if a2.PendingOK(reqWithCookie(pendingCookie, session)) {
		t.Fatalf("session cookie value accepted as a pending cookie")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	a := newTestAuth(t, "correct horse battery staple")
	rec := httptest.NewRecorder()
	_, err := a.Login(rec, httptest.NewRequest(http.MethodPost, "/", nil), "", "wrong password")
	if err == nil {
		t.Fatalf("want an error for a wrong password")
	}
	if hasCookie(rec, pendingCookie) || hasCookie(rec, sessionCookie) {
		t.Fatalf("a failed login must not set any cookie")
	}
}

// Setting up 2FA for the first time: BeginTOTPSetup hands out a secret,
// ConfirmTOTPSetup only accepts the matching code, and success is what
// finally issues a real session.
func TestTOTPSetupFlow(t *testing.T) {
	a := newTestAuth(t, "correct horse battery staple")
	loginRec := httptest.NewRecorder()
	if _, err := a.Login(loginRec, httptest.NewRequest(http.MethodPost, "/", nil),
		"", "correct horse battery staple"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	secret, err := a.BeginTOTPSetup()
	if err != nil {
		t.Fatalf("BeginTOTPSetup: %v", err)
	}
	if a.TOTPConfigured() {
		t.Fatalf("TOTPConfigured must stay false until the setup code is confirmed")
	}

	code, err := hotp(secret, totpStep(time.Now()))
	if err != nil {
		t.Fatalf("hotp: %v", err)
	}

	if _, err := a.ConfirmTOTPSetup(httptest.NewRequest(http.MethodPost, "/", nil), "000000"); err == nil {
		t.Fatalf("a wrong code must not confirm setup")
	}
	if _, err := a.ConfirmTOTPSetup(httptest.NewRequest(http.MethodPost, "/", nil), code); err != nil {
		t.Fatalf("ConfirmTOTPSetup with the correct code: %v", err)
	}
	if !a.TOTPConfigured() {
		t.Fatalf("TOTPConfigured should be true once the code is confirmed")
	}
}

// Everyday login once 2FA is already set up: right code passes, wrong code
// fails, and the same code cannot be replayed a second time.
func TestVerifyTOTPCodeAndReplayProtection(t *testing.T) {
	a := newTestAuth(t, "correct horse battery staple")
	secret, _ := generateTOTPSecret()
	a.SetTOTPSecret(secret)

	code, err := hotp(secret, totpStep(time.Now()))
	if err != nil {
		t.Fatalf("hotp: %v", err)
	}

	if err := a.VerifyTOTPCode(httptest.NewRequest(http.MethodPost, "/", nil), "000000"); err == nil {
		t.Fatalf("a wrong code must be rejected")
	}
	if err := a.VerifyTOTPCode(httptest.NewRequest(http.MethodPost, "/", nil), code); err != nil {
		t.Fatalf("the correct current code should be accepted: %v", err)
	}
	if err := a.VerifyTOTPCode(httptest.NewRequest(http.MethodPost, "/", nil), code); err == nil {
		t.Fatalf("the same code must not be accepted twice")
	}
}

