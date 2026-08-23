package repairsauth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestAuth(t *testing.T, pin string) *Auth {
	t.Helper()
	a, err := New(pin, filepath.Join(t.TempDir(), "repairs-session.key"), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func req(ip string) *http.Request {
	r := httptest.NewRequest("POST", "/api/repairs/login", nil)
	r.RemoteAddr = ip + ":12345"
	return r
}

func TestCheckPINAcceptsCorrectCode(t *testing.T) {
	a := newTestAuth(t, "112233")
	if err := a.CheckPIN(req("1.2.3.4"), "112233"); err != nil {
		t.Fatalf("CheckPIN: %v", err)
	}
}

func TestCheckPINRejectsWrongCode(t *testing.T) {
	a := newTestAuth(t, "112233")
	if err := a.CheckPIN(req("1.2.3.4"), "000000"); err == nil {
		t.Fatalf("expected the wrong code to be rejected")
	}
}

func TestCheckPINWithNoPINConfigured(t *testing.T) {
	a := newTestAuth(t, "")
	if a.Configured() {
		t.Fatalf("Configured() = true with an empty PIN")
	}
	if err := a.CheckPIN(req("1.2.3.4"), "112233"); err != ErrNoPIN {
		t.Fatalf("CheckPIN err = %v, want ErrNoPIN", err)
	}
}

func TestCheckPINRateLimitsRepeatedFailures(t *testing.T) {
	a := newTestAuth(t, "112233")
	ip := "9.9.9.9"
	for i := 0; i < 8; i++ {
		a.CheckPIN(req(ip), "000000")
	}
	err := a.CheckPIN(req(ip), "112233") // even the correct code, now rate-limited
	if err == nil || !strings.Contains(err.Error(), "too many attempts") {
		t.Fatalf("expected a rate-limit error after 8 failures, got %v", err)
	}
}

func TestCheckPINResetsLimiterOnSuccess(t *testing.T) {
	a := newTestAuth(t, "112233")
	ip := "5.5.5.5"
	for i := 0; i < 5; i++ {
		a.CheckPIN(req(ip), "000000")
	}
	if err := a.CheckPIN(req(ip), "112233"); err != nil {
		t.Fatalf("correct code before the limit should still work: %v", err)
	}
	if err := a.CheckPIN(req(ip), "112233"); err != nil {
		t.Fatalf("after a success, the counter should have reset: %v", err)
	}
}

func TestSetPINTakesEffectLive(t *testing.T) {
	a := newTestAuth(t, "112233")
	a.SetPIN("998877")
	if err := a.CheckPIN(req("1.2.3.4"), "112233"); err == nil {
		t.Fatalf("the old PIN should no longer work")
	}
	if err := a.CheckPIN(req("1.2.3.5"), "998877"); err != nil {
		t.Fatalf("the new PIN should work immediately: %v", err)
	}
}

// ── PIN hashing ───────────────────────────────────────────────────────────

// The whole point: a PIN handed to New must never sit in memory as the raw
// digits a person typed. Checked here via HashPIN's own decoder rather than
// reflection into Auth's unexported field, so this test breaks the same way
// a real caller relying on the format would.
func TestNewStoresTheHashNotTheRawPIN(t *testing.T) {
	a := newTestAuth(t, "112233")
	if a.pin == "112233" {
		t.Fatalf("New stored the raw PIN instead of hashing it")
	}
	if !looksHashed(a.pin) {
		t.Fatalf("New's stored value doesn't look like an argon2id hash: %q", a.pin)
	}
	if !verifyPIN("112233", a.pin) {
		t.Fatalf("the stored hash doesn't actually verify the PIN it was given")
	}
}

func TestSetPINAlsoHashesRatherThanStoringRaw(t *testing.T) {
	a := newTestAuth(t, "112233")
	a.SetPIN("998877")
	if a.pin == "998877" {
		t.Fatalf("SetPIN stored the raw PIN instead of hashing it")
	}
	if !looksHashed(a.pin) {
		t.Fatalf("SetPIN's stored value doesn't look like an argon2id hash: %q", a.pin)
	}
}

// New and SetPIN both have to accept a value that's already hashed —
// exactly what happens on every restart once repairs.pin holds a hash
// instead of raw digits (see changeRepairsPIN, which hashes before ever
// writing the file) — without hashing it a second time, which would just
// make the real PIN permanently unmatchable.
func TestNewAcceptsAnAlreadyHashedPINWithoutDoubleHashing(t *testing.T) {
	hash, err := HashPIN("445566")
	if err != nil {
		t.Fatalf("HashPIN: %v", err)
	}
	a := newTestAuth(t, hash)
	if a.pin != hash {
		t.Fatalf("New re-hashed an already-hashed value instead of using it as-is:\n got  %q\n want %q", a.pin, hash)
	}
	if err := a.CheckPIN(req("1.2.3.4"), "445566"); err != nil {
		t.Fatalf("CheckPIN against a PIN loaded pre-hashed: %v", err)
	}
}

func TestSetPINAcceptsAnAlreadyHashedPINWithoutDoubleHashing(t *testing.T) {
	a := newTestAuth(t, "112233")
	hash, err := HashPIN("778899")
	if err != nil {
		t.Fatalf("HashPIN: %v", err)
	}
	a.SetPIN(hash)
	if a.pin != hash {
		t.Fatalf("SetPIN re-hashed an already-hashed value instead of using it as-is")
	}
	if err := a.CheckPIN(req("1.2.3.4"), "778899"); err != nil {
		t.Fatalf("CheckPIN against a PIN set pre-hashed: %v", err)
	}
}

// A corrupt or foreign-looking hash (truncated, wrong field count, bad
// base64) must fail closed like any wrong code, never panic — this is what
// stands between a malformed repairs.pin and a crash loop.
func TestVerifyPINFailsClosedOnGarbageHash(t *testing.T) {
	for _, bad := range []string{
		"", "not-a-hash-at-all", "argon2id$only$three$parts",
		"argon2id$3$65536$4$not-base64!!!$alsonotbase64!!!",
		pinHashPrefix + "3$65536$4$" + "AAAA" + "$" + "BBBB",
	} {
		if verifyPIN("112233", bad) {
			t.Fatalf("verifyPIN accepted a garbage hash: %q", bad)
		}
	}
}

func TestIssueAndValidateDeviceRoundTrip(t *testing.T) {
	a := newTestAuth(t, "112233")
	w := httptest.NewRecorder()
	id := a.IssueDevice(w)
	if id == "" {
		t.Fatalf("IssueDevice returned an empty id")
	}

	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	got, ok := a.ValidateDevice(r)
	if !ok || got != id {
		t.Fatalf("ValidateDevice = %q, %v; want %q, true", got, ok, id)
	}
}

func TestValidateDeviceRejectsNoCookie(t *testing.T) {
	a := newTestAuth(t, "112233")
	r := httptest.NewRequest("GET", "/", nil)
	if _, ok := a.ValidateDevice(r); ok {
		t.Fatalf("expected no cookie to fail validation")
	}
}

func splitCookie(v string) []string { return strings.SplitN(v, ".", 3) }

func TestValidateDeviceRejectsTamperedID(t *testing.T) {
	a := newTestAuth(t, "112233")
	w := httptest.NewRecorder()
	a.IssueDevice(w)
	var raw string
	for _, c := range w.Result().Cookies() {
		if c.Name == "goldstar_repairs_device" {
			raw = c.Value
		}
	}
	parts := splitCookie(raw)
	tampered := "ff" + parts[0][2:] + "." + parts[1] + "." + parts[2]

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "goldstar_repairs_device", Value: tampered})
	if _, ok := a.ValidateDevice(r); ok {
		t.Fatalf("a tampered device id should not validate")
	}
}

