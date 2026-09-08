package store

import "strings"

// Keys for the settings table. Named here rather than typed as strings at
// every call site, so a typo is a compile error instead of a setting that
// silently reads back empty forever.
const (
	SetTwilioSID   = "twilio_account_sid"
	SetTwilioToken = "twilio_auth_token"
	SetTwilioFrom  = "twilio_from_number"
	SetStripeKey   = "stripe_secret_key"
	// The public half of a Stripe account, and where a customer is sent
	// back to once they have paid. Neither is a secret.
	SetStripePublishable = "stripe_publishable_key"
	SetStripeReturnURL   = "stripe_return_url"

	// The house price list for hires. Snapshotted onto an agreement when a
	// car goes out, so changing these never rewrites an existing hire.
	SetInsurancePerDay = "rental_insurance_per_day"
	SetLateFeePerDay   = "rental_late_fee_per_day"
	SetDepositDefault  = "rental_deposit_default"
)

// secretSettings are the ones never sent back to a browser — only whether
// they are set. The others are safe to show and useful to check.
var secretSettings = map[string]bool{
	SetTwilioToken: true,
	SetStripeKey:   true,
}

func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil && err.Error() == "sql: no rows in result set" {
		return "", nil // unset is not an error, it is the normal starting state
	}
	return v, err
}

// SetSetting writes a value, or clears it when given an empty string. An
// empty value is stored rather than deleted so "this was deliberately
// cleared" and "never touched" look the same to every reader — which is
// what the callers actually want.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value, updated_at)
		VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, strings.TrimSpace(value))
	return err
}

// SettingsView is what the Admin page is allowed to see: the public values
// in full, and the secrets only as whether they are set at all.
type SettingsView struct {
	TwilioSID         string `json:"twilio_account_sid"`
	TwilioFrom        string `json:"twilio_from_number"`
	TwilioTokenSet    bool   `json:"twilio_auth_token_set"`
	TwilioReady       bool   `json:"twilio_ready"`
	StripePublishable string `json:"stripe_publishable_key"`
	StripeReturnURL   string `json:"stripe_return_url"`
	StripeKeySet      bool   `json:"stripe_secret_key_set"`
	StripeReady       bool   `json:"stripe_ready"`

	InsurancePerDay string `json:"rental_insurance_per_day"`
	LateFeePerDay   string `json:"rental_late_fee_per_day"`
	DepositDefault  string `json:"rental_deposit_default"`
}

func (s *Store) SettingsView() (*SettingsView, error) {
	all := map[string]string{}
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		all[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &SettingsView{
		TwilioSID:         all[SetTwilioSID],
		TwilioFrom:        all[SetTwilioFrom],
		TwilioTokenSet:    all[SetTwilioToken] != "",
		StripePublishable: all[SetStripePublishable],
		StripeReturnURL:   all[SetStripeReturnURL],
		StripeKeySet:      all[SetStripeKey] != "",
		InsurancePerDay:   all[SetInsurancePerDay],
		LateFeePerDay:     all[SetLateFeePerDay],
		DepositDefault:    all[SetDepositDefault],
	}
	// "Ready" is the only thing the UI should act on: a half-filled set of
	// credentials cannot send anything, and saying so up front beats a
	// failure at the moment someone presses Send.
	out.TwilioReady = all[SetTwilioSID] != "" && all[SetTwilioToken] != "" && all[SetTwilioFrom] != ""
	out.StripeReady = all[SetStripeKey] != ""
	return out, nil
}

// IsSecretSetting reports whether a key must never be echoed back.
func IsSecretSetting(key string) bool { return secretSettings[key] }
