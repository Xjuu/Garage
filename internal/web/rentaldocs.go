package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goldstar/internal/store"
	"goldstar/internal/twilio"
)

// maxDocBytes caps one upload. A licence scan or a damage photo from a
// phone is a few megabytes; anything past this is a mistake, and saying so
// beats filling a disk quietly.
const maxDocBytes = 15 << 20

// allowedDocTypes is what a hire desk actually scans. An allowlist rather
// than a blocklist: these files are served back to a browser later, and
// "everything except the ones I thought of" is not a safe way to decide
// what may be handed to it.
var allowedDocTypes = map[string]string{
	".pdf":  "application/pdf",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
	".heic": "image/heic",
}

// uploadRentalDocument takes one file and files it against a customer or a
// hire. The stored name is generated, never taken from the upload: a
// filename is attacker-controlled text, and the original is kept in the
// database for display instead.
func (s *Server) uploadRentalDocument(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeErr := func(code int, format string, args ...any) {
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"error":%q}`, fmt.Sprintf(format, args...))
	}

	if err := r.ParseMultipartForm(maxDocBytes); err != nil {
		writeErr(http.StatusBadRequest, "that upload could not be read: %v", err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(http.StatusBadRequest, "no file was attached")
		return
	}
	defer file.Close()

	if header.Size > maxDocBytes {
		writeErr(http.StatusRequestEntityTooLarge,
			"%s is too big — %d MB is the limit", header.Filename, maxDocBytes>>20)
		return
	}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	contentType, ok := allowedDocTypes[ext]
	if !ok {
		writeErr(http.StatusBadRequest,
			"%s is not a kind of file this takes — PDFs and photos only", ext)
		return
	}

	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	agreementID, _ := strconv.ParseInt(r.FormValue("agreement_id"), 10, 64)
	if customerID == 0 && agreementID == 0 {
		writeErr(http.StatusBadRequest, "a document has to belong to a customer or a hire")
		return
	}

	// Foldered by month so one directory never accumulates every document
	// this business has ever taken.
	dir := filepath.Join(s.cfg.RentalDocsDir(), time.Now().Format("2006/01"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(http.StatusInternalServerError, "could not store that file")
		return
	}
	name := randomHex(12) + ext
	path := filepath.Join(dir, name)

	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		writeErr(http.StatusInternalServerError, "could not store that file")
		return
	}
	written, copyErr := io.Copy(dst, io.LimitReader(file, maxDocBytes))
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(path)
		writeErr(http.StatusInternalServerError, "could not store that file")
		return
	}

	by := ""
	if u, ok := s.auth.CurrentUser(r); ok {
		by = u.Username
	}
	doc := store.RentalDocument{
		Kind: r.FormValue("kind"), Filename: filepath.Base(header.Filename),
		StoredPath: path, Mime: contentType, Bytes: written, UploadedBy: by,
	}
	if customerID > 0 {
		doc.CustomerID = &customerID
	}
	if agreementID > 0 {
		doc.AgreementID = &agreementID
	}

	id, err := s.db.AddRentalDocument(doc)
	if err != nil {
		// The row is what makes the file reachable; without it the file is
		// litter, so it goes too rather than being left behind.
		os.Remove(path)
		writeErr(http.StatusBadRequest, "%v", err)
		return
	}
	fmt.Fprintf(w, `{"id":%d,"filename":%q,"bytes":%d}`, id, doc.Filename, written)
}

// serveRentalDocument streams a stored document back. The path comes from
// the database and is checked against the documents directory before
// anything is opened — a tampered row must not be able to read arbitrary
// files, the same rule invoiceFile follows.
func (s *Server) serveRentalDocument(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	doc, err := s.db.RentalDocument(id)
	if err != nil {
		http.Error(w, "no such document", http.StatusNotFound)
		return
	}

	root, rerr := filepath.Abs(s.cfg.RentalDocsDir())
	abs, aerr := filepath.Abs(doc.StoredPath)
	if rerr != nil || aerr != nil || !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := os.Stat(abs); err != nil {
		http.Error(w, "the file is missing from disk", http.StatusNotFound)
		return
	}

	if doc.Mime != "" {
		w.Header().Set("Content-Type", doc.Mime)
	} else if ct := mime.TypeByExtension(filepath.Ext(abs)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// inline, so a licence scan opens rather than downloading — but the
	// filename is quoted, since it came from an upload.
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(doc.Filename))
	http.ServeFile(w, r, abs)
}

func (s *Server) rentalDocuments(r *http.Request) (any, error) {
	v := r.URL.Query()
	customerID, _ := strconv.ParseInt(v.Get("customer"), 10, 64)
	agreementID, _ := strconv.ParseInt(v.Get("agreement"), 10, 64)
	return s.db.RentalDocuments(customerID, agreementID)
}

func (s *Server) deleteRentalDocument(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	path, err := s.db.DeleteRentalDocument(id)
	if err != nil {
		return nil, err
	}
	// Only inside our own folder, whatever the row claimed.
	root, rerr := filepath.Abs(s.cfg.RentalDocsDir())
	abs, aerr := filepath.Abs(path)
	if rerr == nil && aerr == nil && strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		os.Remove(abs)
	}
	return okResponse(), nil
}

// randomHex names a stored file. crypto/rand rather than a counter: the
// name is the only thing between one customer's licence scan and a guessed
// URL, even though the endpoint is behind a session too.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ── messages ──────────────────────────────────────────────────────────────

// sendRentalMessage is the free-text one: whatever the desk wants to say,
// to whoever. Logged either way — a message Twilio refused is the more
// useful record of the two, and "nobody told me" is a conversation this
// gives an answer to.
func (s *Server) sendRentalMessage(r *http.Request) (any, error) {
	var body struct {
		CustomerID  int64  `json:"customer_id"`
		AgreementID int64  `json:"agreement_id"`
		Body        string `json:"body"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if strings.TrimSpace(body.Body) == "" {
		return nil, fail(http.StatusBadRequest, "nothing to send")
	}
	cust, err := s.db.RentalCustomer(body.CustomerID)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "no such customer")
	}

	by := ""
	if u, ok := s.auth.CurrentUser(r); ok {
		by = u.Username
	}
	msg := store.RentalMessage{
		CustomerID: cust.ID, Phone: cust.Phone, Body: strings.TrimSpace(body.Body), SentBy: by,
	}
	if body.AgreementID > 0 {
		msg.AgreementID = &body.AgreementID
	}

	sid, sendErr := twilio.New().Send(r.Context(), s.twilioCreds(), cust.Phone, msg.Body)
	if sendErr != nil {
		msg.Status, msg.Error = "failed", sendErr.Error()
		_, _ = s.db.LogRentalMessage(msg)
		return nil, fail(http.StatusBadGateway, "%v", sendErr)
	}
	msg.Status, msg.ProviderSID = "sent", sid
	if _, err := s.db.LogRentalMessage(msg); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "sent_to": cust.Phone}, nil
}

