package store

import (
	"fmt"
	"strings"
	"time"
)

// DayPoint is one day on the spend chart.
type DayPoint struct {
	Date     string  `json:"date"`
	Invoices int     `json:"invoices"`
	Netto    float64 `json:"netto"`
	VAT      float64 `json:"vat"`
	Brutto   float64 `json:"brutto"`
}

// DayLine is one purchased line, for the scrollable day-by-day breakdown.
type DayLine struct {
	InvoiceID  int64   `json:"invoice_id"`
	Supplier   string  `json:"supplier"`
	PartNumber string  `json:"part_number"`
	Desc       string  `json:"description"`
	VehicleReg string  `json:"vehicle_reg"`
	IsGeneral  bool    `json:"is_general"`
	Quantity   float64 `json:"quantity"`
	UnitPrice  float64 `json:"unit_price"`
	Netto      float64 `json:"netto"`
	VAT        float64 `json:"vat"`
	Brutto     float64 `json:"brutto"`
}

// DayDetail groups a day's lines so the UI can render one block per day.
type DayDetail struct {
	Date   string    `json:"date"`
	Netto  float64   `json:"netto"`
	VAT    float64   `json:"vat"`
	Brutto float64   `json:"brutto"`
	Lines  []DayLine `json:"lines"`
}

// Trends is a spend summary for a window, next to the window immediately
// before it. The comparison is what makes the number mean anything: £900 is
// only high or low relative to the £600 before it.
type Trends struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Days     int    `json:"days"`
	Label    string `json:"label"`
	Scope    string `json:"scope"`
	Vehicle  string `json:"vehicle"`
	Invoices int    `json:"invoices"`

	Netto  float64 `json:"netto"`
	VAT    float64 `json:"vat"`
	Brutto float64 `json:"brutto"`

	PrevFrom   string  `json:"prev_from"`
	PrevTo     string  `json:"prev_to"`
	PrevBrutto float64 `json:"prev_brutto"`
	ChangePct  float64 `json:"change_pct"`
	// HasPrev is false when nothing was recorded in the preceding window, in
	// which case a percentage change would be meaningless rather than infinite.
	HasPrev bool `json:"has_prev"`

	AvgPerDay float64     `json:"avg_per_day"`
	Series    []DayPoint  `json:"series"`
	Detail    []DayDetail `json:"detail"`
}

// TrendQuery selects the window and optionally narrows to one vehicle or to
// general workshop stock.
type TrendQuery struct {
	Period   string // 7d | 30d | 90d | 365d | custom
	From, To string // used when Period is "custom"
	Vehicle  string
	Scope    string // "", "general", "vehicle"
}

// resolve turns a period into concrete dates, plus the preceding window of the
// same length for comparison.
func (q TrendQuery) resolve(today time.Time) (from, to, prevFrom, prevTo string, days int, label string, err error) {
	windows := map[string]struct {
		days  int
		label string
	}{
		"7d":   {7, "Last 7 days"},
		"30d":  {30, "Last 30 days"},
		"90d":  {90, "Last 90 days"},
		"365d": {365, "Last 365 days"},
	}

	if w, ok := windows[q.Period]; ok {
		end := today
		start := end.AddDate(0, 0, -(w.days - 1))
		prevEnd := start.AddDate(0, 0, -1)
		prevStart := prevEnd.AddDate(0, 0, -(w.days - 1))
		return iso(start), iso(end), iso(prevStart), iso(prevEnd), w.days, w.label, nil
	}

	if q.From == "" || q.To == "" {
		return "", "", "", "", 0, "", fmt.Errorf("a custom period needs both a start and an end date")
	}
	start, e1 := time.Parse("2006-01-02", q.From)
	end, e2 := time.Parse("2006-01-02", q.To)
	if e1 != nil || e2 != nil {
		return "", "", "", "", 0, "", fmt.Errorf("dates must be YYYY-MM-DD")
	}
	if end.Before(start) {
		start, end = end, start
	}
	n := int(end.Sub(start).Hours()/24) + 1
	prevEnd := start.AddDate(0, 0, -1)
	prevStart := prevEnd.AddDate(0, 0, -(n - 1))
	return iso(start), iso(end), iso(prevStart), iso(prevEnd), n,
		fmt.Sprintf("%s to %s", iso(start), iso(end)), nil
}

