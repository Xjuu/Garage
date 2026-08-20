package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// totpDigits and totpStepSeconds match what every authenticator app assumes
// unless an otpauth:// URI says otherwise — RFC 6238's own defaults: six
// digits, HMAC-SHA1, a new code every 30 seconds. Authy, Google Authenticator
// and 1Password all read these without needing them spelled out, but the URI
// states them anyway so nothing is left to an app's own guess.
const (
	totpDigits      = 6
	totpStepSeconds = 30
	totpSecretBytes = 20 // 160 bits — RFC 4226's recommended HMAC-SHA1 key size
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// generateTOTPSecret returns a fresh random secret, base32-encoded without
// padding — the form every authenticator app expects to scan or type in.
func generateTOTPSecret() (string, error) {
	b := make([]byte, totpSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return totpEncoding.EncodeToString(b), nil
}

// TOTPURL builds the otpauth:// URI the setup QR code encodes. issuer and
// account both appear in the authenticator app's list, so a phone carrying
// several accounts can tell them apart at a glance.
func TOTPURL(issuer, account, secretBase32 string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{
		"secret":    {secretBase32},
		"issuer":    {issuer},
		"algorithm": {"SHA1"},
		"digits":    {fmt.Sprintf("%d", totpDigits)},
		"period":    {fmt.Sprintf("%d", totpStepSeconds)},
	}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// FormatSecretForDisplay groups the secret into blocks of four, the way
// authenticator apps and setup guides conventionally show a manual-entry
// key — easier to type back correctly than one unbroken run of characters,
// for whoever can't scan the QR code.
func FormatSecretForDisplay(secret string) string {
	var b strings.Builder
	for i, r := range secret {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func totpStep(t time.Time) int64 { return t.Unix() / totpStepSeconds }

// hotp is RFC 4226's HMAC-based one-time password — the primitive TOTP wraps
// around a counter derived from the time instead of one that just increments.
func hotp(secretBase32 string, counter int64) (string, error) {
	key, err := totpEncoding.DecodeString(strings.ToUpper(strings.TrimSpace(secretBase32)))
	if err != nil {
		return "", fmt.Errorf("bad TOTP secret: %w", err)
	}
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter))

	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	sum := mac.Sum(nil)

	// Dynamic truncation, straight out of RFC 4226 §5.3: the low nibble of
	// the last byte picks a 4-byte window elsewhere in the HMAC, and clearing
	// the top bit keeps the result a positive 31-bit number before reducing
	// it to six digits.
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset]&0x7f) << 24) | (uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) | uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, code%mod), nil
}

// checkTOTP reports whether code matches secret at the current 30-second step
// or the one immediately before or after it — one step of slack either way
// for a phone clock that has drifted or a code typed right as it rolls over.
// It also returns which step matched, so the caller can refuse to accept the
// same code twice: without that, a code good for up to a minute could be
// replayed by anyone who saw it once.
func checkTOTP(secretBase32, code string, now time.Time) (ok bool, step int64) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false, 0
	}
	current := totpStep(now)
	for _, skew := range []int64{0, -1, 1} {
		s := current + skew
		want, err := hotp(secretBase32, s)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(code), []byte(want)) == 1 {
			return true, s
		}
	}
	return false, 0
}