func (s *Server) rentalMessages(r *http.Request) (any, error) {
	customerID, _ := strconv.ParseInt(r.URL.Query().Get("customer"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return s.db.RentalMessages(customerID, limit)
}

// ── charges ───────────────────────────────────────────────────────────────

func (s *Server) setRentalCharges(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var c store.RentalCharges
	if err := decode(r, &c); err != nil {
		return nil, err
	}
	if err := s.db.SetRentalCharges(id, c); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return s.db.RentalAgreement(id)
}

// applyLateFee turns what an overdue car is running up into a real charge.
// With no amount given it charges exactly what the agreed rate has come to,
// which is the answer nine times out of ten; passing 0 explicitly waives it.
func (s *Server) applyLateFee(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Amount *float64 `json:"amount"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	a, err := s.db.RentalAgreement(id)
	if err != nil {
		return nil, err
	}
	amount := a.LateFeeDue
	if body.Amount != nil {
		amount = *body.Amount
	}
	if err := s.db.ApplyLateFee(id, amount); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return s.db.RentalAgreement(id)
}

func (s *Server) setDepositReturned(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Returned bool `json:"returned"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if err := s.db.SetDepositReturned(id, body.Returned); err != nil {
		return nil, err
	}
	return s.db.RentalAgreement(id)
}

func (s *Server) rentalAgreement(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.db.RentalAgreement(id)
}
