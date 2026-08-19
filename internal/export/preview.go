package export

import (
	"encoding/csv"
	"os"
	"strings"

	"github.com/xuri/excelize/v2"
)

// previewRows caps how much of a sheet is sent to the browser. A year's
// workbook can hold thousands of rows; the preview exists to confirm the file
// is right before downloading, not to replace opening it.
const previewRows = 200

// SheetPreview is one sheet rendered as plain strings, exactly as Excel would
// display them — formatted values, not raw floats, so £1,234.50 does not
// arrive as 1234.5000000001.
type SheetPreview struct {
	Name      string     `json:"name"`
	Header    []string   `json:"header"`
	Rows      [][]string `json:"rows"`
	TotalRows int        `json:"total_rows"`
	Truncated bool       `json:"truncated"`
}

// Preview is a whole workbook, sheet by sheet.
type Preview struct {
	Name   string         `json:"name"`
	Sheets []SheetPreview `json:"sheets"`
}

// ReadPreview opens a generated file and returns its contents for display.
// Both formats the app writes are supported, so the popup behaves the same
// whichever one was generated.
func ReadPreview(path, name string) (*Preview, error) {
	if strings.HasSuffix(strings.ToLower(path), ".csv") {
		return readCSVPreview(path, name)
	}
	return readXLSXPreview(path, name)
}

func readCSVPreview(path, name string) (*Preview, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	all, err := r.ReadAll()
	if err != nil {
		return nil, err
	}

	p := SheetPreview{Name: "CSV", Header: []string{}, Rows: [][]string{}}
	if len(all) > 0 {
		p.Header = all[0]
		body := all[1:]
		p.TotalRows = len(body)
		if len(body) > previewRows {
			body = body[:previewRows]
			p.Truncated = true
		}
		p.Rows = body
	}
	return &Preview{Name: name, Sheets: []SheetPreview{p}}, nil
}

func readXLSXPreview(path, name string) (*Preview, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := &Preview{Name: name}
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			return nil, err
		}
		p := SheetPreview{Name: sheet, Header: []string{}, Rows: [][]string{}}
		if len(rows) > 0 {
			p.Header = rows[0]
			body := rows[1:]
			p.TotalRows = len(body)
			if len(body) > previewRows {
				body = body[:previewRows]
				p.Truncated = true
			}
			// Short rows are padded so the browser can render a rectangular
			// table without index checks on every cell.
			for _, r := range body {
				for len(r) < len(p.Header) {
					r = append(r, "")
				}
				p.Rows = append(p.Rows, r)
			}
		}
		out.Sheets = append(out.Sheets, p)
	}
	return out, nil
}
