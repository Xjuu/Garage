// Package stripe creates hosted checkout pages for a hire. Same reasoning
// as internal/twilio: two endpoints are needed, and Stripe's Go SDK is a
// large dependency for that.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	HTTP *http.Client
	// BaseURL exists for tests. Empty means Stripe itself.
	BaseURL string
}

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Session is the part of a Stripe Checkout Session this system keeps: where
// to send the customer, and whether they have paid yet.
type Session struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Status  string `json:"status"`
	Payment string `json:"payment_status"`
}

// Paid reports whether the money has actually arrived, which is
// payment_status rather than the session being "complete" — a session can
// complete without payment succeeding.
func (s Session) Paid() bool { return s.Payment == "paid" }

// CreateCheckout opens a hosted payment page for one hire.
//
// amountPence, not pounds: Stripe deals in the currency's smallest unit,
// and passing a float here is how rounding errors become money.
func (c *Client) CreateCheckout(ctx context.Context, secretKey, description string,
	amountPence int64, returnURL string, meta map[string]string) (*Session, error) {
	if strings.TrimSpace(secretKey) == "" {
		return nil, fmt.Errorf("Stripe is not set up yet — add the secret key under Admin")
	}
	if amountPence <= 0 {
		return nil, fmt.Errorf("there is nothing to charge for this hire")
	}
	if strings.TrimSpace(returnURL) == "" {
		return nil, fmt.Errorf("set the return URL under Admin — Stripe needs somewhere to send the customer back to")
	}

	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", returnURL)
	form.Set("cancel_url", returnURL)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", "gbp")
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(amountPence, 10))
	form.Set("line_items[0][price_data][product_data][name]", description)
	for k, v := range meta {
		form.Set("metadata["+k+"]", v)
	}

	var out Session
	if err := c.do(ctx, secretKey, http.MethodPost, "/v1/checkout/sessions", form, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Checkout re-reads a session, which is how a hire finds out the customer
// has paid without this system needing a public webhook endpoint. A webhook
// would be lower-latency; asking when someone looks is enough for a desk
// that is going to look anyway.
func (c *Client) Checkout(ctx context.Context, secretKey, sessionID string) (*Session, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("no payment has been started for this hire")
	}
	var out Session
	err := c.do(ctx, secretKey, http.MethodGet, "/v1/checkout/sessions/"+url.PathEscape(sessionID), nil, &out)
	return &out, err
}

func (c *Client) do(ctx context.Context, secretKey, method, path string,
	form url.Values, out any) error {
	base := c.BaseURL
	if base == "" {
		base = "https://api.stripe.com"
	}
	var bodyReader *strings.Reader
	if form != nil {
		bodyReader = strings.NewReader(form.Encode())
	} else {
		bodyReader = strings.NewReader("")
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secretKey)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Stripe: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode >= 400 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Error.Message != "" {
			return fmt.Errorf("Stripe refused it: %s", e.Error.Message)
		}
		return fmt.Errorf("Stripe refused it (%s)", res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}