func TestValidateDeviceRejectsWrongSecret(t *testing.T) {
	a1 := newTestAuth(t, "112233")
	a2 := newTestAuth(t, "112233") // different signing key
	w := httptest.NewRecorder()
	a1.IssueDevice(w)

	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	if _, ok := a2.ValidateDevice(r); ok {
		t.Fatalf("a cookie signed by a different secret should not validate")
	}
}

func TestValidateDeviceRejectsExpired(t *testing.T) {
	a := newTestAuth(t, "112233")
	expired := a.sign("dev-1", time.Now().Add(-time.Hour))
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "goldstar_repairs_device", Value: expired})
	if _, ok := a.ValidateDevice(r); ok {
		t.Fatalf("an expired device cookie should not validate")
	}
}

func TestClearDeviceExpiresTheCookie(t *testing.T) {
	a := newTestAuth(t, "112233")
	w := httptest.NewRecorder()
	a.ClearDevice(w)
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "goldstar_repairs_device" {
			found = true
			if c.MaxAge >= 0 {
				t.Fatalf("ClearDevice's cookie MaxAge = %d, want negative (expire now)", c.MaxAge)
			}
		}
	}
	if !found {
		t.Fatalf("ClearDevice did not set the device cookie at all")
	}
}

func TestCSRFOKRequiresMatchingHeaderAndCookie(t *testing.T) {
	a := newTestAuth(t, "112233")
	w := httptest.NewRecorder()
	a.IssueDevice(w)
	var csrf string
	for _, c := range w.Result().Cookies() {
		if c.Name == "goldstar_repairs_csrf" {
			csrf = c.Value
		}
	}
	if csrf == "" {
		t.Fatalf("IssueDevice did not set a CSRF cookie")
	}

	ok := httptest.NewRequest("POST", "/", nil)
	ok.AddCookie(&http.Cookie{Name: "goldstar_repairs_csrf", Value: csrf})
	ok.Header.Set("X-CSRF-Token", csrf)
	if !a.CSRFOK(ok) {
		t.Fatalf("matching cookie and header should pass CSRFOK")
	}

	bad := httptest.NewRequest("POST", "/", nil)
	bad.AddCookie(&http.Cookie{Name: "goldstar_repairs_csrf", Value: csrf})
	bad.Header.Set("X-CSRF-Token", "wrong")
	if a.CSRFOK(bad) {
		t.Fatalf("a mismatched header should fail CSRFOK")
	}

	none := httptest.NewRequest("POST", "/", nil)
	if a.CSRFOK(none) {
		t.Fatalf("no cookie at all should fail CSRFOK")
	}
}
