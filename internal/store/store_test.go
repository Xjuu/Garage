package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// open makes a throwaway database on disk. SQLite in-memory would not exercise
// the same open path, and these tests are cheap enough to use a real file.
func open(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ── encryption at rest ───────────────────────────────────────────────────
// Every other test in this package opens with an empty key deliberately —
// SQLCipher's own crypto correctness isn't this package's job to re-prove.
// What IS this package's job: that Open actually plumbs a key through to
// the driver at all, that two different keys really do produce two
// different unreadable files rather than silently succeeding either way,
// and that a malformed key is rejected up front rather than surfacing as a
// confusing failure three calls later.

const testKeyA = "79d201e62c8fa1d76a92c38bb5ccef1bc283ef1bc55ebc7290dfa63a4b66479c"
const testKeyB = "cb0e24aafa89c6e503a72c0f022c1da678205824533d8686b6723fd5f5b81617"

func TestOpenWithAKeyProducesAGenuinelyEncryptedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path, testKeyA)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw file: %v", err)
	}
	// Every ordinary, unencrypted SQLite file starts with this exact magic —
	// an encrypted one must not.
	if len(raw) >= 16 && string(raw[:16]) == "SQLite format 3\x00" {
		t.Fatalf("file is plain SQLite, not encrypted, despite a key being given")
	}
}

func TestOpenWithTheWrongKeyFailsToRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path, testKeyA)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close()

	wrong, err := Open(path, testKeyB)
	if err == nil {
		wrong.Close()
		t.Fatalf("Open with the wrong key should have failed outright (applying the schema against a file it can't decrypt), but succeeded")
	}
}

func TestOpenWithTheRightKeyReadsBackWhatWasWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path, testKeyA)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	add(t, db, Invoice{InvoiceNumber: "ENC-1", InvoiceDate: "2026-08-01", Brutto: 123.45})
	db.Close()

	reopened, err := Open(path, testKeyA)
	if err != nil {
		t.Fatalf("reopen with the same key: %v", err)
	}
	defer reopened.Close()

	if has, err := reopened.HasFile("ENC-1-sha"); err != nil || !has {
		t.Fatalf("HasFile after reopening with the correct key = %v, %v; want true, nil", has, err)
	}
}

func TestOpenRejectsAMalformedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	for _, bad := range []string{
		"too-short",
		"not-hex-but-64-characters-long-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		testKeyA + "00", // 66 chars — one byte too many
	} {
		if _, err := Open(path, bad); err == nil {
			t.Fatalf("Open accepted a malformed key: %q", bad)
		}
	}
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

