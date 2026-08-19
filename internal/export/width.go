package export

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// Column sizing bounds, in Excel's character-width units. The ceiling stops a
// long part description from stretching a column off the screen; the floor
// keeps narrow numeric columns from crowding their header.
const (
	minColWidth = 8.0
	maxColWidth = 46.0
	colPadding  = 2.6
)

// widths records the longest rendered value seen in each column so a sheet can
// be sized to its actual contents. Excel stores column widths but computes
// "autofit" only in the UI, so a file written by a library has to do the
// measuring itself.
type widths struct{ max map[int]int }

func newWidths() *widths { return &widths{max: make(map[int]int)} }

// see records a value destined for a 1-based column.
func (w *widths) see(col int, v any) {
	if n := displayLen(v); n > w.max[col] {
		w.max[col] = n
	}
}

// apply sets every measured column's width on the sheet.
func (w *widths) apply(f *excelize.File, sheet string) error {
	for col, n := range w.max {
		name, err := excelize.ColumnNumberToName(col)
		if err != nil {
			return err
		}
		width := float64(n) + colPadding
		if width < minColWidth {
			width = minColWidth
		}
		if width > maxColWidth {
			width = maxColWidth
		}
		if err := f.SetColWidth(sheet, name, name, width); err != nil {
			return err
		}
	}
	return nil
}

// displayLen estimates how wide a value renders once the sheet's number
// formats are applied — floats show as #,##0.00, so the separators and the two
// decimals both count.
func displayLen(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case string:
		return longestLine(t)
	case int:
		return len(withThousands(strconv.Itoa(t)))
	case int64:
		return len(withThousands(strconv.FormatInt(t, 10)))
	case float64:
		return len(withThousands(strconv.FormatFloat(t, 'f', 2, 64)))
	default:
		return longestLine(fmt.Sprint(v))
	}
}

// longestLine measures in runes, not bytes, so a supplier name with accented
// characters is not over-measured.
func longestLine(s string) int {
	longest := 0
	for _, line := range strings.Split(s, "\n") {
		if n := utf8.RuneCountInString(line); n > longest {
			longest = n
		}
	}
	return longest
}

// withThousands inserts group separators into a plain decimal string so the
// measurement matches what Excel will draw.
func withThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	intPart, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	out := b.String()
	if hasFrac {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}
