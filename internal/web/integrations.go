package web

import (
	"fmt"
	"net/http"
	"strings"

	"goldstar/internal/store"
	"goldstar/internal/stripe"
	"goldstar/internal/twilio"
)

// twilioCreds reads what the Admin page saved. Errors reading a setting are
// folded into "not configured", because the caller's only sensible response
// to either is the same: say it is not set up yet.
func (s *Server) twilioCreds() twilio.Creds {
	sid, _ := s.db.Setting(store.SetTwilioSID)
	tok, _ := s.db.Setting(store.SetTwilioToken)
	from, _ := s.db.Setting(store.SetTwilioFrom)
	return twilio.Creds{AccountSID: sid, AuthToken: tok, From: from}
}

// ── settings, entered on the Admin page ───────────────────────────────────

func (s *Server) integrationSettings(r *http.Request) (any, error) {
	return s.db.SettingsView()
}

// saveIntegrationSettings takes only the keys it recognises, and treats a
// blank secret as "leave what is already there" rather than "clear it" —
// the form never shows a saved secret back, so an empty box means the
// person did not retype it, not that they want it gone. Clearing is done
// with the explicit checkbox instead.
func (s *Server) saveIntegrationSettings(r *http.Request) (any, error) {
	var body struct {
		TwilioSID         *string `json:"twilio_account_sid"`
		TwilioToken       *string `json:"twilio_auth_token"`
		TwilioFrom        *string `json:"twilio_from_number"`
		StripeKey         *string `json:"stripe_secret_key"`
		StripePublishable *string `json:"stripe_publishable_key"`
		StripeReturnURL   *string `json:"stripe_return_url"`
		ClearTwilioToken  bool    `json:"clear_twilio_auth_token"`
		ClearStripeKey    bool    `json:"clear_stripe_secret_key"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}

	set := func(key string, v *string) error {
		if v == nil {
			return nil
		}
		// A secret left blank is not a request to delete it.
		if store.IsSecretSetting(key) && strings.TrimSpace(*v) == "" {
			return nil
		}
		return s.db.SetSetting(key, *v)
	}

	for _, p := range []struct {
		key string
		val *string
	}{
		{store.SetTwilioSID, body.TwilioSID},
		{store.SetTwilioToken, body.TwilioToken},
		{store.SetTwilioFrom, body.TwilioFrom},
		{store.SetStripeKey, body.StripeKey},
		{store.SetStripePublishable, body.StripePublishable},
		{store.SetStripeReturnURL, body.StripeReturnURL},
	} {
		if err := set(p.key, p.val); err != nil {
			return nil, err
		}
	}
	if body.ClearTwilioToken {
		if err := s.db.SetSetting(store.SetTwilioToken, ""); err != nil {
			return nil, err
		}
	}
	if body.ClearStripeKey {
		if err := s.db.SetSetting(store.SetStripeKey, ""); err != nil {
			return nil, err
		}
	}
	return s.db.SettingsView()
}

// testTwilio sends a real message to a number the admin types, which is the
// only way to find out whether a set of credentials actually works —
// Twilio's own API accepts a wrong from-number happily until you send.
func (s *Server) testTwilio(r *http.Request) (any, error) {
	var body struct {
		To string `json:"to"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	sid, err := twilio.New().Send(r.Context(), s.twilioCreds(), body.To,
		"Goldstar test message — texting is set up correctly.")
	if err != nil {
		return nil, fail(http.StatusBadGateway, "%v", err)
	}
	return map[string]any{"ok": true, "sid": sid}, nil
}

// ── rentals: texting the customer ─────────────────────────────────────────

// textCarReady is the message this was asked for: their own car is repaired
// and can be collected, sent to whoever is holding one of our loan cars.
func (s *Server) textCarReady(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	a, err := s.db.RentalAgreement(id)
	if err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("Hi %s, your car is ready to collect from Goldstar.", firstName(a.CustomerName))
	if a.CourtesyForReg != "" {
		msg = fmt.Sprintf("Hi %s, your %s is repaired and ready to collect from Goldstar. "+
			"Please bring the loan car (%s) back when you come.",
			firstName(a.CustomerName), a.CourtesyForReg, a.Registration)
	}

	if _, err := twilio.New().Send(r.Context(), s.twilioCreds(), a.Phone, msg); err != nil {
		return nil, fail(http.StatusBadGateway, "%v", err)
	}
	if err := s.db.MarkReadyTexted(id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "sent_to": a.Phone, "message": msg}, nil
}

// firstName keeps a text personal without it reading like a form letter.
// A single-word name is used whole rather than truncated to nothing.
func firstName(full string) string {
	if i := strings.IndexByte(strings.TrimSpace(full), ' '); i > 0 {
		return strings.TrimSpace(full)[:i]
	}
	return strings.TrimSpace(full)
}

// ── rentals: taking payment ───────────────────────────────────────────────

// startRentalPayment opens a Stripe checkout page for a hire, or hands back
// the one already opened for it — pressing the button twice should give the
// customer the same link, not leave a second session hanging.
func (s *Server) startRentalPayment(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	a, err := s.db.RentalAgreement(id)
	if err != nil {
		return nil, err
	}
	if a.Paid {
		return nil, fail(http.StatusConflict, "this hire is already paid")
	}
	if a.PaymentURL != "" {
		return map[string]any{"url": a.PaymentURL, "reused": true}, nil
	}

	key, _ := s.db.Setting(store.SetStripeKey)
	returnURL, _ := s.db.Setting(store.SetStripeReturnURL)
	desc := fmt.Sprintf("%s hire — %s, %d day(s)", a.Registration, a.CustomerName, a.Days)

	// Pence, not pounds: Stripe deals in the smallest unit, and rounding
	// here rather than at the boundary is how a penny goes missing.
	pence := int64(a.Total*100 + 0.5)
	sess, err := stripe.New().CreateCheckout(r.Context(), key, desc, pence, returnURL,
		map[string]string{"agreement_id": fmt.Sprint(a.ID), "registration": a.Registration})
	if err != nil {
		return nil, fail(http.StatusBadGateway, "%v", err)
	}
	if err := s.db.StartRentalPayment(id, sess.ID, sess.URL); err != nil {
		return nil, err
	}
	return map[string]any{"url": sess.URL, "reused": false}, nil
}

// checkRentalPayment asks Stripe whether the money arrived. Polling on
// demand rather than running a webhook: this system has no public endpoint
// of its own, and the desk is looking at the hire anyway.
func (s *Server) checkRentalPayment(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	a, err := s.db.RentalAgreement(id)
	if err != nil {
		return nil, err
	}
	if a.Paid {
		return map[string]any{"paid": true}, nil
	}
	key, _ := s.db.Setting(store.SetStripeKey)
	sess, err := stripe.New().Checkout(r.Context(), key, a.PaymentSession)
	if err != nil {
		return nil, fail(http.StatusBadGateway, "%v", err)
	}
	if sess.Paid() {
		if err := s.db.MarkRentalPaid(id); err != nil {
			return nil, err
		}
	}
	return map[string]any{"paid": sess.Paid(), "status": sess.Payment}, nil
}

func (s *Server) rentalStats(r *http.Request) (any, error)   { return s.db.RentalStats() }
func (s *Server) courtesyLoans(r *http.Request) (any, error) { return s.db.CourtesyLoans() }