func iso(t time.Time) string { return t.Format("2006-01-02") }

// scopeClause narrows to one vehicle or to general stock.
func (q TrendQuery) scopeClause() (string, []any) {
	var clauses []string
	var args []any

	if reg := NormalizeReg(q.Vehicle); reg != "" {
		clauses = append(clauses, "i.vehicle_reg = ?")
		args = append(args, reg)
	}
	switch q.Scope {
	case "general":
		clauses = append(clauses, "i.is_general = 1")
	case "vehicle":
		clauses = append(clauses, "i.is_general = 0 AND i.vehicle_reg <> ''")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

// Spending builds the trend summary, the daily series and the day-by-day
// breakdown in one pass.
func (s *Store) Spending(q TrendQuery) (*Trends, error) {
	from, to, prevFrom, prevTo, days, label, err := q.resolve(time.Now())
	if err != nil {
		return nil, err
	}
	scope, scopeArgs := q.scopeClause()

	t := &Trends{
		From: from, To: to, Days: days, Label: label,
		Scope: q.Scope, Vehicle: NormalizeReg(q.Vehicle),
		PrevFrom: prevFrom, PrevTo: prevTo,
		Series: []DayPoint{}, Detail: []DayDetail{},
	}

	args := append([]any{from, to}, scopeArgs...)
	err = s.db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(i.netto),0), COALESCE(SUM(i.vat_amount),0),
		       COALESCE(SUM(i.brutto),0)
		FROM invoices i
		WHERE i.invoice_date >= ? AND i.invoice_date <= ?`+scope, args...).
		Scan(&t.Invoices, &t.Netto, &t.VAT, &t.Brutto)
	if err != nil {
		return nil, err
	}

	var prevCount int
	prevArgs := append([]any{prevFrom, prevTo}, scopeArgs...)
	err = s.db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(i.brutto),0)
		FROM invoices i
		WHERE i.invoice_date >= ? AND i.invoice_date <= ?`+scope, prevArgs...).
		Scan(&prevCount, &t.PrevBrutto)
	if err != nil {
		return nil, err
	}
	t.HasPrev = prevCount > 0
	if t.PrevBrutto > 0 {
		t.ChangePct = (t.Brutto - t.PrevBrutto) / t.PrevBrutto * 100
	}
	if days > 0 {
		t.AvgPerDay = t.Brutto / float64(days)
	}

	if t.Series, err = s.dailySeries(from, to, scope, scopeArgs); err != nil {
		return nil, err
	}
	if t.Detail, err = s.dailyDetail(from, to, scope, scopeArgs); err != nil {
		return nil, err
	}
	return t, nil
}

