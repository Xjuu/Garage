// Package export renders the stored invoices into a spreadsheet with three
// sheets: one row per invoice, one row per part line, and a monthly VAT summary.
package export

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/xuri/excelize/v2"

	"goldstar/internal/store"
)

const (
	sheetInvoices = "Invoices"
	sheetItems    = "Items"
	sheetSummary  = "Summary"
	sheetCharts   = "Charts"
)

// SortInvoices orders rows by vehicle registration, then by price with the
// dearest first. Grouping by plate is what makes a workbook readable when
// someone is checking what a particular car has cost; within a plate, the big
// numbers are the ones worth looking at, so they come first.
//
// General workshop stock has no registration, and sorts to the end rather than
// to the top, where an empty leading column would look like missing data.
func SortInvoices(invoices []store.Invoice) {
	sort.SliceStable(invoices, func(i, j int) bool {
		a, b := invoices[i], invoices[j]
		if (a.VehicleReg == "") != (b.VehicleReg == "") {
			return b.VehicleReg == ""
		}
		if a.VehicleReg != b.VehicleReg {
			return a.VehicleReg < b.VehicleReg
		}
		if a.Brutto != b.Brutto {
			return a.Brutto > b.Brutto
		}
		return a.InvoiceDate > b.InvoiceDate
	})
}

// itemRow is one line of the Items sheet, flattened so it can be sorted across
// invoice boundaries — a plate's parts belong together even when they arrived
// on different invoices.
type itemRow struct {
	inv  store.Invoice
	item store.Item
	reg  string
}

func flattenItems(invoices []store.Invoice) []itemRow {
	var rows []itemRow
	for _, inv := range invoices {
		for _, it := range inv.Items {
			// A line-level plate wins; otherwise the line belongs to the
			// vehicle named on the invoice as a whole.
			reg := it.VehicleReg
			if reg == "" {
				reg = inv.VehicleReg
			}
			rows = append(rows, itemRow{inv: inv, item: it, reg: reg})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.reg == "") != (b.reg == "") {
			return b.reg == ""
		}
		if a.reg != b.reg {
			return a.reg < b.reg
		}
		if a.item.Brutto != b.item.Brutto {
			return a.item.Brutto > b.item.Brutto
		}
		return a.inv.InvoiceDate > b.inv.InvoiceDate
	})
	return rows
}

var invoiceHeaders = []string{
	"Invoice date", "Supplier", "Invoice no.", "Vehicle reg", "Currency",
	"Netto", "VAT rate %", "VAT", "Brutto", "Parts", "Needs review", "Notes", "Source file",
}

var itemHeaders = []string{
	"Invoice date", "Supplier", "Invoice no.", "Vehicle reg", "Part number",
	"Description", "Qty", "Unit price", "Netto", "VAT rate %", "VAT", "Brutto",
}

var summaryHeaders = []string{"Month", "Invoices", "Netto", "VAT", "Brutto"}

// Write renders invoices to an .xlsx at path and returns the path written.
func Write(path string, invoices []store.Invoice) (string, error) {
	f := excelize.NewFile()
	defer f.Close()

	// Sort a copy: the caller's slice order is not ours to change, and the same
	// slice is sometimes rendered to CSV straight afterwards.
	sorted := make([]store.Invoice, len(invoices))
	copy(sorted, invoices)
	SortInvoices(sorted)
	invoices = sorted

	if err := f.SetSheetName("Sheet1", sheetInvoices); err != nil {
		return "", err
	}
	for _, name := range []string{sheetItems, sheetSummary, sheetCharts} {
		if _, err := f.NewSheet(name); err != nil {
			return "", err
		}
	}

	header, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"1F4E79"}},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	if err != nil {
		return "", err
	}
	money, err := f.NewStyle(&excelize.Style{NumFmt: 4}) // #,##0.00
	if err != nil {
		return "", err
	}
	review, err := f.NewStyle(&excelize.Style{
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFF2CC"}},
	})
	if err != nil {
		return "", err
	}

	if err := writeInvoices(f, invoices, header, money, review); err != nil {
		return "", err
	}
	if err := writeItems(f, invoices, header, money); err != nil {
		return "", err
	}
	if err := writeSummary(f, invoices, header, money); err != nil {
		return "", err
	}
	if err := writeCharts(f, invoices, header, money); err != nil {
		return "", err
	}

	f.SetActiveSheet(0)
	if err := f.SaveAs(path); err != nil {
		return "", fmt.Errorf("save %s: %w", path, err)
	}
	return path, nil
}

// DefaultPath is the dated workbook name written by the daily timer.
func DefaultPath(dir string, day time.Time) string {
	return filepath.Join(dir, fmt.Sprintf("goldstar-invoices-%s.xlsx", day.Format("2006-01-02")))
}

