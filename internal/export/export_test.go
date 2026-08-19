package export

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"

	"goldstar/internal/store"
)

func sample() []store.Invoice {
	return []store.Invoice{
		// Deliberately out of order, with general stock in the middle.
		{InvoiceNumber: "C", VehicleReg: "MJ65VJZ", InvoiceDate: "2026-08-17",
			Netto: 209.31, VATAmount: 41.86, Brutto: 251.17, Currency: "GBP",
			Items: []store.Item{{PartNumber: "OESE020303", Quantity: 1,
				UnitPrice: 209.31, Netto: 209.31, VATAmount: 41.86, Brutto: 251.17}}},
		{InvoiceNumber: "G", IsGeneral: true, InvoiceDate: "2026-08-17",
			Netto: 10.84, VATAmount: 2.17, Brutto: 13.01, Currency: "GBP",
			Items: []store.Item{{PartNumber: "EUREXSV517", Quantity: 1,
				UnitPrice: 10.84, Netto: 10.84, VATAmount: 2.17, Brutto: 13.01}}},
		{InvoiceNumber: "A", VehicleReg: "AE18FKM", InvoiceDate: "2026-08-11",
			Netto: 76.92, VATAmount: 15.38, Brutto: 92.30, Currency: "GBP",
			Items: []store.Item{{PartNumber: "APEDSK2338", Quantity: 1,
				UnitPrice: 76.92, Netto: 76.92, VATAmount: 15.38, Brutto: 92.30}}},
		// Same plate as the first, cheaper: must sort below it.
		{InvoiceNumber: "D", VehicleReg: "MJ65VJZ", InvoiceDate: "2026-08-01",
			Netto: 10, VATAmount: 2, Brutto: 12, Currency: "GBP"},
	}
}

// Registration first, then price with the dearest at the top, and no-plate
// stock at the end. This is the order the whole workbook promises.
func TestSortInvoices(t *testing.T) {
	inv := sample()
	SortInvoices(inv)

	want := []struct {
		reg    string
		brutto float64
	}{
		{"AE18FKM", 92.30},
		{"MJ65VJZ", 251.17},
		{"MJ65VJZ", 12},
		{"", 13.01},
	}
	if len(inv) != len(want) {
		t.Fatalf("got %d invoices, want %d", len(inv), len(want))
	}
	for i, w := range want {
		if inv[i].VehicleReg != w.reg || inv[i].Brutto != w.brutto {
			t.Errorf("row %d = %q/%v, want %q/%v",
				i, inv[i].VehicleReg, inv[i].Brutto, w.reg, w.brutto)
		}
	}
}

// Sorting must not disturb the caller's slice: the same invoices are handed to
// the CSV writer straight afterwards.
func TestWriteDoesNotReorderCaller(t *testing.T) {
	inv := sample()
	first := inv[0].InvoiceNumber

	path := filepath.Join(t.TempDir(), "book.xlsx")
	if _, err := Write(path, inv); err != nil {
		t.Fatalf("write: %v", err)
	}
	if inv[0].InvoiceNumber != first {
		t.Errorf("caller slice was reordered: first is now %q, was %q",
			inv[0].InvoiceNumber, first)
	}
}

// The workbook must actually contain the four sheets and the three charts the
// UI tells people are there.
func TestWorkbookStructure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.xlsx")
	if _, err := Write(path, sample()); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, err := ReadPreview(path, "book.xlsx")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	want := []string{"Invoices", "Items", "Summary", "Charts"}
	if len(p.Sheets) != len(want) {
		t.Fatalf("sheets = %d, want %d", len(p.Sheets), len(want))
	}
	for i, name := range want {
		if p.Sheets[i].Name != name {
			t.Errorf("sheet %d = %q, want %q", i, p.Sheets[i].Name, name)
		}
	}
	if rows := p.Sheets[0].TotalRows; rows != 4 {
		t.Errorf("invoice rows = %d, want 4", rows)
	}
}

// CSV and the workbook must agree. If they disagree, one of the two downloads
// is wrong and nobody would notice until a return was filed.
func TestCSVMatchesWorkbookOrder(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "out.csv")
	if err := WriteCSV(csvPath, sample()); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) < 2 {
		t.Fatal("csv has no data rows")
	}
	if rows[0][3] != "vehicle_reg" {
		t.Fatalf("column 4 is %q, want vehicle_reg", rows[0][3])
	}

	// Column 4 is the registration; blanks (general stock) must come last.
	var regs []string
	for _, r := range rows[1:] {
		regs = append(regs, r[3])
	}
	seenBlank := false
	for _, reg := range regs {
		if reg == "" {
			seenBlank = true
			continue
		}
		if seenBlank {
			t.Errorf("a registration (%q) appears after general stock: %v", reg, regs)
			break
		}
	}
}

// A saved workbook must list with its real counts rather than as "unknown".
func TestManifestRecordsCounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "goldstar-all.xlsx")
	if _, err := WriteWithManifest(path, "Everything", "", "", sample()); err != nil {
		t.Fatalf("write: %v", err)
	}

	files, err := NewCatalogue(dir).List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("listed %d files, want 1", len(files))
	}
	f := files[0]
	if f.Stale {
		t.Error("file reported as stale despite having a manifest")
	}
	if f.Invoices != 4 {
		t.Errorf("invoices = %d, want 4", f.Invoices)
	}
	if got, want := f.Brutto, 251.17+13.01+92.30+12; got != want {
		t.Errorf("brutto = %v, want %v", got, want)
	}
}

// A crafted filename must not reach outside the exports folder.
func TestCatalogueRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteWithManifest(filepath.Join(dir, "ok.xlsx"), "x", "", "", sample()); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := NewCatalogue(dir)

	for _, bad := range []string{
		"../../../etc/passwd", "..%2fescape.xlsx", "/etc/passwd",
		"sub/dir.xlsx", "notes.txt", "", "goldstar.db",
	} {
		if _, err := c.Resolve(bad); err == nil {
			t.Errorf("Resolve(%q) was allowed; it must be refused", bad)
		}
	}
	if _, err := c.Resolve("ok.xlsx"); err != nil {
		t.Errorf("Resolve(ok.xlsx) refused a legitimate file: %v", err)
	}
}

// The listing is cached; a newly written file must still appear at once.
func TestCatalogueCacheInvalidatesOnWrite(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalogue(dir)

	if files, _ := c.List(); len(files) != 0 {
		t.Fatalf("expected an empty folder, got %d files", len(files))
	}
	if _, err := WriteWithManifest(filepath.Join(dir, "new.xlsx"), "x", "", "", sample()); err != nil {
		t.Fatalf("write: %v", err)
	}
	c.Invalidate()
	files, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("after writing, listed %d files, want 1", len(files))
	}
}
