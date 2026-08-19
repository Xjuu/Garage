package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// open makes a throwaway database on disk. SQLite in-memory would not exercise
// the same open path, and these tests are cheap enough to use a real file.
func open(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func add(t *testing.T, db *Store, inv Invoice) int64 {
	t.Helper()
	if inv.FileSHA256 == "" {
		inv.FileSHA256 = inv.InvoiceNumber + "-sha"
	}
	if inv.Currency == "" {
		inv.Currency = "GBP"
	}
	id, err := db.InsertInvoice(&inv)
	if err != nil {
		t.Fatalf("insert %s: %v", inv.InvoiceNumber, err)
	}
	return id
}

// NormalizeReg is the join key between an invoice and the vehicle registry, so
// a change here silently splits a car's history in two.
func TestNormalizeReg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ab12cde", "AB12CDE"},
		{"AB12 CDE", "AB12CDE"},
		{"MJ65-VJZ", "MJ65VJZ"},
		{" fg21oxa ", "FG21OXA"},
		{"M8 TXI", "M8TXI"},
		// Placeholders are not registrations. A literal "-" sorts ahead of
		// every real plate and shows up as a vehicle in its own right.
		{"-", ""}, {"--", ""}, {"N/A", ""}, {"n/a", ""},
		{"none", ""}, {"NONE", ""}, {"unknown", ""}, {"Not specified", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeReg(c.in); got != c.want {
			t.Errorf("NormalizeReg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Money must survive the round trip to SQLite and back untouched. Pence errors
// here become VAT-return errors.
func TestInvoiceTotalsRoundTrip(t *testing.T) {
	db := open(t)
	in := Invoice{
		Supplier: "Millfield Autoparts Ltd", InvoiceNumber: "HS351559",
		InvoiceDate: "2026-08-17", VehicleReg: "MJ65VJZ",
		Netto: 209.31, VATAmount: 41.86, VATRate: 20, Brutto: 251.17,
		Items: []Item{{PartNumber: "OESE020303", Desc: "EGR module",
			Quantity: 1, UnitPrice: 209.31, Netto: 209.31,
			VATRate: 20, VATAmount: 41.86, Brutto: 251.17}},
	}
	id := add(t, db, in)

	got, err := db.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"netto", got.Netto, 209.31},
		{"vat", got.VATAmount, 41.86},
		{"brutto", got.Brutto, 251.17},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(got.Items))
	}
	if got.Items[0].UnitPrice != 209.31 {
		t.Errorf("unit price = %v, want 209.31", got.Items[0].UnitPrice)
	}
}

// The same document arriving twice — resent, or forwarded — must not be
// counted twice. This is what keeps the VAT total honest.
func TestDuplicateFileRejected(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{FileSHA256: "same", InvoiceNumber: "A1", Brutto: 100, InvoiceDate: "2026-08-01"})

	if has, err := db.HasFile("same"); err != nil || !has {
		t.Fatalf("HasFile(same) = %v, %v; want true, nil", has, err)
	}
	if has, err := db.HasFile("other"); err != nil || has {
		t.Fatalf("HasFile(other) = %v, %v; want false, nil", has, err)
	}
}

// Search totals drive the figures shown beside the table and the export, so
// they must match the rows the filter selected — not everything in the table.
func TestSearchTotalsMatchFilter(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{InvoiceNumber: "A", Supplier: "Alpha", InvoiceDate: "2026-07-10",
		Netto: 100, VATAmount: 20, Brutto: 120})
	add(t, db, Invoice{InvoiceNumber: "B", Supplier: "Beta", InvoiceDate: "2026-08-10",
		Netto: 200, VATAmount: 40, Brutto: 240})
	add(t, db, Invoice{InvoiceNumber: "C", Supplier: "Beta", InvoiceDate: "2026-08-20",
		Netto: 50, VATAmount: 10, Brutto: 60})

	page, err := db.Search(Query{From: "2026-08-01", To: "2026-08-31"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if page.Total != 2 {
		t.Errorf("total = %d, want 2", page.Total)
	}
	if page.Brutto != 300 {
		t.Errorf("brutto = %v, want 300", page.Brutto)
	}
	if page.VAT != 50 {
		t.Errorf("vat = %v, want 50", page.VAT)
	}
}

// Case must not matter when searching: an operator types a plate in lower case.
func TestGlobalSearchIsCaseInsensitive(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{InvoiceNumber: "A", Supplier: "Millfield Autoparts Ltd",
		VehicleReg: "MJ65VJZ", InvoiceDate: "2026-08-17", Brutto: 251.17})

	for _, q := range []string{"mj65vjz", "MJ65VJZ", "Mj65Vjz", "millfield", "MILLFIELD"} {
		res, err := db.GlobalSearch(q, "", "")
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if res.Total == 0 {
			t.Errorf("search %q found nothing", q)
		}
	}
}

