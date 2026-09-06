package stripe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCreateCheckoutSendsPenceAndTheHiresDetails(t *testing.T) {
	var gotForm url.Values
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		r.ParseForm()
		gotForm = r.PostForm
		w.Write([]byte(`{"id":"cs_1","url":"https://pay/x","payment_status":"unpaid"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	sess, err := c.CreateCheckout(context.Background(), "sk_test", "RE21NTL hire", 22500,
		"https://rentals.example/", map[string]string{"agreement_id": "7"})
	if err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if sess.URL != "https://pay/x" || sess.ID != "cs_1" {
		t.Errorf("session = %+v", sess)
	}
	if sess.Paid() {
		t.Error("an unpaid session must not report as paid")
	}
	if gotAuth != "Bearer sk_test" {
		t.Errorf("auth = %q", gotAuth)
	}
	// £225.00 is 22500 pence — sending pounds here would charge someone £225
	// worth of pennies, or 1/100th of the bill.
	if got := gotForm.Get("line_items[0][price_data][unit_amount]"); got != "22500" {
		t.Errorf("unit_amount = %q, want 22500", got)
	}
	if got := gotForm.Get("line_items[0][price_data][currency]"); got != "gbp" {
		t.Errorf("currency = %q, want gbp", got)
	}
	// The metadata is what ties a payment back to the hire it was for.
	if got := gotForm.Get("metadata[agreement_id]"); got != "7" {
		t.Errorf("metadata[agreement_id] = %q, want 7", got)
	}
}

// A session being "complete" is not the same as the money arriving.
func TestPaidFollowsPaymentStatusNotSessionStatus(t *testing.T) {
	for _, tc := range []struct {
		payment string
		want    bool
	}{{"paid", true}, {"unpaid", false}, {"no_payment_required", false}} {
		if got := (Session{Status: "complete", Payment: tc.payment}).Paid(); got != tc.want {
			t.Errorf("payment_status %q: Paid() = %v, want %v", tc.payment, got, tc.want)
		}
	}
}

func TestCreateCheckoutRefusesWhatStripeWouldAnyway(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}

	for _, tc := range []struct{ name, key, ret string; amount int64; want string }{
		{"no key", "", "https://x/", 100, "not set up"},
		{"nothing to charge", "sk", "https://x/", 0, "nothing to charge"},
		{"no return url", "sk", "", 100, "return URL"},
	} {
		_, err := c.CreateCheckout(context.Background(), tc.key, "d", tc.amount, tc.ret, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
	if called {
		t.Error("nothing should have reached Stripe")
	}
}

func TestStripeErrorsCarryStripesOwnMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"Invalid API Key provided"}}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	_, err := c.CreateCheckout(context.Background(), "sk_bad", "d", 100, "https://x/", nil)
	if err == nil || !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("error = %v, want Stripe's own message", err)
	}
}
