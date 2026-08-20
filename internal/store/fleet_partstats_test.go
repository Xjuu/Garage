package store

import (
	"math"
	"testing"
)

// Money is float64 arithmetic under the hood, so 0.56+0.40 lands a shade off
// 0.96 rather than exactly on it — comparisons need a penny of slack, not
// bit-exact equality.
func closeEnough(got, want float64) bool { return math.Abs(got-want) < 0.005 }

// The Parts detail page shows a "Total (incl. VAT)" tile alongside the net
// figure — PartStats has to carry the gross total, not just netto, or that
// tile has nothing to show. Two purchases of the same part, across two
// invoices, must sum correctly on both sides of VAT.
func TestPartStatsIncludesVATAndGrossTotals(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{
		InvoiceNumber: "INV-A", VehicleReg: "AB12CDE", InvoiceDate: "2026-08-01",
		Items: []Item{
			{PartNumber: "JRP308W", Desc: "Number plate fixings screw",
				Quantity: 3, UnitPrice: 0.94, Netto: 2.82, VATAmount: 0.56, Brutto: 3.38},
		},
	})
	add(t, db, Invoice{
		InvoiceNumber: "INV-B", VehicleReg: "CD34EFG", InvoiceDate: "2026-08-05",
		Items: []Item{
			{PartNumber: "JRP308W", Desc: "Number plate fixings screw",
				Quantity: 2, UnitPrice: 1.00, Netto: 2.00, VATAmount: 0.40, Brutto: 2.40},
		},
	})

	stats, err := db.PartStats("JRP308W")
	if err != nil {
		t.Fatalf("PartStats: %v", err)
	}
	if !closeEnough(stats.Netto, 4.82) {
		t.Errorf("Netto = %v, want 4.82", stats.Netto)
	}
	if !closeEnough(stats.VAT, 0.96) {
		t.Errorf("VAT = %v, want 0.96", stats.VAT)
	}
	if !closeEnough(stats.Brutto, 5.78) {
		t.Errorf("Brutto = %v, want 5.78", stats.Brutto)
	}
	if len(stats.History) != 2 {
		t.Fatalf("history has %d point(s), want 2", len(stats.History))
	}
	for _, p := range stats.History {
		if p.Brutto == 0 {
			t.Errorf("history point for invoice %d has no Brutto", p.InvoiceID)
		}
	}
}