// A month-to-date figure compared against a whole previous month always reads
// as a fall. The comparison window must be the same number of days.
func TestThisMonthComparesEqualWindows(t *testing.T) {
	db := open(t)
	// 10 August, and the equivalent first-10-days of July.
	add(t, db, Invoice{InvoiceNumber: "AUG", InvoiceDate: "2026-08-05", Brutto: 100, Netto: 80, VATAmount: 20})
	add(t, db, Invoice{InvoiceNumber: "JUL1", InvoiceDate: "2026-07-05", Brutto: 50, Netto: 40, VATAmount: 10})
	// Late July must be excluded: it is outside the first 10 days.
	add(t, db, Invoice{InvoiceNumber: "JUL2", InvoiceDate: "2026-07-25", Brutto: 900, Netto: 720, VATAmount: 180})

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	m, err := db.ThisMonth(now)
	if err != nil {
		t.Fatalf("this month: %v", err)
	}
	if m.Brutto != 100 {
		t.Errorf("this month brutto = %v, want 100", m.Brutto)
	}
	if m.PrevBrutto != 50 {
		t.Errorf("previous window brutto = %v, want 50 (late July must be excluded)", m.PrevBrutto)
	}
	if m.ChangePct != 100 {
		t.Errorf("change = %v%%, want 100%%", m.ChangePct)
	}
}

// General workshop stock has no registration and must not be attributed to a
// vehicle, nor counted as a vehicle in its own right.
func TestGeneralStockExcludedFromVehicleScope(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{InvoiceNumber: "V", VehicleReg: "AB12CDE", InvoiceDate: "2026-08-10", Brutto: 100})
	add(t, db, Invoice{InvoiceNumber: "G", IsGeneral: true, InvoiceDate: "2026-08-10", Brutto: 40})

	general, err := db.Spending(TrendQuery{Period: "365d", Scope: "general"})
	if err != nil {
		t.Fatalf("spending general: %v", err)
	}
	if general.Brutto != 40 {
		t.Errorf("general stock = %v, want 40", general.Brutto)
	}

	vehicle, err := db.Spending(TrendQuery{Period: "365d", Scope: "vehicle"})
	if err != nil {
		t.Fatalf("spending vehicle: %v", err)
	}
	if vehicle.Brutto != 100 {
		t.Errorf("vehicle work = %v, want 100", vehicle.Brutto)
	}
}

// Credit notes are stored with negative totals. Netting them into a single
// "spend" figure hides them; one large credit can make a month of real
// purchasing show as a negative number that reads as a bug.
func TestOverviewSeparatesCreditNotes(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{InvoiceNumber: "HS1", InvoiceDate: "2026-08-03",
		Netto: 100, VATAmount: 20, Brutto: 120})
	add(t, db, Invoice{InvoiceNumber: "HS2", InvoiceDate: "2026-08-04",
		Netto: 50, VATAmount: 10, Brutto: 60})
	add(t, db, Invoice{InvoiceNumber: "HC1", InvoiceDate: "2026-08-04",
		Netto: -200, VATAmount: -40, Brutto: -240})

	o, err := db.Overview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if o.Purchases != 180 {
		t.Errorf("purchases = %v, want 180", o.Purchases)
	}
	if o.Credits != -240 {
		t.Errorf("credits = %v, want -240", o.Credits)
	}
	if o.CreditCount != 1 {
		t.Errorf("credit count = %d, want 1", o.CreditCount)
	}
	// The net figure stays available and stays negative — that is the truth,
	// it just must not be the only number on show.
	if o.Brutto != -60 {
		t.Errorf("net = %v, want -60", o.Brutto)
	}
}

func TestThisMonthSeparatesCreditNotes(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{InvoiceNumber: "HS1", InvoiceDate: "2026-08-03", Brutto: 120, Netto: 100, VATAmount: 20})
	add(t, db, Invoice{InvoiceNumber: "HC1", InvoiceDate: "2026-08-05", Brutto: -240, Netto: -200, VATAmount: -40})

	m, err := db.ThisMonth(time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("this month: %v", err)
	}
	if m.Purchases != 120 {
		t.Errorf("purchases = %v, want 120", m.Purchases)
	}
	if m.Credits != -240 {
		t.Errorf("credits = %v, want -240", m.Credits)
	}
	if m.CreditCount != 1 {
		t.Errorf("credit count = %d, want 1", m.CreditCount)
	}
}

// A clean shutdown checkpoints the write-ahead log so the .db file is complete
// on its own. Without this the file can be copied or backed up while rows live
// only in the -wal, and the copy silently omits them.
func TestCheckpointFoldsWALIntoTheDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := range 25 {
		add(t, db, Invoice{
			InvoiceNumber: fmt.Sprintf("INV-%03d", i),
			InvoiceDate:   "2026-08-19", Brutto: float64(i + 1),
		})
	}

	if err := db.Checkpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Open the file with the -wal deleted: anything not folded in is gone.
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	var n int
	if err := again.db.QueryRow(`SELECT COUNT(*) FROM invoices`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 25 {
		t.Errorf("after checkpoint and discarding the WAL, %d of 25 invoices survived", n)
	}
}
