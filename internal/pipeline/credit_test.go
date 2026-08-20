package pipeline

import (
	"testing"

	"goldstar/internal/extract"
	"goldstar/internal/store"
)

func insertOriginal(t *testing.T, db *store.Store, sha, supplier, invoiceNumber string) int64 {
	t.Helper()
	id, err := db.InsertInvoice(&store.Invoice{
		FileSHA256: sha, SourceFile: "x.pdf", MailDate: "2026-08-01T00:00:00Z",
		Supplier: supplier, InvoiceNumber: invoiceNumber, InvoiceDate: "2026-08-01",
		Currency: "GBP", Netto: 100, VATAmount: 20, Brutto: 120,
	})
	if err != nil {
		t.Fatalf("insertOriginal: %v", err)
	}
	return id
}

// A credit note that states which invoice it credits, and matches exactly
// one existing invoice from the same supplier, links to it automatically —
// no manual review needed.
func TestLinkCreditNoteMatchesByReference(t *testing.T) {
	db := openDB(t)
	origID := insertOriginal(t, db, "orig-sha", "ACME Parts", "INV-100")

	res := &extract.Result{
		Supplier: "ACME Parts", IsCreditNote: true, CreditReference: "INV-100",
	}
	inv := &store.Invoice{Supplier: res.Supplier}
	if err := linkCreditNote(db, res, inv); err != nil {
		t.Fatalf("linkCreditNote: %v", err)
	}
	if inv.CreditOf == nil || *inv.CreditOf != origID {
		t.Fatalf("CreditOf = %v, want %d", inv.CreditOf, origID)
	}
	if inv.NeedsReview {
		t.Fatalf("a confidently matched credit note should not need review")
	}
}

// No reference stated on the credit note — nothing to match against, so it
// is flagged for a human rather than left silently unlinked.
func TestLinkCreditNoteWithNoReferenceNeedsReview(t *testing.T) {
	db := openDB(t)
	res := &extract.Result{Supplier: "ACME Parts", IsCreditNote: true, CreditReference: ""}
	inv := &store.Invoice{Supplier: res.Supplier}
	if err := linkCreditNote(db, res, inv); err != nil {
		t.Fatalf("linkCreditNote: %v", err)
	}
	if inv.CreditOf != nil {
		t.Fatalf("CreditOf = %v, want nil (nothing to link to)", inv.CreditOf)
	}
	if !inv.NeedsReview {
		t.Fatalf("a credit note with no stated reference should need review")
	}
}

// A stated reference that matches nothing on file — same treatment as no
// reference at all: flagged, not guessed.
func TestLinkCreditNoteWithUnmatchedReferenceNeedsReview(t *testing.T) {
	db := openDB(t)
	insertOriginal(t, db, "orig-sha", "ACME Parts", "INV-100")

	res := &extract.Result{Supplier: "ACME Parts", IsCreditNote: true, CreditReference: "INV-999"}
	inv := &store.Invoice{Supplier: res.Supplier}
	if err := linkCreditNote(db, res, inv); err != nil {
		t.Fatalf("linkCreditNote: %v", err)
	}
	if inv.CreditOf != nil {
		t.Fatalf("CreditOf = %v, want nil (no invoice INV-999 on file)", inv.CreditOf)
	}
	if !inv.NeedsReview {
		t.Fatalf("an unmatched credit reference should need review")
	}
}

// Two different suppliers happening to reuse the same invoice number must
// not cross-match — the supplier is part of the key, not just the number.
func TestLinkCreditNoteDoesNotMatchAcrossSuppliers(t *testing.T) {
	db := openDB(t)
	insertOriginal(t, db, "orig-sha", "ACME Parts", "INV-100")

	res := &extract.Result{Supplier: "Different Supplier Ltd", IsCreditNote: true, CreditReference: "INV-100"}
	inv := &store.Invoice{Supplier: res.Supplier}
	if err := linkCreditNote(db, res, inv); err != nil {
		t.Fatalf("linkCreditNote: %v", err)
	}
	if inv.CreditOf != nil {
		t.Fatalf("CreditOf = %v, want nil — the matching invoice belongs to a different supplier", inv.CreditOf)
	}
	if !inv.NeedsReview {
		t.Fatalf("a cross-supplier non-match should still need review")
	}
}

// A document that is not itself a credit note is left completely alone —
// linkCreditNote must be a no-op for every ordinary invoice.
func TestLinkCreditNoteIsNoOpForOrdinaryInvoices(t *testing.T) {
	db := openDB(t)
	res := &extract.Result{Supplier: "ACME Parts", IsCreditNote: false}
	inv := &store.Invoice{Supplier: res.Supplier, NeedsReview: false}
	if err := linkCreditNote(db, res, inv); err != nil {
		t.Fatalf("linkCreditNote: %v", err)
	}
	if inv.CreditOf != nil || inv.NeedsReview {
		t.Fatalf("an ordinary invoice must be untouched: CreditOf=%v NeedsReview=%v", inv.CreditOf, inv.NeedsReview)
	}
}
