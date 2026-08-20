// Package pipeline wires mailbox -> Gemini -> SQLite -> Excel together.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"goldstar/internal/config"
	"goldstar/internal/export"
	"goldstar/internal/extract"
	"goldstar/internal/mailbox"
	"goldstar/internal/store"
)

// ProcessedFlag marks messages the tool has ingested, visible in any mail client.
const ProcessedFlag = "Goldstar-Processed"

// LogFunc receives progress. The CLI sends it to stderr; the dashboard sends
// it to a job log the browser polls.
type LogFunc func(format string, args ...any)

func (l LogFunc) printf(format string, args ...any) {
	if l != nil {
		l(format, args...)
	}
}

type Stats struct {
	MessagesScanned int `json:"messages_scanned"`
	MessagesNew     int `json:"messages_new"`
	Attachments     int `json:"attachments"`
	Invoices        int `json:"invoices"`
	Skipped         int `json:"skipped"`
	Failed          int `json:"failed"`
}

func (s Stats) Summary() string {
	return fmt.Sprintf("scanned %d, new %d, attachments %d, stored %d, skipped %d, failed %d",
		s.MessagesScanned, s.MessagesNew, s.Attachments, s.Invoices, s.Skipped, s.Failed)
}

// Fetch pulls new mail, extracts every invoice attachment and stores results.
func Fetch(ctx context.Context, cfg *config.Config, db *store.Store, logf LogFunc) (*Stats, error) {
	if err := cfg.RequireMail(); err != nil {
		return nil, err
	}
	if err := cfg.RequireGemini(); err != nil {
		return nil, err
	}

	ex, err := extract.New(ctx, cfg.GeminiKey, cfg.GeminiModel)
	if err != nil {
		return nil, err
	}

	guidance := LoadGuidance(db, logf)

	logf.printf("connecting to %s:%d as %s", cfg.IMAPHost, cfg.IMAPPort, cfg.IMAPUser)
	mb, err := mailbox.Connect(mailbox.Options{
		Host: cfg.IMAPHost, Port: cfg.IMAPPort,
		User: cfg.IMAPUser, Pass: cfg.IMAPPass, Mailbox: cfg.IMAPMailbox,
	})
	if err != nil {
		return nil, err
	}
	defer mb.Close()

	uids, err := mb.SearchRecent(cfg.LookbackDays)
	if err != nil {
		return nil, err
	}

	st := &Stats{MessagesScanned: len(uids)}
	logf.printf("mailbox %s: %d message(s) in the last %d day(s)", cfg.IMAPMailbox, len(uids), cfg.LookbackDays)

	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		seen, err := db.IsMessageSeen(cfg.IMAPMailbox, uid)
		if err != nil {
			return st, err
		}
		if seen {
			continue
		}
		st.MessagesNew++

		msg, err := mb.FetchMessage(uid)
		if err != nil {
			logf.printf("uid %d: %v", uid, err)
			st.Failed++
			continue
		}
		if len(msg.Attachments) == 0 {
			// Nothing to extract, but record it so we do not re-download it.
			if err := db.MarkMessageSeen(cfg.IMAPMailbox, uid); err != nil {
				return st, err
			}
			continue
		}

		logf.printf("message %q from %s: %d attachment(s)", trim(msg.Subject, 60), msg.From, len(msg.Attachments))
		for _, att := range msg.Attachments {
			st.Attachments++
			if err := processAttachment(ctx, cfg, db, ex, msg, att, st, logf, guidance); err != nil {
				logf.printf("uid %d %q: %v", uid, att.Filename, err)
				st.Failed++
			}
		}

		if err := db.MarkMessageSeen(cfg.IMAPMailbox, uid); err != nil {
			return st, err
		}
		if err := mb.Flag(uid, ProcessedFlag); err != nil {
			logf.printf("uid %d: could not flag: %v", uid, err)
		}
	}
	return st, nil
}

// IngestFile processes invoice documents already on disk — a scanned paper
// invoice, or one uploaded through the dashboard — through the same path as mail.
func IngestFile(ctx context.Context, cfg *config.Config, db *store.Store, paths []string, logf LogFunc) (*Stats, error) {
	if err := cfg.RequireGemini(); err != nil {
		return nil, err
	}
	ex, err := extract.New(ctx, cfg.GeminiKey, cfg.GeminiModel)
	if err != nil {
		return nil, err
	}

	guidance := LoadGuidance(db, logf)

	st := &Stats{}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			logf.printf("%s: %v", p, err)
			st.Failed++
			continue
		}
		mimeType := mimeFromExt(p)
		if mimeType == "" {
			logf.printf("%s: unsupported file type", filepath.Base(p))
			st.Failed++
			continue
		}
		st.Attachments++

		modTime := time.Now()
		if info, err := os.Stat(p); err == nil {
			modTime = info.ModTime()
		}
		msg := &mailbox.Message{
			Subject: "uploaded: " + filepath.Base(p),
			From:    "(upload)", Date: modTime,
		}
		att := mailbox.Attachment{Filename: filepath.Base(p), MIMEType: mimeType, Data: data}

		logf.printf("reading %s (%s)", filepath.Base(p), humanBytes(len(data)))
		if err := processAttachment(ctx, cfg, db, ex, msg, att, st, logf, guidance); err != nil {
			logf.printf("%s: %v", filepath.Base(p), err)
			st.Failed++
		}
	}
	return st, nil
}

