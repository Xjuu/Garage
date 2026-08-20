package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 4226 Appendix D's test vectors: HOTP with the 20-byte ASCII key
// "12345678901234567890" at counters 0..9. TOTP (RFC 6238) is the same HOTP
// algorithm keyed on a time-derived counter instead of an incrementing one,
// so if hotp() matches these it is implemented correctly.
func TestHOTPMatchesRFC4226Vectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	want := []string{
		"755224", "287082", "359152", "969429", "338314",
		"254676", "287922", "162583", "399871", "520489",
	}
	for counter, w := range want {
		got, err := hotp(secret, int64(counter))
		if err != nil {
			t.Fatalf("hotp(%d): %v", counter, err)
		}
		if got != w {
			t.Errorf("hotp(%d) = %q, want %q", counter, got, w)
		}
	}
}

func TestCheckTOTPAcceptsCurrentAndAdjacentSteps(t *testing.T) {
	secret, err := generateTOTPSecret()
	if err != nil {
		t.Fatalf("generateTOTPSecret: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)

	code, err := hotp(secret, totpStep(now))
	if err != nil {
		t.Fatalf("hotp: %v", err)
	}

	// Checked slightly before and after the step it was generated for, the
	// way a phone clock a few seconds off, or a code typed right as it
	// changes, would actually present it.
	for _, offset := range []time.Duration{0, 20 * time.Second, -20 * time.Second} {
		ok, _ := checkTOTP(secret, code, now.Add(offset))
		if !ok {
			t.Errorf("checkTOTP at offset %s: want accepted", offset)
		}
	}

	// Two steps away (60s+) is outside the one-step slack and must be
	// rejected, or the "6-digit code" becomes effectively guessable over a
	// much wider window than intended.
	ok, _ := checkTOTP(secret, code, now.Add(90*time.Second))
	if ok {
		t.Errorf("checkTOTP 90s later: want rejected, code is stale")
	}
}

func TestCheckTOTPRejectsWrongCode(t *testing.T) {
	secret, _ := generateTOTPSecret()
	ok, _ := checkTOTP(secret, "000000", time.Now())
	if ok {
		t.Fatalf("an arbitrary code should not validate")
	}
}

func TestCheckTOTPRejectsMalformedCode(t *testing.T) {
	secret, _ := generateTOTPSecret()
	for _, bad := range []string{"", "12345", "1234567", "12345a"} {
		if ok, _ := checkTOTP(secret, bad, time.Now()); ok {
			t.Errorf("checkTOTP(%q): want rejected", bad)
		}
	}
}

func TestTOTPURLCarriesTheStandardParameters(t *testing.T) {
	u := TOTPURL("Garage Goldstar", "faz", "ABCD1234")
	for _, want := range []string{
		"otpauth://totp/", "secret=ABCD1234", "issuer=Garage",
		"algorithm=SHA1", "digits=6", "period=30",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("TOTPURL = %q, missing %q", u, want)
		}
	}
}

func TestFormatSecretForDisplayGroupsInFours(t *testing.T) {
	got := FormatSecretForDisplay("ABCDEFGHIJKL")
	want := "ABCD EFGH IJKL"
	if got != want {
		t.Errorf("FormatSecretForDisplay = %q, want %q", got, want)
	}
}
