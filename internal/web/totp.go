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
	if !s.auth.PendingOK(r) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "sign in again"})
		return
	}
	secret, err := s.auth.BeginTOTPSetup()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	otpURL := auth.TOTPURL(totpIssuer, s.auth.AccountLabel(), secret)
	png, err := qrcode.Encode(otpURL, qrcode.Medium, 256)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"secret":  auth.FormatSecretForDisplay(secret),
		"qr_png":  "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		"account": s.auth.AccountLabel(),
	})
}

// handleTOTPConfirm checks the first code typed against the pending setup
// secret and, on success, makes 2FA permanent and signs the browser in for
// real — this is the only path from "just set up 2FA" to an actual session.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.auth.PendingOK(r) {
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

	secret, err := s.auth.ConfirmTOTPSetup(r, body.Code)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if err := s.cfg.WriteTOTPSecret(secret); err != nil {
		// The in-memory secret is already live at this point — a write
		// failure here means it will not survive a restart, which is worth
		// surfacing rather than silently pretending setup fully succeeded.
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "2FA verified but could not be saved: " + err.Error(),
		})
		return
	}

	s.auth.IssueSession(w)
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleTOTPVerify is the everyday second step: this account already has 2FA
// set up, and a valid code turns the pending cookie into a real session.
func (s *Server) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.auth.PendingOK(r) {
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

	if err := s.auth.VerifyTOTPCode(r, body.Code); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	s.auth.IssueSession(w)
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
	secret, ok := s.auth.TOTPSecretForDisplay()
	if !ok {
		return nil, fail(http.StatusBadRequest, "2FA is not set up on this account yet")
	}

	otpURL := auth.TOTPURL(totpIssuer, s.auth.AccountLabel(), secret)
	png, err := qrcode.Encode(otpURL, qrcode.Medium, 256)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"secret": auth.FormatSecretForDisplay(secret),
		"qr_png": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	}, nil
}
