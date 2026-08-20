package store

import "testing"

func TestFindInvoiceByReferenceMatchesExactlyOne(t *testing.T) {
	db := open(t)
	origID := add(t, db, Invoice{Supplier: "ACME Parts", InvoiceNumber: "INV-100"})

	id, found, err := db.FindInvoiceByReference("ACME Parts", "INV-100")
	if err != nil {
		t.Fatalf("FindInvoiceByReference: %v", err)
	}
	if !found || id != origID {
		t.Fatalf("found=%v id=%d, want found=true id=%d", found, id, origID)
	}
}

func TestFindInvoiceByReferenceReturnsNotFoundWithoutAMatch(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{Supplier: "ACME Parts", InvoiceNumber: "INV-100"})

	_, found, err := db.FindInvoiceByReference("ACME Parts", "INV-999")
	if err != nil {
		t.Fatalf("FindInvoiceByReference: %v", err)
	}
	if found {
		t.Fatalf("found = true, want false — no invoice INV-999 on file")
	}
}

// Two invoices sharing the same number (a supplier reusing one, or two
// different suppliers) must not resolve to a guess — acting on the wrong
// one automatically would corrupt a financial record with nobody reviewing
// the choice.
func TestFindInvoiceByReferenceRefusesAnAmbiguousMatch(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{FileSHA256: "a", Supplier: "ACME Parts", InvoiceNumber: "INV-100"})
	add(t, db, Invoice{FileSHA256: "b", Supplier: "ACME Parts", InvoiceNumber: "INV-100"})

	_, found, err := db.FindInvoiceByReference("ACME Parts", "INV-100")
	if err != nil {
		t.Fatalf("FindInvoiceByReference: %v", err)
	}
	if found {
		t.Fatalf("found = true, want false — two invoices share this number, refuse to guess")
	}
}

// A credit note itself must never be treated as "the original" a later
// credit note could reference — crediting a credit note makes no sense, and
// would only mean a supplier reused a number.
func TestFindInvoiceByReferenceExcludesExistingCreditNotes(t *testing.T) {
	db := open(t)
	origID := add(t, db, Invoice{FileSHA256: "orig", Supplier: "ACME Parts", InvoiceNumber: "INV-100"})
	cn := origID
	add(t, db, Invoice{FileSHA256: "cn", Supplier: "ACME Parts", InvoiceNumber: "INV-100", CreditOf: &cn})

	// Both rows now share supplier+number; only the real original is
	// eligible, so the match still has to resolve cleanly to it, not go
	// ambiguous or pick the credit note.
	id, found, err := db.FindInvoiceByReference("ACME Parts", "INV-100")
	if err != nil {
		t.Fatalf("FindInvoiceByReference: %v", err)
	}
	if !found || id != origID {
		t.Fatalf("found=%v id=%d, want the original (%d), not the credit note", found, id, origID)
	}
}

func TestFindInvoiceByReferenceRejectsEmptyInputWithoutErroring(t *testing.T) {
	db := open(t)
	if _, found, err := db.FindInvoiceByReference("", "INV-100"); err != nil || found {
		t.Fatalf("empty supplier: found=%v err=%v", found, err)
	}
	if _, found, err := db.FindInvoiceByReference("ACME Parts", ""); err != nil || found {
		t.Fatalf("empty invoice number: found=%v err=%v", found, err)
	}
}

func TestMarkReturnedSetsTheFlag(t *testing.T) {
	db := open(t)
	id := add(t, db, Invoice{Supplier: "ACME Parts", InvoiceNumber: "INV-100"})

	before, err := db.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if before.Returned {
		t.Fatalf("a fresh invoice must not start out returned")
	}

	if err := db.MarkReturned(id); err != nil {
		t.Fatalf("MarkReturned: %v", err)
	}
	after, err := db.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !after.Returned {
		t.Fatalf("MarkReturned did not set the flag")
	}
}

// CreditOf is a nullable link stored on the credit note's own row — this
// pins down that it survives the round trip through both Get (single) and
// Search (list) instead of silently coming back nil.
func TestCreditOfRoundTripsThroughGetAndSearch(t *testing.T) {
	db := open(t)
	origID := add(t, db, Invoice{FileSHA256: "orig", Supplier: "ACME Parts", InvoiceNumber: "INV-100"})
	cn := origID
	cnID := add(t, db, Invoice{FileSHA256: "cn", Supplier: "ACME Parts", InvoiceNumber: "CN-1", CreditOf: &cn})
	if err := db.MarkReturned(origID); err != nil {
		t.Fatalf("MarkReturned: %v", err)
	}

	got, err := db.Get(cnID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CreditOf == nil || *got.CreditOf != origID {
		t.Fatalf("Get(%d).CreditOf = %v, want %d", cnID, got.CreditOf, origID)
	}

	orig, err := db.Get(origID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !orig.Returned {
		t.Fatalf("the original invoice should read back as returned")
	}
	if orig.CreditOf != nil {
		t.Fatalf("the original invoice must not itself carry a CreditOf: got %v", orig.CreditOf)
	}

	page, err := db.Search(Query{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var sawLinked, sawReturned bool
	for _, inv := range page.Invoices {
		if inv.ID == cnID && inv.CreditOf != nil && *inv.CreditOf == origID {
			sawLinked = true
		}
		if inv.ID == origID && inv.Returned {
			sawReturned = true
		}
	}
	if !sawLinked {
		t.Fatalf("Search results did not carry the credit note's CreditOf link")
	}
	if !sawReturned {
		t.Fatalf("Search results did not carry the original's Returned flag")
	}
}

// A human can toggle Returned manually through the normal Patch path — a
// safety valve for correcting a wrong automatic match or flagging something
// the automatic matching missed entirely.
func TestReturnedIsManuallyPatchable(t *testing.T) {
	db := open(t)
	id := add(t, db, Invoice{Supplier: "ACME Parts", InvoiceNumber: "INV-100"})

	yes := true
	if err := db.Update(id, Patch{Returned: &yes}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := db.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Returned {
		t.Fatalf("Patch.Returned=true did not take effect")
	}

	no := false
	if err := db.Update(id, Patch{Returned: &no}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err = db.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Returned {
		t.Fatalf("Patch.Returned=false did not clear it")
	}
}
