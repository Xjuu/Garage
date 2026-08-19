package export

import (
	"fmt"
	"sort"

	"github.com/xuri/excelize/v2"

	"goldstar/internal/store"
)

// chartCap limits how many bars a chart draws. Beyond roughly this many the
// labels overlap into an unreadable smear, so the long tail is folded into a
// single "everything else" bar rather than silently dropped — a chart that
// quietly omits spend would misrepresent the total.
const chartCap = 12

// writeCharts builds a sheet of real Excel charts. They are native chart
// objects, not pictures, so they stay live: filter or edit the data and the
// charts redraw in Excel or LibreOffice.
func writeCharts(f *excelize.File, invoices []store.Invoice, header, money int) error {
	byVehicle := totalsBy(invoices, func(inv store.Invoice) string {
		if inv.VehicleReg == "" {
			return "General stock"
		}
		return inv.VehicleReg
	})
	bySupplier := totalsBy(invoices, func(inv store.Invoice) string {
		if inv.Supplier == "" {
			return "Unknown"
		}
		return inv.Supplier
	})
	byMonth := totalsBy(invoices, func(inv store.Invoice) string {
		if len(inv.InvoiceDate) >= 7 {
			return inv.InvoiceDate[:7]
		}
		return "unknown"
	})

	vehicles := topN(byVehicle, chartCap)
	suppliers := topN(bySupplier, chartCap)
	// Months read left to right in time order; ranking them by size would make
	// the trend line meaningless.
	months := byMonth
	sort.Slice(months, func(i, j int) bool { return months[i].Key < months[j].Key })

	// The three data blocks sit in columns A:B, D:E and G:H of the same sheet.
	// Charts are anchored below them so the numbers behind each picture stay
	// visible and checkable.
	blocks := []struct {
		col   string
		title string
		rows  []keyTotal
	}{
		{"A", "Spend by vehicle", vehicles},
		{"D", "Spend by supplier", suppliers},
		{"G", "Spend by month", months},
	}

	w := newWidths()
	for _, b := range blocks {
		if err := writeBlock(f, b.col, b.title, b.rows, header, money, w); err != nil {
			return err
		}
	}
	if err := w.apply(f, sheetCharts); err != nil {
		return err
	}

	maxRows := 0
	for _, b := range blocks {
		if len(b.rows) > maxRows {
			maxRows = len(b.rows)
		}
	}
	// Leave a two-row gap between the last data row and the first chart.
	anchorRow := maxRows + 4

	charts := []struct {
		cell    string
		title   string
		col     string
		rows    int
		kind    excelize.ChartType
		reverse bool
	}{
		{fmt.Sprintf("A%d", anchorRow), "Spend by vehicle (inc VAT)", "A", len(vehicles), excelize.BarStacked, true},
		{fmt.Sprintf("J%d", anchorRow), "Spend by supplier (inc VAT)", "D", len(suppliers), excelize.BarStacked, true},
		{fmt.Sprintf("A%d", anchorRow+20), "Spend by month (inc VAT)", "G", len(months), excelize.Col, false},
	}

	for _, c := range charts {
		if c.rows == 0 {
			continue
		}
		valueCol := nextCol(c.col)
		if err := f.AddChart(sheetCharts, c.cell, &excelize.Chart{
			Type: c.kind,
			Series: []excelize.ChartSeries{{
				Name:       "Gross",
				Categories: fmt.Sprintf("%s!$%s$3:$%s$%d", sheetCharts, c.col, c.col, c.rows+2),
				Values:     fmt.Sprintf("%s!$%s$3:$%s$%d", sheetCharts, valueCol, valueCol, c.rows+2),
			}},
			Title:  excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: c.title}}},
			Legend: excelize.ChartLegend{Position: "none"},
			// A horizontal bar chart lists categories bottom-up by default,
			// which puts the largest at the bottom; reversing reads as a
			// ranking, largest first.
			YAxis: excelize.ChartAxis{ReverseOrder: c.reverse},
			Dimension: excelize.ChartDimension{
				Width:  480,
				Height: 300,
			},
		}); err != nil {
			return fmt.Errorf("chart %q: %w", c.title, err)
		}
	}
	return nil
}

type keyTotal struct {
	Key    string
	Count  int
	Brutto float64
}

func totalsBy(invoices []store.Invoice, key func(store.Invoice) string) []keyTotal {
	index := map[string]*keyTotal{}
	for _, inv := range invoices {
		k := key(inv)
		t, ok := index[k]
		if !ok {
			t = &keyTotal{Key: k}
			index[k] = t
		}
		t.Count++
		t.Brutto += inv.Brutto
	}
	out := make([]keyTotal, 0, len(index))
	for _, t := range index {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Brutto != out[j].Brutto {
			return out[i].Brutto > out[j].Brutto
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// topN keeps the biggest n and rolls the remainder into one labelled bar, so
// the chart still adds up to the real total.
func topN(rows []keyTotal, n int) []keyTotal {
	if len(rows) <= n {
		return rows
	}
	head := append([]keyTotal{}, rows[:n]...)
	rest := keyTotal{Key: fmt.Sprintf("Other (%d)", len(rows)-n)}
	for _, r := range rows[n:] {
		rest.Count += r.Count
		rest.Brutto += r.Brutto
	}
	return append(head, rest)
}

// writeBlock lays out one titled two-column table starting at the given column.
func writeBlock(f *excelize.File, col, title string, rows []keyTotal, header, money int, w *widths) error {
	c1, err := excelize.ColumnNameToNumber(col)
	if err != nil {
		return err
	}
	c2 := c1 + 1

	titleCell, _ := excelize.CoordinatesToCellName(c1, 1)
	if err := f.SetCellValue(sheetCharts, titleCell, title); err != nil {
		return err
	}
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 12}})
	if err != nil {
		return err
	}
	if err := f.SetCellStyle(sheetCharts, titleCell, titleCell, bold); err != nil {
		return err
	}

	for i, h := range []string{"Name", "Gross"} {
		cell, _ := excelize.CoordinatesToCellName(c1+i, 2)
		if err := f.SetCellValue(sheetCharts, cell, h); err != nil {
			return err
		}
		if err := f.SetCellStyle(sheetCharts, cell, cell, header); err != nil {
			return err
		}
		w.see(c1+i, h)
	}

	for i, r := range rows {
		row := i + 3
		nameCell, _ := excelize.CoordinatesToCellName(c1, row)
		valCell, _ := excelize.CoordinatesToCellName(c2, row)
		if err := f.SetCellValue(sheetCharts, nameCell, r.Key); err != nil {
			return err
		}
		if err := f.SetCellValue(sheetCharts, valCell, r.Brutto); err != nil {
			return err
		}
		if err := f.SetCellStyle(sheetCharts, valCell, valCell, money); err != nil {
			return err
		}
		w.see(c1, r.Key)
		w.see(c2, r.Brutto)
	}
	return nil
}

func nextCol(col string) string {
	n, err := excelize.ColumnNameToNumber(col)
	if err != nil {
		return col
	}
	name, err := excelize.ColumnNumberToName(n + 1)
	if err != nil {
		return col
	}
	return name
}
