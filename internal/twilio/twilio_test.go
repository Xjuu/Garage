package twilio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSendPostsToTwilioWithTheRightShape(t *testing.T) {
	var gotPath, gotUser, gotPass string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		r.ParseForm()
		gotForm = r.PostForm
		w.Write([]byte(`{"sid":"SM123"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	sid, err := c.Send(context.Background(),
		Creds{AccountSID: "AC1", AuthToken: "tok", From: "+441"}, "+442", "hello")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sid != "SM123" {
		t.Errorf("sid = %q, want SM123", sid)
	}
	if gotPath != "/2010-04-01/Accounts/AC1/Messages.json" {
		t.Errorf("path = %q", gotPath)
	}
	// Twilio authenticates with the account SID and token as basic auth —
	// getting this wrong fails in a way that looks like a bad number.
	if gotUser != "AC1" || gotPass != "tok" {
		t.Errorf("basic auth = %q/%q, want AC1/tok", gotUser, gotPass)
	}
	if gotForm.Get("To") != "+442" || gotForm.Get("From") != "+441" || gotForm.Get("Body") != "hello" {
		t.Errorf("form = %v", gotForm)
	}
}

// Twilio's own message is worth surfacing: "the number is unverified on a
// trial account" is something the person at the desk can act on, where
// "422" is not.
func TestSendSurfacesTwiliosOwnRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"message":"The number +442 is unverified.","code":21608}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	_, err := c.Send(context.Background(),
		Creds{AccountSID: "AC1", AuthToken: "tok", From: "+441"}, "+442", "hello")
	if err == nil {
		t.Fatal("want an error for a refused message")
	}
	if !strings.Contains(err.Error(), "unverified") {
		t.Errorf("error should carry Twilio's own words, got %q", err)
	}
}

// Nothing is sent when it could not possibly work, and the reason says
// which half is missing — otherwise "failed to send" is the whole story.
func TestSendRefusesBeforeCallingOutWhenItCannotWork(t *testing.T) {
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	full := Creds{AccountSID: "AC1", AuthToken: "tok", From: "+441"}

	for _, tc := range []struct {
		name  string
		creds Creds
		to    string
		body  string
		want  string
	}{
		{"no credentials at all", Creds{}, "+442", "hi", "not set up"},
		{"half-configured", Creds{AccountSID: "AC1"}, "+442", "hi", "not set up"},
		{"no number for the customer", full, "  ", "hi", "no mobile number"},
		{"nothing to say", full, "+442", "  ", "nothing to send"},
	} {
		_, err := c.Send(context.Background(), tc.creds, tc.to, tc.body)
		if err == nil {
			t.Errorf("%s: want an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
	if sent {
		t.Error("nothing should have been sent to Twilio at all")
	}
}