func writeHeader(f *excelize.File, sheet string, headers []string, style int, w *widths) error {
	for i, h := range headers {
		w.see(i+1, h)
		cell, err := excelize.CoordinatesToCellName(i+1, 1)
		if err != nil {
			return err
		}
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return err
		}
	}
	last, err := excelize.CoordinatesToCellName(len(headers), 1)
	if err != nil {
		return err
	}
	if err := f.SetCellStyle(sheet, "A1", last, style); err != nil {
		return err
	}
	if err := f.SetRowHeight(sheet, 1, 20); err != nil {
		return err
	}
	// Freeze the header so long invoice lists stay readable while scrolling.
	if err := f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, Split: false, XSplit: 0, YSplit: 1,
		TopLeftCell: "A2", ActivePane: "bottomLeft",
	}); err != nil {
		return err
	}
	return f.AutoFilter(sheet, fmt.Sprintf("A1:%s", last), nil)
}

func setRow(f *excelize.File, sheet string, row int, values []any, w *widths) error {
	for i, v := range values {
		w.see(i+1, v)
		cell, err := excelize.CoordinatesToCellName(i+1, row)
		if err != nil {
			return err
		}
		if err := f.SetCellValue(sheet, cell, v); err != nil {
			return err
		}
	}
	return nil
}

func styleCols(f *excelize.File, sheet string, style int, cols ...string) error {
	for _, c := range cols {
		if err := f.SetColStyle(sheet, c, style); err != nil {
			return err
		}
	}
	return nil
}

func writeInvoices(f *excelize.File, invoices []store.Invoice, header, money, review int) error {
	w := newWidths()
	if err := writeHeader(f, sheetInvoices, invoiceHeaders, header, w); err != nil {
		return err
	}
	for i, inv := range invoices {
		row := i + 2
		flag := ""
		if inv.NeedsReview {
			flag = "CHECK"
		}
		if err := setRow(f, sheetInvoices, row, []any{
			inv.InvoiceDate, inv.Supplier, inv.InvoiceNumber, inv.VehicleReg, inv.Currency,
			inv.Netto, inv.VATRate, inv.VATAmount, inv.Brutto, len(inv.Items),
			flag, inv.Notes, filepath.Base(inv.SourceFile),
		}, w); err != nil {
			return err
		}
		if inv.NeedsReview {
			if err := f.SetCellStyle(sheetInvoices, fmt.Sprintf("A%d", row), fmt.Sprintf("M%d", row), review); err != nil {
				return err
			}
		}
	}
	if err := styleCols(f, sheetInvoices, money, "F", "H", "I"); err != nil {
		return err
	}
	return w.apply(f, sheetInvoices)
}

func writeItems(f *excelize.File, invoices []store.Invoice, header, money int) error {
	w := newWidths()
	if err := writeHeader(f, sheetItems, itemHeaders, header, w); err != nil {
		return err
	}
	row := 2
	for _, r := range flattenItems(invoices) {
		if err := setRow(f, sheetItems, row, []any{
			r.inv.InvoiceDate, r.inv.Supplier, r.inv.InvoiceNumber, r.reg, r.item.PartNumber,
			r.item.Desc, r.item.Quantity, r.item.UnitPrice, r.item.Netto,
			r.item.VATRate, r.item.VATAmount, r.item.Brutto,
		}, w); err != nil {
			return err
		}
		row++
	}
	if err := styleCols(f, sheetItems, money, "H", "I", "K", "L"); err != nil {
		return err
	}
	return w.apply(f, sheetItems)
}

// writeSummary aggregates by calendar month, which is the shape a UK VAT
// return wants.
func writeSummary(f *excelize.File, invoices []store.Invoice, header, money int) error {
	w := newWidths()
	if err := writeHeader(f, sheetSummary, summaryHeaders, header, w); err != nil {
		return err
	}

	type agg struct {
		count              int
		netto, vat, brutto float64
	}
	months := map[string]*agg{}
	for _, inv := range invoices {
		key := "unknown"
		if len(inv.InvoiceDate) >= 7 {
			key = inv.InvoiceDate[:7]
		}
		a, ok := months[key]
		if !ok {
			a = &agg{}
			months[key] = a
		}
		a.count++
		a.netto += inv.Netto
		a.vat += inv.VATAmount
		a.brutto += inv.Brutto
	}

	keys := make([]string, 0, len(months))
	for k := range months {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))

	row := 2
	var tn, tv, tb float64
	var tc int
	for _, k := range keys {
		a := months[k]
		if err := setRow(f, sheetSummary, row, []any{k, a.count, a.netto, a.vat, a.brutto}, w); err != nil {
			return err
		}
		tn, tv, tb, tc = tn+a.netto, tv+a.vat, tb+a.brutto, tc+a.count
		row++
	}
	if err := setRow(f, sheetSummary, row, []any{"TOTAL", tc, tn, tv, tb}, w); err != nil {
		return err
	}
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	if err := f.SetCellStyle(sheetSummary, fmt.Sprintf("A%d", row), fmt.Sprintf("E%d", row), bold); err != nil {
		return err
	}
	if err := styleCols(f, sheetSummary, money, "C", "D", "E"); err != nil {
		return err
	}
	return w.apply(f, sheetSummary)
}
