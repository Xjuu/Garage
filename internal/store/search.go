package store

import (
	"database/sql"
	"strconv"
	"strings"
)

// Hit is one global-search result. Kind tells the UI which page to open.
type Hit struct {
	Kind     string  `json:"kind"` // invoice | vehicle | part | supplier
	Ref      string  `json:"ref"`  // id, registration, part number or supplier name
	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle"`
	Date     string  `json:"date"`
	Count    int     `json:"count"`
	Brutto   float64 `json:"brutto"`
}

// GlobalResults groups hits so the UI can label each section.
type GlobalResults struct {
	Invoices  []Hit `json:"invoices"`
	Vehicles  []Hit `json:"vehicles"`
	Parts     []Hit `json:"parts"`
	Suppliers []Hit `json:"suppliers"`
	Total     int   `json:"total"`
}

// perKind caps each group so the dropdown stays readable; the full list is
// always one click away on the relevant tab.
const perKind = 6

// GlobalSearch looks across invoices, vehicles, parts and suppliers at once.
// The date range narrows every group, so "brakes in July" works as expected
// rather than only filtering the invoice list.
func (s *Store) GlobalSearch(q, from, to string) (*GlobalResults, error) {
	q = strings.TrimSpace(q)
	out := &GlobalResults{
		Invoices: []Hit{}, Vehicles: []Hit{}, Parts: []Hit{}, Suppliers: []Hit{},
	}

	// A date range with no text is a legitimate query: "show me everything in
	// this period". Only bail when there is nothing to go on at all.
	if q == "" && from == "" && to == "" {
		return out, nil
	}
	like := "%" + strings.ToLower(q) + "%"

	dateClause, dateArgs := dateRange("i.invoice_date", from, to)
	var err error

	if out.Invoices, err = s.searchInvoices(q, like, dateClause, dateArgs); err != nil {
		return nil, err
	}
	if out.Vehicles, err = s.searchVehicles(q, like, dateClause, dateArgs); err != nil {
		return nil, err
	}
	// Registry-only cars are appended after the invoiced ones: a vehicle you
	// have actually spent money on is the more likely thing being looked for.
	if len(out.Vehicles) < perKind {
		onFleet, err := s.searchRegistry(q, like)
		if err != nil {
			return nil, err
		}
		for _, hit := range onFleet {
			if len(out.Vehicles) >= perKind {
				break
			}
			out.Vehicles = append(out.Vehicles, hit)
		}
	}
	if out.Parts, err = s.searchParts(q, like, dateClause, dateArgs); err != nil {
		return nil, err
	}
	if out.Suppliers, err = s.searchSuppliers(q, like, dateClause, dateArgs); err != nil {
		return nil, err
	}
	out.Total = len(out.Invoices) + len(out.Vehicles) + len(out.Parts) + len(out.Suppliers)
	return out, nil
}

