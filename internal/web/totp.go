package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	qrcode "github.com/skip2/go-qrcode"

	"goldstar/internal/auth"
)

// totpIssuer names the account in whatever authenticator app scans the QR
// code — the same label on every install, since the URL it is served from
// already says which one this is.
const totpIssuer = "Garage Goldstar"

// handleTOTPSetup hands out the QR code for a first-time 2FA setup. Only
// reachable with a pending cookie — the password step that precedes it — and
// only while this account has not finished setup yet.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	u, ok := s.auth.PendingUser(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "sign in again"})
		return
	}
	secret, err := s.auth.BeginTOTPSetup(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	otpURL := auth.TOTPURL(totpIssuer, auth.AccountLabel(u), secret)
	png, err := qrcode.Encode(otpURL, qrcode.Medium, 256)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"secret":  auth.FormatSecretForDisplay(secret),
		"qr_png":  "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		"account": auth.AccountLabel(u),
	})
}

// handleTOTPConfirm checks the first code typed against the pending setup
// secret and, on success, makes 2FA permanent and signs the browser in for
// real — this is the only path from "just set up 2FA" to an actual session.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, ok := s.auth.PendingUser(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "sign in again"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "bad request"})
		return
	}

	userID, err := s.auth.ConfirmTOTPSetup(r, body.Code)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// The secret is already persisted to the account's row by
	// ConfirmTOTPSetup itself — nothing left to save here.
	s.auth.IssueSession(w, userID)
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleTOTPVerify is the everyday second step: this account already has 2FA
// set up, and a valid code turns the pending cookie into a real session.
func (s *Server) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, ok := s.auth.PendingUser(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "sign in again"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "bad request"})
		return
	}

	userID, err := s.auth.VerifyTOTPCode(r, body.Code)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	s.auth.IssueSession(w, userID)
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// TEMP: lets an already signed-in session re-display its own confirmed 2FA
// secret as a QR code, so it can be scanned into a second device (a new
// phone, Authy on another platform, ...) without running `goldstar
// totp-reset` and losing the original enrollment in the process. Registered
// on the authenticated admin API — see routesAdmin — so the standard session
// check already gates it; nothing extra is needed here.
//
// Remove this handler, its route in admin.go, and the matching panel in the
// Admin page once it is no longer needed.
func (s *Server) totpReshow(r *http.Request) (any, error) {
	u, ok := s.auth.CurrentUser(r)
	if !ok {
		return nil, fail(http.StatusUnauthorized, "sign in again")
	}
	secret, ok := s.auth.TOTPSecretForDisplay(r)
	if !ok {
		return nil, fail(http.StatusBadRequest, "2FA is not set up on this account yet")
	}

	otpURL := auth.TOTPURL(totpIssuer, auth.AccountLabel(u), secret)
	png, err := qrcode.Encode(otpURL, qrcode.Medium, 256)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"secret": auth.FormatSecretForDisplay(secret),
		"qr_png": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	}, nil
}