func mimeFromExt(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".heic":
		return "image/heic"
	}
	return ""
}

// CheckMail verifies credentials and returns how many messages fall inside the
// lookback window, without downloading or extracting anything.
func CheckMail(cfg *config.Config) (int, error) {
	if err := cfg.RequireMail(); err != nil {
		return 0, err
	}
	mb, err := mailbox.Connect(mailbox.Options{
		Host: cfg.IMAPHost, Port: cfg.IMAPPort,
		User: cfg.IMAPUser, Pass: cfg.IMAPPass, Mailbox: cfg.IMAPMailbox,
	})
	if err != nil {
		return 0, err
	}
	defer mb.Close()

	uids, err := mb.SearchRecent(cfg.LookbackDays)
	if err != nil {
		return 0, err
	}
	return len(uids), nil
}

func processAttachment(ctx context.Context, cfg *config.Config, db *store.Store,
	ex *extract.Client, msg *mailbox.Message, att mailbox.Attachment, st *Stats,
	logf LogFunc, g *extract.Guidance) error {

	sum := sha256.Sum256(att.Data)
	sha := hex.EncodeToString(sum[:])

	dup, err := db.HasFile(sha)
	if err != nil {
		return err
	}
	if dup {
		st.Skipped++
		logf.printf("skip %q: already ingested", att.Filename)
		return nil
	}

	res, raw, err := ex.Extract(ctx, extract.Document{
		Filename: att.Filename, MIMEType: att.MIMEType, Data: att.Data,
	}, g)
	if err != nil {
		return err
	}
	if !res.IsInvoice {
		st.Skipped++
		logf.printf("skip %q: not an invoice", att.Filename)
		return nil
	}

	// Keep the original document; the spreadsheet is a derived view, never the
	// only record.
	path, err := saveAttachment(cfg, msg.Date, sha, att)
	if err != nil {
		return err
	}

	inv := &store.Invoice{
		FileSHA256: sha, SourceFile: path,
		MailUID: msg.UID, MailSubject: msg.Subject, MailFrom: msg.From,
		MailDate:      msg.Date.Format(time.RFC3339),
		Supplier:      res.Supplier,
		InvoiceNumber: res.InvoiceNumber,
		InvoiceDate:   res.InvoiceDate,
		VehicleReg:    res.VehicleReg,
		Currency:      strings.ToUpper(res.Currency),
		Netto:         res.Netto, VATAmount: res.VATAmount, VATRate: res.VATRate, Brutto: res.Brutto,
		IsGeneral: res.IsGeneralStock,
		RawJSON:   raw,
	}
	inv.Notes, inv.NeedsReview = audit(res)
	if err := linkCreditNote(db, res, inv); err != nil {
		return err
	}

	for i, it := range res.Items {
		inv.Items = append(inv.Items, store.Item{
			LineNo: i + 1, PartNumber: it.PartNumber, Desc: it.Desc, VehicleReg: it.VehicleReg,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice,
			Netto: it.Netto, VATAmount: it.VATAmount, VATRate: it.VATRate, Brutto: it.Brutto,
		})
	}

	if _, err := db.InsertInvoice(inv); err != nil {
		return err
	}
	if inv.CreditOf != nil {
		if err := db.MarkReturned(*inv.CreditOf); err != nil {
			return err
		}
	}
	st.Invoices++
	logf.printf("stored %s %s  %s %.2f  %s  %d line(s)%s%s",
		inv.Supplier, inv.InvoiceNumber, inv.Currency, inv.Brutto, target(inv),
		len(inv.Items), reviewSuffix(inv.NeedsReview), creditSuffix(inv))
	return nil
}