func dateRange(col, from, to string) (string, []any) {
	var clauses []string
	var args []any
	if from != "" {
		clauses = append(clauses, col+" >= ?")
		args = append(args, from)
	}
	if to != "" {
		clauses = append(clauses, col+" <= ?")
		args = append(args, to)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

// textClause returns a predicate plus its args, or an always-true predicate
// when the query is empty so a date-only search still returns rows.
func textClause(q, like string, cols ...string) (string, []any) {
	if q == "" {
		return "1=1", nil
	}
	parts := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		parts = append(parts, "LOWER("+c+") LIKE ?")
		args = append(args, like)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func (s *Store) searchInvoices(q, like, dateClause string, dateArgs []any) ([]Hit, error) {
	text, textArgs := textClause(q, like,
		"i.supplier", "i.invoice_number", "i.vehicle_reg", "i.notes")

	sqlText := `
		SELECT i.id, i.supplier, i.invoice_number, i.vehicle_reg,
		       COALESCE(i.invoice_date,''), i.brutto
		FROM invoices i
		WHERE (` + text + ` OR EXISTS (
		        SELECT 1 FROM invoice_items it WHERE it.invoice_id = i.id
		          AND (LOWER(it.part_number) LIKE ? OR LOWER(it.description) LIKE ?)))` +
		dateClause + `
		ORDER BY COALESCE(NULLIF(i.invoice_date,''), i.created_at) DESC LIMIT ?`

	args := append([]any{}, textArgs...)
	if q == "" {
		// The EXISTS branch still needs its two placeholders filled.
		args = append(args, "%", "%")
	} else {
		args = append(args, like, like)
	}
	args = append(args, dateArgs...)
	args = append(args, perKind)

	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Hit{}
	for rows.Next() {
		var id int64
		var supplier, number, reg, date string
		var brutto float64
		if err := rows.Scan(&id, &supplier, &number, &reg, &date, &brutto); err != nil {
			return nil, err
		}
		out = append(out, Hit{
			Kind: "invoice", Ref: itoa(id),
			Title:    firstNonEmpty(supplier, "Invoice "+itoa(id)),
			Subtitle: strings.TrimSpace(number + " " + reg),
			Date:     date, Brutto: brutto, Count: 1,
		})
	}
	return out, rows.Err()
}

// searchRegistry finds vehicles that are on the fleet but have never been
// invoiced. Without it a car you own is unfindable until it first costs money,
// which is exactly backwards — "have we ever spent anything on this one?" is a
// question that needs an answer of zero, not silence.
func (s *Store) searchRegistry(q, like string) ([]Hit, error) {
	if q == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT v.registration, COALESCE(v.make,''), COALESCE(v.model,''),
		       COALESCE(v.notes,''), COALESCE(c.name,'')
		FROM vehicles v
		LEFT JOIN companies c ON c.id = v.company_id
		WHERE NOT EXISTS (SELECT 1 FROM invoices i WHERE i.vehicle_reg = v.registration)
		  AND (LOWER(v.registration) LIKE ? OR LOWER(v.make) LIKE ?
		       OR LOWER(v.model) LIKE ? OR LOWER(v.notes) LIKE ?)
		ORDER BY v.registration LIMIT ?`, like, like, like, like, perKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Hit{}
	for rows.Next() {
		var reg, mk, model, notes, company string
		if err := rows.Scan(&reg, &mk, &model, &notes, &company); err != nil {
			return nil, err
		}
		sub := strings.TrimSpace(mk + " " + model)
		if cs := callsignFrom(notes); cs != "" {
			sub = strings.TrimSpace("callsign " + cs + " · " + sub)
		}
		if company != "" {
			sub = strings.TrimSpace(sub + " · " + company)
		}
		out = append(out, Hit{
			Kind: "vehicle", Ref: reg, Title: reg,
			Subtitle: strings.TrimSpace(sub + " · no invoices yet"),
		})
	}
	return out, rows.Err()
}

func (s *Store) searchVehicles(q, like, dateClause string, dateArgs []any) ([]Hit, error) {
	text, textArgs := textClause(q, like, "i.vehicle_reg", "v.make", "v.model", "v.driver")

	sqlText := `
		SELECT i.vehicle_reg, COUNT(1), COALESCE(SUM(i.brutto),0),
		       COALESCE(MAX(NULLIF(i.invoice_date,'')),''),
		       COALESCE(MAX(v.make),''), COALESCE(MAX(v.model),''), COALESCE(MAX(c.name),'')
		FROM invoices i
		LEFT JOIN vehicles v  ON v.registration = i.vehicle_reg
		LEFT JOIN companies c ON c.id = v.company_id
		WHERE i.vehicle_reg <> '' AND ` + text + dateClause + `
		GROUP BY i.vehicle_reg ORDER BY SUM(i.brutto) DESC LIMIT ?`

	args := append([]any{}, textArgs...)
	args = append(args, dateArgs...)
	args = append(args, perKind)

	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Hit{}
	for rows.Next() {
		var reg, date, make_, model, company string
		var count int
		var brutto float64
		if err := rows.Scan(&reg, &count, &brutto, &date, &make_, &model, &company); err != nil {
			return nil, err
		}
		sub := strings.TrimSpace(make_ + " " + model)
		if company != "" {
			sub = strings.TrimSpace(sub + " · " + company)
		}
		out = append(out, Hit{
			Kind: "vehicle", Ref: reg, Title: reg,
			Subtitle: sub, Date: date, Count: count, Brutto: brutto,
		})
	}
	return out, rows.Err()
}

func (s *Store) searchParts(q, like, dateClause string, dateArgs []any) ([]Hit, error) {
	text, textArgs := textClause(q, like, "it.part_number", "it.description")

	sqlText := `
		SELECT it.part_number, MAX(it.description), COUNT(1),
		       COALESCE(SUM(it.netto),0), COALESCE(MAX(NULLIF(i.invoice_date,'')),'')
		FROM invoice_items it
		JOIN invoices i ON i.id = it.invoice_id
		WHERE it.part_number <> '' AND ` + text + dateClause + `
		GROUP BY it.part_number ORDER BY SUM(it.netto) DESC LIMIT ?`

	args := append([]any{}, textArgs...)
	args = append(args, dateArgs...)
	args = append(args, perKind)

	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Hit{}
	for rows.Next() {
		var part, date string
		var desc sql.NullString
		var count int
		var netto float64
		if err := rows.Scan(&part, &desc, &count, &netto, &date); err != nil {
			return nil, err
		}
		out = append(out, Hit{
			Kind: "part", Ref: part, Title: part,
			Subtitle: desc.String, Date: date, Count: count, Brutto: netto,
		})
	}
	return out, rows.Err()
}

func (s *Store) searchSuppliers(q, like, dateClause string, dateArgs []any) ([]Hit, error) {
	text, textArgs := textClause(q, like, "i.supplier")

	sqlText := `
		SELECT i.supplier, COUNT(1), COALESCE(SUM(i.brutto),0),
		       COALESCE(MAX(NULLIF(i.invoice_date,'')),'')
		FROM invoices i
		WHERE i.supplier <> '' AND ` + text + dateClause + `
		GROUP BY i.supplier ORDER BY SUM(i.brutto) DESC LIMIT ?`

	args := append([]any{}, textArgs...)
	args = append(args, dateArgs...)
	args = append(args, perKind)

	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Hit{}
	for rows.Next() {
		var supplier, date string
		var count int
		var brutto float64
		if err := rows.Scan(&supplier, &count, &brutto, &date); err != nil {
			return nil, err
		}
		out = append(out, Hit{
			Kind: "supplier", Ref: supplier, Title: supplier,
			Subtitle: plural(count, "invoice"), Date: date, Count: count, Brutto: brutto,
		})
	}
	return out, rows.Err()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return itoa(int64(n)) + " " + word + "s"
}
