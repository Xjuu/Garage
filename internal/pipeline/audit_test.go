package pipeline

import (
	"strings"
	"testing"

	"goldstar/internal/extract"
)

// The audit is the last thing standing between a misread PDF and a wrong VAT
// return. These cases pin down what it must catch and — just as important —
// what it must not flag, because a queue full of false alarms gets ignored.
func TestAudit(t *testing.T) {
	good := func() *extract.Result {
		return &extract.Result{
			Confidence: "high", InvoiceDate: "2026-08-17", VehicleReg: "MJ65VJZ",
			Netto: 209.31, VATAmount: 41.86, Brutto: 251.17,
			Items: []extract.Item{{PartNumber: "OESE020303", Netto: 209.31}},
		}
	}

	cases := []struct {
		name   string
		mutate func(*extract.Result)
		flag   bool
		expect string // substring the note must contain when flagged
	}{
		{"clean invoice", func(r *extract.Result) {}, false, ""},

		{"arithmetic does not reconcile", func(r *extract.Result) {
			r.Brutto = 300 // netto+VAT is 251.17
		}, true, "off brutto"},

		{"line items do not sum to the invoice net", func(r *extract.Result) {
			r.Items = []extract.Item{{Netto: 100}}
		}, true, "line items sum"},

		{"low model confidence", func(r *extract.Result) {
			r.Confidence = "low"
		}, true, "low confidence"},

		{"unparseable date", func(r *extract.Result) {
			r.InvoiceDate = "17th August"
		}, true, "invoice date"},

		{"no totals at all", func(r *extract.Result) {
			r.Netto, r.VATAmount, r.Brutto = 0, 0, 0
			r.Items = nil
		}, true, "no totals"},

		{"vehicle work with no registration", func(r *extract.Result) {
			r.VehicleReg = ""
		}, true, "no vehicle reg"},

		// Consumables genuinely have no plate. Flagging these buried the real
		// problems in noise.
		{"general stock needs no registration", func(r *extract.Result) {
			r.VehicleReg = ""
			r.IsGeneralStock = true
		}, false, ""},

		// A per-line registration is enough; the invoice header need not carry one.
		{"registration on the line item only", func(r *extract.Result) {
			r.VehicleReg = ""
			r.Items = []extract.Item{{VehicleReg: "AB12CDE", Netto: 209.31}}
		}, false, ""},

		// Rounding of a penny is normal on a real invoice and must not flag.
		{"one penny of rounding is tolerated", func(r *extract.Result) {
			r.Brutto = 251.18
		}, false, ""},

		// Model commentary is not a problem with the invoice.
		{"routine model note does not trigger review", func(r *extract.Result) {
			r.Notes = "Line item VAT derived from net at 20%."
		}, false, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := good()
			c.mutate(r)
			notes, flagged := audit(r)

			if flagged != c.flag {
				t.Fatalf("needsReview = %v, want %v (notes: %q)", flagged, c.flag, notes)
			}
			if c.flag && !strings.Contains(notes, c.expect) {
				t.Errorf("notes = %q, want it to mention %q", notes, c.expect)
			}
		})
	}
}

// A note from the model must still be recorded even when nothing is wrong, so
// the reasoning is not lost.
func TestAuditKeepsModelNotes(t *testing.T) {
	r := &extract.Result{
		Confidence: "high", InvoiceDate: "2026-08-17", VehicleReg: "AB12CDE",
		Netto: 100, VATAmount: 20, Brutto: 120,
		Notes: "VAT derived from the summary box.",
	}
	notes, flagged := audit(r)
	if flagged {
		t.Errorf("a clean invoice was flagged: %q", notes)
	}
	if !strings.Contains(notes, "summary box") {
		t.Errorf("model note was dropped; got %q", notes)
	}
}
