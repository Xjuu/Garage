package export

import (
	"encoding/csv"
	"os"
	"strconv"

	"goldstar/internal/store"
)

// CSVHeader is the column order, shared with the streaming download so both
// paths produce identical files.
var CSVHeader = []string{
	"invoice_date", "supplier", "invoice_number", "vehicle_reg", "part_number",
	"description", "quantity", "unit_price", "netto", "vat_rate", "vat", "brutto", "currency",
}

// CSVRows flattens invoices to one row per line item, in the same
// registration-then-price order as the workbook so the two agree.
func CSVRows(invoices []store.Invoice) [][]string {
	sorted := make([]store.Invoice, len(invoices))
	copy(sorted, invoices)
	SortInvoices(sorted)

	num := func(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

	rows := [][]string{}
	for _, r := range flattenItems(sorted) {
		rows = append(rows, []string{
			r.inv.InvoiceDate, r.inv.Supplier, r.inv.InvoiceNumber, r.reg,
			r.item.PartNumber, r.item.Desc,
			strconv.FormatFloat(r.item.Quantity, 'f', -1, 64),
			num(r.item.UnitPrice), num(r.item.Netto), num(r.item.VATRate),
			num(r.item.VATAmount), num(r.item.Brutto), r.inv.Currency,
		})
	}
	return rows
}

// WriteCSV saves a CSV alongside the workbooks so both formats appear in the
// same saved-files list.
func WriteCSV(path string, invoices []store.Invoice) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.Write(CSVHeader); err != nil {
		return err
	}
	if err := w.WriteAll(CSVRows(invoices)); err != nil {
		return err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return f.Close()
}