// linkCreditNote looks for the invoice a credit note's stated reference
// names and, when found, links this credit note to it (store.Invoice.CreditOf)
// so InsertInvoice's caller can flag that original returned. A credit note
// is always still stored on its own — its own (usually negative) amounts
// are what actually reduce the total, exactly as they always have — this
// only adds the link when one is confidently found. No reference stated, or
// no single matching invoice, gets flagged for a human instead of guessed.
func linkCreditNote(db *store.Store, res *extract.Result, inv *store.Invoice) error {
	if !res.IsCreditNote {
		return nil
	}
	ref := strings.TrimSpace(res.CreditReference)
	if ref == "" {
		inv.Notes = appendNote(inv.Notes, "credit note with no stated reference to an original invoice — link it manually if it should mark one returned")
		inv.NeedsReview = true
		return nil
	}
	id, found, err := db.FindInvoiceByReference(res.Supplier, ref)
	if err != nil {
		return err
	}
	if !found {
		inv.Notes = appendNote(inv.Notes,
			fmt.Sprintf("credit note references invoice %q but no single matching invoice was found — link it manually", ref))
		inv.NeedsReview = true
		return nil
	}
	inv.CreditOf = &id
	return nil
}

func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

func creditSuffix(inv *store.Invoice) string {
	if inv.CreditOf == nil {
		return ""
	}
	return fmt.Sprintf("  [credits invoice #%d, marked returned]", *inv.CreditOf)
}

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// audit sanity-checks the model's arithmetic. Anything that does not add up is
// flagged rather than silently trusted — these numbers end up on a VAT return.
//
// The model's own commentary is recorded as a note but does not by itself
// trigger review: it remarks on routine things like deriving line-level VAT,
// and a flag that fires on every invoice would be worth nothing.
func audit(r *extract.Result) (notes string, needsReview bool) {
	var problems []string
	if r.Confidence == "low" {
		problems = append(problems, "model reported low confidence")
	}
	if !isoDate.MatchString(r.InvoiceDate) {
		problems = append(problems, "unrecognised invoice date")
	}
	if r.Brutto == 0 && r.Netto == 0 {
		problems = append(problems, "no totals found")
	}
	if r.Netto != 0 && r.Brutto != 0 {
		if diff := r.Netto + r.VATAmount - r.Brutto; diff > 0.02 || diff < -0.02 {
			problems = append(problems, fmt.Sprintf("netto+VAT off brutto by %.2f", diff))
		}
	}
	// Workshop stock legitimately has no registration, so only a purchase that
	// claims to be vehicle work and still names no vehicle is worth flagging.
	if !r.IsGeneralStock && r.VehicleReg == "" && !anyItemHasReg(r) {
		problems = append(problems, "no vehicle reg found, and not marked as general stock")
	}
	if itemsTotal := sumItemNetto(r); itemsTotal > 0 && r.Netto > 0 {
		if diff := itemsTotal - r.Netto; diff > 0.02 || diff < -0.02 {
			problems = append(problems, fmt.Sprintf("line items sum to %.2f, invoice netto is %.2f", itemsTotal, r.Netto))
		}
	}

	needsReview = len(problems) > 0
	if r.Notes != "" {
		problems = append(problems, r.Notes)
	}
	return strings.Join(problems, "; "), needsReview
}

func sumItemNetto(r *extract.Result) float64 {
	var total float64
	for _, it := range r.Items {
		total += it.Netto
	}
	return total
}

func anyItemHasReg(r *extract.Result) bool {
	for _, it := range r.Items {
		if it.VehicleReg != "" {
			return true
		}
	}
	return false
}

func saveAttachment(cfg *config.Config, date time.Time, sha string, att mailbox.Attachment) (string, error) {
	if date.IsZero() {
		date = time.Now()
	}
	dir := filepath.Join(cfg.AttachmentsDir(), date.Format("2006"), date.Format("01"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s", sha[:12], sanitize(att.Filename))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, att.Data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(name string) string {
	name = unsafeChars.ReplaceAllString(filepath.Base(name), "_")
	if len(name) > 80 {
		name = name[len(name)-80:]
	}
	if name == "" || name == "_" {
		return "attachment.bin"
	}
	return name
}

// Export writes the full dataset to a dated workbook in the exports directory.
func Export(cfg *config.Config, db *store.Store) (string, int, error) {
	invoices, err := db.ListInvoices("", "")
	if err != nil {
		return "", 0, err
	}
	path := export.DefaultPath(cfg.ExportsDir(), time.Now())
	// Write the manifest too, so a workbook made from the command line or the
	// daily timer appears in the dashboard's export list with its counts
	// rather than as an unlabelled file.
	written, err := export.WriteWithManifest(path, "Everything", "", "", invoices)
	if err != nil {
		return "", 0, err
	}
	return written, len(invoices), nil
}

// target describes what the purchase was for, in the log line.
func target(inv *store.Invoice) string {
	if inv.IsGeneral {
		return "general stock"
	}
	if inv.VehicleReg == "" {
		return "reg=-"
	}
	return "reg=" + inv.VehicleReg
}

func reviewSuffix(b bool) string {
	if b {
		return "  [NEEDS REVIEW]"
	}
	return ""
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func humanBytes(n int) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := int64(n) / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