// The Spending page's day-by-day breakdown used to list a returned item
// twice — once as the original purchase, once again as its credit note's
// own negative-quantity line — with neither telling you it was a return.
// Same exclusion Overview and This Month already apply to their own spend
// totals: a returned invoice's headline trend figures and its individual
// line items, on both sides of the return, are excluded here too.
func TestSpendingExcludesReturnedInvoicesAndTheirCreditNotes(t *testing.T) {
	db := open(t)
	kept := add(t, db, Invoice{InvoiceNumber: "HS1", InvoiceDate: "2026-08-10",
		VehicleReg: "AB12CDE", Brutto: 100, Netto: 80, VATAmount: 20,
		Items: []Item{{PartNumber: "KEPT-1", Desc: "A part that stayed bought", Quantity: 1, Brutto: 100}}})
	returnedID := add(t, db, Invoice{InvoiceNumber: "HS2", InvoiceDate: "2026-08-11",
		VehicleReg: "AB12CDE", Brutto: 300, Netto: 250, VATAmount: 50,
		Items: []Item{{PartNumber: "RETURNED-1", Desc: "The original purchase", Quantity: 1, Brutto: 300}}})
	if err := db.MarkReturned(returnedID); err != nil {
		t.Fatalf("MarkReturned: %v", err)
	}
	add(t, db, Invoice{InvoiceNumber: "HS2-CN", InvoiceDate: "2026-08-12",
		VehicleReg: "AB12CDE", Brutto: -300, Netto: -250, VATAmount: -50, CreditOf: &returnedID,
		Items: []Item{{PartNumber: "RETURNED-1", Desc: "The credit note's own line", Quantity: -1, Brutto: -300}}})
	_ = kept

	tr, err := db.Spending(TrendQuery{Period: "365d"})
	if err != nil {
		t.Fatalf("spending: %v", err)
	}
	if tr.Brutto != 100 {
		t.Errorf("headline brutto = %v, want 100 (only the kept invoice)", tr.Brutto)
	}
	if tr.Invoices != 1 {
		t.Errorf("headline invoice count = %d, want 1", tr.Invoices)
	}

	var parts []string
	for _, day := range tr.Detail {
		for _, line := range day.Lines {
			parts = append(parts, line.PartNumber)
		}
	}
	if len(parts) != 1 || parts[0] != "KEPT-1" {
		t.Fatalf("day-by-day line items = %v, want exactly [KEPT-1] — neither side of the return should appear", parts)
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

// A returned invoice's money came back — its full amount must not sit in
// Purchases at all, not even before its credit note (excluded already by
// being negative) gets involved. Otherwise a returned invoice inflates the
// spend total with nothing anywhere to explain why the number looks too
// high.
func TestOverviewExcludesReturnedInvoicesFromPurchases(t *testing.T) {
	db := open(t)
	keptID := add(t, db, Invoice{InvoiceNumber: "HS1", InvoiceDate: "2026-08-03",
		Netto: 100, VATAmount: 20, Brutto: 120})
	returnedID := add(t, db, Invoice{InvoiceNumber: "HS2", InvoiceDate: "2026-08-04",
		Netto: 250, VATAmount: 50, Brutto: 300})
	_ = keptID
	if err := db.MarkReturned(returnedID); err != nil {
		t.Fatalf("MarkReturned: %v", err)
	}

	o, err := db.Overview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if o.Purchases != 120 {
		t.Errorf("purchases = %v, want 120 (the returned £300 invoice must be excluded)", o.Purchases)
	}
	// Brutto is the true raw total across everything — it still includes the
	// returned invoice's amount, since that is genuinely what was invoiced.
	// Only Purchases, the "what did we actually spend" figure, excludes it.
	if o.Brutto != 420 {
		t.Errorf("brutto (raw total) = %v, want 420 — it must NOT also exclude the returned invoice", o.Brutto)
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

// Same exclusion as Overview's own Purchases figure, for the monthly one —
// a returned invoice's money came back, so it must not count as this
// month's spend.
func TestThisMonthExcludesReturnedInvoicesFromPurchases(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{InvoiceNumber: "HS1", InvoiceDate: "2026-08-03", Brutto: 120, Netto: 100, VATAmount: 20})
	returnedID := add(t, db, Invoice{InvoiceNumber: "HS2", InvoiceDate: "2026-08-04", Brutto: 300, Netto: 250, VATAmount: 50})
	if err := db.MarkReturned(returnedID); err != nil {
		t.Fatalf("MarkReturned: %v", err)
	}

	m, err := db.ThisMonth(time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("this month: %v", err)
	}
	if m.Purchases != 120 {
		t.Errorf("purchases = %v, want 120 (the returned £300 invoice must be excluded)", m.Purchases)
	}
	if m.Brutto != 420 {
		t.Errorf("brutto (raw total) = %v, want 420 — it must NOT also exclude the returned invoice", m.Brutto)
	}
}

// A clean shutdown checkpoints the write-ahead log so the .db file is complete
// on its own. Without this the file can be copied or backed up while rows live
// only in the -wal, and the copy silently omits them.
func TestCheckpointFoldsWALIntoTheDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	db, err := Open(path, "")
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

	again, err := Open(path, "")
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

// O and 0 are indistinguishable on a plate but are different vehicles to the
// database, so one mistyped character splits a car's cost history in two. The
// correction is applied only where the UK format makes the intent certain.
func TestNormalizeRegFixesConfusableCharacters(t *testing.T) {
	cases := []struct{ in, want, why string }{
		// The case that actually occurred.
		{"FG210XA", "FG21OXA", "zero in the trailing letters becomes O"},
		{"FG21OXA", "FG21OXA", "the correct spelling is left alone"},

		{"MJ65VJ2", "MJ65VJ2", "a digit that is not 0 or 1 is not touched"},
		{"MJ65VJ0", "MJ65VJO", "trailing zero becomes O"},
		{"MJ65VJ1", "MJ65VJI", "trailing one becomes I"},
		{"MJO5VJZ", "MJ05VJZ", "letter O where a digit belongs becomes zero"},
		{"MJI5VJZ", "MJ15VJZ", "letter I where a digit belongs becomes one"},
		{"0J65VJZ", "OJ65VJZ", "leading zero becomes O"},

		// Anything that would not become a valid plate is left as typed.
		{"ABCDEFG", "", "no digit at all is not a registration"},
		{"A1", "A1", "too short to judge"},
		{"J99NUS", "J99NUS", "older format, untouched"},
		{"FAZ4101", "FAZ4101", "personalised plate, not the standard shape"},
		{"IGZ3356", "IGZ3356", "not the standard shape, left alone"},
	}
	for _, c := range cases {
		if got := NormalizeReg(c.in); got != c.want {
			t.Errorf("NormalizeReg(%q) = %q, want %q — %s", c.in, got, c.want, c.why)
		}
	}
}

// The two spellings must land on the same vehicle, or the costs stay split.
func TestConfusableSpellingsShareOneVehicle(t *testing.T) {
	db := open(t)

	add(t, db, Invoice{InvoiceNumber: "A", VehicleReg: NormalizeReg("FG21OXA"),
		InvoiceDate: "2026-08-17", Brutto: 100})
	add(t, db, Invoice{InvoiceNumber: "B", VehicleReg: NormalizeReg("FG210XA"),
		InvoiceDate: "2026-08-18", Brutto: 104.24})

	rows, err := db.Vehicles()
	if err != nil {
		t.Fatalf("vehicles: %v", err)
	}
	if len(rows) != 1 {
		regs := make([]string, len(rows))
		for i, r := range rows {
			regs[i] = r.VehicleReg
		}
		t.Fatalf("the two spellings produced %d vehicles (%v), want 1", len(rows), regs)
	}
	if rows[0].Brutto != 204.24 {
		t.Errorf("gross = %v, want 204.24 — the spend was split", rows[0].Brutto)
	}
}

// Capabilities is a fleet-level classification (e.g. "F"), not something
// any repair-visit spec update ever sets — there's no Go setter for it yet,
// only a raw column a bulk import writes into directly, so this proves the
// read side (GetVehicle and the registry list both) surfaces whatever is
// actually in that column rather than silently dropping it.
func TestVehicleCapabilitiesRoundTripsThroughGetAndRegistry(t *testing.T) {
	db := open(t)
	if err := db.SaveVehicle("FG21OXA", VehiclePatch{Make: ptr("Skoda"), Model: ptr("Octavia")}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE vehicles SET capabilities = 'F' WHERE registration = 'FG21OXA'`); err != nil {
		t.Fatalf("seed capabilities: %v", err)
	}

	v, err := db.GetVehicle("FG21OXA")
	if err != nil {
		t.Fatalf("GetVehicle: %v", err)
	}
	if v.Capabilities != "F" {
		t.Fatalf("GetVehicle Capabilities = %q, want %q", v.Capabilities, "F")
	}

	rows, err := db.RegisteredVehicles(0)
	if err != nil {
		t.Fatalf("RegisteredVehicles: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Registration == "FG21OXA" {
			found = true
			if r.Capabilities != "F" {
				t.Errorf("registry row Capabilities = %q, want %q", r.Capabilities, "F")
			}
		}
	}
	if !found {
		t.Fatalf("FG21OXA not present in RegisteredVehicles at all")
	}

	// A vehicle nobody has tagged must read as empty, not some other
	// placeholder — "no capabilities set" and "capabilities: (blank
	// string)" have to be the same thing for the frontend's `if (!val)
	// return ''` card-omission logic to work at all.
	if err := db.SaveVehicle("DK18CXR", VehiclePatch{Make: ptr("SEAT")}); err != nil {
		t.Fatal(err)
	}
	untagged, err := db.GetVehicle("DK18CXR")
	if err != nil {
		t.Fatalf("GetVehicle: %v", err)
	}
	if untagged.Capabilities != "" {
		t.Fatalf("an untagged vehicle's Capabilities = %q, want empty", untagged.Capabilities)
	}
}

// The callsign is the number the office uses on the radio, so it has to find
// the car whether or not that car has ever been invoiced. It previously only
// matched vehicles with no invoices — the least useful half.
func TestSearchByCallsignFindsInvoicedVehicles(t *testing.T) {
	db := open(t)

	spent := "Callsign 150"
	if err := db.SaveVehicle("FG21OXA", VehiclePatch{
		Make: ptr("Skoda"), Model: ptr("Octavia"), Notes: &spent,
	}); err != nil {
		t.Fatal(err)
	}
	add(t, db, Invoice{InvoiceNumber: "A", VehicleReg: "FG21OXA",
		InvoiceDate: "2026-08-17", Brutto: 204.24})

	idle := "Callsign 210"
	if err := db.SaveVehicle("DK18CXR", VehiclePatch{
		Make: ptr("SEAT"), Model: ptr("Alhambra"), Notes: &idle,
	}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ query, wantReg string }{
		{"150", "FG21OXA"}, // has invoices — this is the case that failed
		{"210", "DK18CXR"}, // has none
	} {
		res, err := db.GlobalSearch(c.query, "", "")
		if err != nil {
			t.Fatalf("search %q: %v", c.query, err)
		}
		found := false
		for _, v := range res.Vehicles {
			if v.Ref == c.wantReg {
				found = true
				if !strings.Contains(v.Subtitle, "callsign") {
					t.Errorf("search %q: subtitle %q does not explain the match", c.query, v.Subtitle)
				}
			}
		}
		if !found {
			t.Errorf("search %q did not find %s", c.query, c.wantReg)
		}
	}
}

// A vehicle listed twice in a dispatch export keeps both callsigns, and either
// one must find it.
func TestSearchFindsEitherOfTwoCallsigns(t *testing.T) {
	db := open(t)
	both := "Callsign 603, 622"
	if err := db.SaveVehicle("AE18FJZ", VehiclePatch{
		Make: ptr("Skoda"), Model: ptr("Superb"), Notes: &both,
	}); err != nil {
		t.Fatal(err)
	}
	add(t, db, Invoice{InvoiceNumber: "A", VehicleReg: "AE18FJZ",
		InvoiceDate: "2026-08-19", Brutto: 178.09})

	for _, q := range []string{"603", "622"} {
		res, err := db.GlobalSearch(q, "", "")
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		hit := false
		for _, v := range res.Vehicles {
			if v.Ref == "AE18FJZ" {
				hit = true
			}
		}
		if !hit {
			t.Errorf("callsign %q did not find AE18FJZ", q)
		}
	}
}

func ptr(s string) *string { return &s }
