// Package twilio sends SMS. Deliberately hand-rolled against the REST API
// rather than pulling in Twilio's SDK: this needs exactly one endpoint, and
// the SDK would add a large dependency tree to a binary whose whole
// deployment story is "one file, no runtime dependencies".
package twilio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Creds is what the Admin page collects. Empty fields mean "not configured
// yet", which every caller has to handle — nothing here panics on a blank.
type Creds struct {
	AccountSID string
	AuthToken  string
	From       string
}

func (c Creds) Ready() bool {
	return c.AccountSID != "" && c.AuthToken != "" && c.From != ""
}

// Client is safe to construct per call; it holds no state beyond a timeout.
type Client struct {
	HTTP *http.Client
	// BaseURL exists for tests. Empty means Twilio itself.
	BaseURL string
}

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Send delivers one message and returns Twilio's message SID.
//
// Errors are returned with Twilio's own message where there is one: "the
// number is unverified on a trial account" is something the person at the
// desk can act on, where "422 Unprocessable Entity" is not.
func (c *Client) Send(ctx context.Context, creds Creds, to, body string) (string, error) {
	if !creds.Ready() {
		return "", fmt.Errorf("Twilio is not set up yet — add the account SID, token and from-number under Admin")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return "", fmt.Errorf("no mobile number on file for that customer")
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("nothing to send")
	}

	base := c.BaseURL
	if base == "" {
		base = "https://api.twilio.com"
	}
	endpoint := fmt.Sprintf("%s/2010-04-01/Accounts/%s/Messages.json", base, url.PathEscape(creds.AccountSID))

	form := url.Values{}
	form.Set("To", to)
	form.Set("From", creds.From)
	form.Set("Body", body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(creds.AccountSID, creds.AuthToken)

	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach Twilio: %w", err)
	}
	defer res.Body.Close()

	var payload struct {
		SID     string `json:"sid"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	_ = json.NewDecoder(res.Body).Decode(&payload)

	if res.StatusCode >= 400 {
		if payload.Message != "" {
			return "", fmt.Errorf("Twilio refused it: %s", payload.Message)
		}
		return "", fmt.Errorf("Twilio refused it (%s)", res.Status)
	}
	return payload.SID, nil
}