// dailySeries returns one row per day that had spend. Days with nothing are
// left out here and filled in by the caller when drawing the chart, which
// keeps the payload small over a 365-day window.
func (s *Store) dailySeries(from, to, scope string, scopeArgs []any) ([]DayPoint, error) {
	args := append([]any{from, to}, scopeArgs...)
	rows, err := s.db.Query(`
		SELECT i.invoice_date, COUNT(1),
		       COALESCE(SUM(i.netto),0), COALESCE(SUM(i.vat_amount),0), COALESCE(SUM(i.brutto),0)
		FROM invoices i
		WHERE i.invoice_date >= ? AND i.invoice_date <= ?`+scope+`
		GROUP BY i.invoice_date ORDER BY i.invoice_date`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DayPoint{}
	for rows.Next() {
		var d DayPoint
		if err := rows.Scan(&d.Date, &d.Invoices, &d.Netto, &d.VAT, &d.Brutto); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// dailyDetail lists every line bought on every day in the window, newest first,
// so the operator can scroll back through what was actually purchased and at
// what price.
func (s *Store) dailyDetail(from, to, scope string, scopeArgs []any) ([]DayDetail, error) {
	args := append([]any{from, to}, scopeArgs...)
	rows, err := s.db.Query(`
		SELECT i.invoice_date, i.id, i.supplier, i.vehicle_reg, i.is_general,
		       it.part_number, it.description,
		       it.quantity, it.unit_price, it.netto, it.vat_amount, it.brutto
		FROM invoices i
		JOIN invoice_items it ON it.invoice_id = i.id
		WHERE i.invoice_date >= ? AND i.invoice_date <= ?`+scope+`
		ORDER BY i.invoice_date DESC, i.id DESC, it.line_no`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DayDetail{}
	var current *DayDetail

	for rows.Next() {
		var date string
		var line DayLine
		var general int
		if err := rows.Scan(&date, &line.InvoiceID, &line.Supplier, &line.VehicleReg, &general,
			&line.PartNumber, &line.Desc, &line.Quantity, &line.UnitPrice,
			&line.Netto, &line.VAT, &line.Brutto); err != nil {
			return nil, err
		}
		line.IsGeneral = general != 0

		if current == nil || current.Date != date {
			out = append(out, DayDetail{Date: date, Lines: []DayLine{}})
			current = &out[len(out)-1]
		}
		current.Lines = append(current.Lines, line)
		current.Netto += line.Netto
		current.VAT += line.VAT
		current.Brutto += line.Brutto
	}
	return out, rows.Err()
}

// MonthToDate is the current calendar month's spend next to the same number of
// days in the month before. Comparing whole-month-so-far against a full
// previous month would always look like a fall, which is the kind of chart
// that trains people to ignore it.
type MonthToDate struct {
	Month       string  `json:"month"`
	Netto       float64 `json:"netto"`
	VAT         float64 `json:"vat"`
	Brutto      float64 `json:"brutto"`
	Invoices    int     `json:"invoices"`
	Purchases   float64 `json:"purchases"`
	Credits     float64 `json:"credits"`
	CreditCount int     `json:"credit_count"`
	PrevBrutto  float64 `json:"prev_brutto"`
	PrevLabel   string  `json:"prev_label"`
	HasPrev     bool    `json:"has_prev"`
	ChangePct   float64 `json:"change_pct"`
	DayOfMonth  int     `json:"day_of_month"`
}

func (s *Store) ThisMonth(now time.Time) (*MonthToDate, error) {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	day := now.Day()

	out := &MonthToDate{Month: start.Format("January 2006"), DayOfMonth: day}
	// Purchases excludes a returned invoice's amount entirely, same reasoning
	// as Overview's own Purchases figure — the money came back, so it was
	// never really this month's spend.
	err := s.db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(netto),0), COALESCE(SUM(vat_amount),0), COALESCE(SUM(brutto),0),
		       COALESCE(SUM(CASE WHEN brutto >= 0 AND returned = 0 THEN brutto ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN brutto <  0 THEN brutto ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN brutto <  0 THEN 1 ELSE 0 END),0)
		FROM invoices WHERE invoice_date >= ? AND invoice_date <= ?`,
		iso(start), iso(now)).
		Scan(&out.Invoices, &out.Netto, &out.VAT, &out.Brutto,
			&out.Purchases, &out.Credits, &out.CreditCount)
	if err != nil {
		return nil, err
	}

	prevStart := start.AddDate(0, -1, 0)
	// Clamp to the previous month's length: "the 31st" does not exist in every
	// month, and AddDate would silently roll into the following one.
	prevEnd := prevStart.AddDate(0, 0, day-1)
	if lastDay := prevStart.AddDate(0, 1, -1); prevEnd.After(lastDay) {
		prevEnd = lastDay
	}
	out.PrevLabel = prevStart.Format("January")

	var prevCount int
	if err := s.db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(brutto),0)
		FROM invoices WHERE invoice_date >= ? AND invoice_date <= ?`,
		iso(prevStart), iso(prevEnd)).Scan(&prevCount, &out.PrevBrutto); err != nil {
		return nil, err
	}
	out.HasPrev = prevCount > 0
	if out.PrevBrutto > 0 {
		out.ChangePct = (out.Brutto - out.PrevBrutto) / out.PrevBrutto * 100
	}
	return out, nil
}
