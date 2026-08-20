package partsauth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func newTestAuth(t *testing.T, pin string) *Auth {
	t.Helper()
	a, err := New(pin, filepath.Join(t.TempDir(), "parts-session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func cookieFrom(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s cookie was set", name)
	return nil
}

func TestCheckPINAcceptsCorrectCode(t *testing.T) {
	a := newTestAuth(t, "602314")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if err := a.CheckPIN(r, "602314"); err != nil {
		t.Fatalf("CheckPIN: %v", err)
	}
}

func TestCheckPINRejectsWrongCode(t *testing.T) {
	a := newTestAuth(t, "602314")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if err := a.CheckPIN(r, "000000"); err == nil {
		t.Fatalf("a wrong PIN must be rejected")
	}
}

func TestCheckPINWithNoPINConfigured(t *testing.T) {
	a := newTestAuth(t, "")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if err := a.CheckPIN(r, "602314"); err != ErrNoPIN {
		t.Fatalf("CheckPIN with nothing configured = %v, want ErrNoPIN", err)
	}
}

// Six digits is only a million possibilities — this is the whole reason the
// limiter exists. Same request pattern an attacker would actually use:
// repeated wrong guesses from one IP.
func TestCheckPINRateLimitsRepeatedFailures(t *testing.T) {
	a := newTestAuth(t, "602314")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")

	var lastErr error
	for i := 0; i < 20; i++ {
		lastErr = a.CheckPIN(r, "000000")
	}
	if lastErr == nil || lastErr.Error() != "too many attempts, try again later" {
		t.Fatalf("after 20 wrong guesses, want rate-limited, got: %v", lastErr)
	}

	// And the limiter must not have blocked some OTHER IP in the meantime —
	// it is keyed per address, not global.
	other := httptest.NewRequest(http.MethodPost, "/", nil)
	other.Header.Set("CF-Connecting-IP", "203.0.113.99")
	if err := a.CheckPIN(other, "602314"); err != nil {
		t.Fatalf("a different IP must not be caught by another IP's lockout: %v", err)
	}
}

func TestCheckPINResetsLimiterOnSuccess(t *testing.T) {
	a := newTestAuth(t, "602314")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("CF-Connecting-IP", "203.0.113.10")

	for i := 0; i < 5; i++ {
		a.CheckPIN(r, "000000")
	}
	if err := a.CheckPIN(r, "602314"); err != nil {
		t.Fatalf("correct PIN after some failures should still work: %v", err)
	}
	// The slate is clean again — five more failures should not be enough on
	// their own to trip a limiter that was just reset.
	for i := 0; i < 5; i++ {
		a.CheckPIN(r, "000000")
	}
	if err := a.CheckPIN(r, "602314"); err != nil {
		t.Fatalf("limiter should have been reset by the earlier success: %v", err)
	}
}

func TestIssueAndValidateDeviceRoundTrip(t *testing.T) {
	a := newTestAuth(t, "602314")
	rec := httptest.NewRecorder()
	issued := a.IssueDevice(rec)

	c := cookieFrom(t, rec, "goldstar_parts_device")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)

	id, ok := a.ValidateDevice(r)
	if !ok {
		t.Fatalf("a freshly issued device cookie should validate")
	}
	if id != issued {
		t.Fatalf("ValidateDevice returned %q, want the issued id %q", id, issued)
	}
}

func TestValidateDeviceRejectsNoCookie(t *testing.T) {
	a := newTestAuth(t, "602314")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := a.ValidateDevice(r); ok {
		t.Fatalf("no cookie at all must not validate")
	}
}

func TestValidateDeviceRejectsTamperedID(t *testing.T) {
	a := newTestAuth(t, "602314")
	rec := httptest.NewRecorder()
	a.IssueDevice(rec)
	c := cookieFrom(t, rec, "goldstar_parts_device")

	// Swap the device id but keep the original signature — the classic
	// tamper attempt: claim to be a different, perhaps still-active device.
	parts := splitCookie(c.Value)
	forged := "attacker-controlled-id." + parts[1] + "." + parts[2]

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "goldstar_parts_device", Value: forged})
	if _, ok := a.ValidateDevice(r); ok {
		t.Fatalf("a tampered device id with the old signature must not validate")
	}
}

func TestValidateDeviceRejectsWrongSecret(t *testing.T) {
	a1 := newTestAuth(t, "602314")
	a2 := newTestAuth(t, "602314") // a different signing key, same PIN
	rec := httptest.NewRecorder()
	a1.IssueDevice(rec)
	c := cookieFrom(t, rec, "goldstar_parts_device")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	if _, ok := a2.ValidateDevice(r); ok {
		t.Fatalf("a cookie signed by a different secret must not validate")
	}
}

func TestValidateDeviceRejectsExpired(t *testing.T) {
	a := newTestAuth(t, "602314")
	// mint() directly with an already-past expiry — the only way to test
	// expiry without waiting a year for IssueDevice's real TTL to pass.
	expired := a.mint("some-device", time.Now().Add(-time.Hour))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "goldstar_parts_device", Value: expired})
	if _, ok := a.ValidateDevice(r); ok {
		t.Fatalf("an expired device cookie must not validate")
	}
}

func TestClearDeviceExpiresTheCookie(t *testing.T) {
	a := newTestAuth(t, "602314")
	rec := httptest.NewRecorder()
	a.ClearDevice(rec)
	c := cookieFrom(t, rec, "goldstar_parts_device")
	if c.MaxAge >= 0 {
		t.Fatalf("ClearDevice's cookie should have a negative MaxAge, got %d", c.MaxAge)
	}
}

func TestCSRFOKRequiresMatchingHeaderAndCookie(t *testing.T) {
	a := newTestAuth(t, "602314")
	rec := httptest.NewRecorder()
	a.IssueDevice(rec)
	csrf := cookieFrom(t, rec, "goldstar_parts_csrf")

	good := httptest.NewRequest(http.MethodPost, "/", nil)
	good.AddCookie(csrf)
	good.Header.Set("X-CSRF-Token", csrf.Value)
	if !a.CSRFOK(good) {
		t.Fatalf("a request whose header matches its own CSRF cookie should pass")
	}

	wrongHeader := httptest.NewRequest(http.MethodPost, "/", nil)
	wrongHeader.AddCookie(csrf)
	wrongHeader.Header.Set("X-CSRF-Token", "something-else")
	if a.CSRFOK(wrongHeader) {
		t.Fatalf("a mismatched header must fail — this is the actual point of the check")
	}

	noHeader := httptest.NewRequest(http.MethodPost, "/", nil)
	noHeader.AddCookie(csrf)
	if a.CSRFOK(noHeader) {
		t.Fatalf("no header at all (a cross-site form post, say) must fail")
	}

	noCookie := httptest.NewRequest(http.MethodPost, "/", nil)
	noCookie.Header.Set("X-CSRF-Token", csrf.Value)
	if a.CSRFOK(noCookie) {
		t.Fatalf("no CSRF cookie at all must fail")
	}
}

func splitCookie(v string) []string {
	out := []string{"", "", ""}
	i := 0
	start := 0
	for j := 0; j < len(v) && i < 2; j++ {
		if v[j] == '.' {
			out[i] = v[start:j]
			start = j + 1
			i++
		}
	}
	out[2] = v[start:]
	return out
}
