package store

import "testing"

func TestSettingsRoundTripAndUnsetIsNotAnError(t *testing.T) {
	db := open(t)

	// Never touched reads as empty rather than failing — the normal state
	// of a fresh install, not an error anyone should have to handle.
	v, err := db.Setting(SetTwilioSID)
	if err != nil || v != "" {
		t.Fatalf("unset Setting = (%q, %v), want empty and no error", v, err)
	}

	if err := db.SetSetting(SetTwilioSID, "  AC123  "); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Setting(SetTwilioSID); v != "AC123" {
		t.Errorf("Setting = %q, want it trimmed to AC123", v)
	}

	// Writing again replaces rather than duplicating the key.
	if err := db.SetSetting(SetTwilioSID, "AC456"); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Setting(SetTwilioSID); v != "AC456" {
		t.Errorf("Setting = %q, want AC456", v)
	}
}

// The view is what a browser is allowed to see: public values in full,
// secrets only as whether they exist.
func TestSettingsViewNeverExposesASecret(t *testing.T) {
	db := open(t)
	for k, v := range map[string]string{
		SetTwilioSID: "AC123", SetTwilioToken: "super-secret", SetTwilioFrom: "+441",
		SetStripeKey: "sk_live_secret", SetStripePublishable: "pk_live_public",
	} {
		if err := db.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}

	view, err := db.SettingsView()
	if err != nil {
		t.Fatalf("SettingsView: %v", err)
	}
	if view.TwilioSID != "AC123" || view.TwilioFrom != "+441" || view.StripePublishable != "pk_live_public" {
		t.Errorf("public values should come back in full: %+v", view)
	}
	if !view.TwilioTokenSet || !view.StripeKeySet {
		t.Error("a saved secret should report as set")
	}
	// Both credentials complete, so both report ready.
	if !view.TwilioReady || !view.StripeReady {
		t.Errorf("both should be ready: %+v", view)
	}

	// Nothing in the view may contain a secret's value, whatever field it
	// might have been added to later.
	for _, s := range []string{view.TwilioSID, view.TwilioFrom, view.StripePublishable, view.StripeReturnURL} {
		if s == "super-secret" || s == "sk_live_secret" {
			t.Fatalf("a secret leaked into the view: %q", s)
		}
	}
	if !IsSecretSetting(SetTwilioToken) || !IsSecretSetting(SetStripeKey) {
		t.Error("the two secrets should be marked as such")
	}
	if IsSecretSetting(SetTwilioSID) {
		t.Error("the account SID is not a secret and should not be treated as one")
	}
}

// Half-configured is not ready: saying so up front beats a failure at the
// moment someone presses Send.
func TestSettingsViewReadyNeedsEveryPart(t *testing.T) {
	db := open(t)
	if err := db.SetSetting(SetTwilioSID, "AC123"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(SetTwilioToken, "tok"); err != nil {
		t.Fatal(err)
	}
	view, _ := db.SettingsView()
	if view.TwilioReady {
		t.Error("Twilio with no from-number is not ready to send")
	}
	if err := db.SetSetting(SetTwilioFrom, "+441"); err != nil {
		t.Fatal(err)
	}
	if view, _ = db.SettingsView(); !view.TwilioReady {
		t.Error("Twilio with all three parts should be ready")
	}
}
