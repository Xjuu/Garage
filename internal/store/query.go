package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// Query describes a filtered, sorted, paginated invoice search.
type Query struct {
	Text       string
	From, To   string
	Supplier   string
	VehicleReg string
	Review     string // "", "yes", "no"
	Scope      string // "", "general" (workshop stock), "vehicle"
	Sort       string
	Dir        string
	Page       int
	PerPage    int
}

// sortColumns whitelists ORDER BY targets. User input never reaches SQL text.
var sortColumns = map[string]string{
	"date":     "COALESCE(NULLIF(i.invoice_date,''), i.created_at)",
	"supplier": "LOWER(i.supplier)",
	"reg":      "i.vehicle_reg",
	"netto":    "i.netto",
	"vat":      "i.vat_amount",
	"brutto":   "i.brutto",
	"added":    "i.created_at",
}

func (q *Query) normalize() {
	if _, ok := sortColumns[q.Sort]; !ok {
		q.Sort = "date"
	}
	if !strings.EqualFold(q.Dir, "asc") {
		q.Dir = "DESC"
	} else {
		q.Dir = "ASC"
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage < 1 || q.PerPage > 500 {
		q.PerPage = 50
	}
}

// where builds the shared predicate for both the count and the page query.
func (q *Query) where() (string, []any) {
	var clauses []string
	var args []any

	if t := strings.TrimSpace(q.Text); t != "" {
		like := "%" + strings.ToLower(t) + "%"
		clauses = append(clauses, `(
			LOWER(i.supplier) LIKE ? OR LOWER(i.invoice_number) LIKE ?
			OR LOWER(i.vehicle_reg) LIKE ? OR LOWER(i.notes) LIKE ?
			OR EXISTS (SELECT 1 FROM invoice_items it WHERE it.invoice_id = i.id
			           AND (LOWER(it.part_number) LIKE ? OR LOWER(it.description) LIKE ?))
		)`)
		args = append(args, like, like, like, like, like, like)
	}
	if q.From != "" {
		clauses = append(clauses, "i.invoice_date >= ?")
		args = append(args, q.From)
	}
	if q.To != "" {
		clauses = append(clauses, "i.invoice_date <= ?")
		args = append(args, q.To)
	}
	if q.Supplier != "" {
		clauses = append(clauses, "i.supplier = ?")
		args = append(args, q.Supplier)
	}
	if q.VehicleReg != "" {
		clauses = append(clauses, "i.vehicle_reg = ?")
		args = append(args, q.VehicleReg)
	}
	switch q.Scope {
	case "general":
		clauses = append(clauses, "i.is_general = 1")
	case "vehicle":
		clauses = append(clauses, "i.is_general = 0 AND i.vehicle_reg <> ''")
	}
	switch q.Review {
	case "yes":
		clauses = append(clauses, "i.needs_review = 1")
	case "no":
		clauses = append(clauses, "i.needs_review = 0")
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// Page is one screen of results plus the totals for the whole filtered set,
// so the UI can show "showing 50 of 214" and sum the filtered selection.
type Page struct {
	Invoices []Invoice
	Total    int
	Netto    float64
	VAT      float64
	Brutto   float64
}

// Search returns a filtered page of invoices with their line items.
func (s *Store) Search(q Query) (*Page, error) {
	q.normalize()
	where, args := q.where()

	out := &Page{Invoices: []Invoice{}}
	err := s.db.QueryRow(
		`SELECT COUNT(1), COALESCE(SUM(i.netto),0), COALESCE(SUM(i.vat_amount),0), COALESCE(SUM(i.brutto),0)
		 FROM invoices i`+where, args...).
		Scan(&out.Total, &out.Netto, &out.VAT, &out.Brutto)
	if err != nil {
		return nil, fmt.Errorf("count: %w", err)
	}

	sql := `SELECT i.id, i.file_sha256, i.source_file, i.mail_uid, i.mail_subject, i.mail_from,
	               i.mail_date, i.supplier, i.invoice_number, i.invoice_date, i.vehicle_reg,
	               i.currency, i.netto, i.vat_amount, i.vat_rate, i.brutto,
	               i.needs_review, i.is_general, i.notes, i.created_at
	        FROM invoices i` + where +
		fmt.Sprintf(" ORDER BY %s %s, i.id DESC LIMIT ? OFFSET ?", sortColumns[q.Sort], q.Dir)

	pageArgs := append(append([]any{}, args...), q.PerPage, (q.Page-1)*q.PerPage)
	rows, err := s.db.Query(sql, pageArgs...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		v, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out.Invoices = append(out.Invoices, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out.Invoices {
		items, err := s.itemsFor(out.Invoices[i].ID)
		if err != nil {
			return nil, err
		}
		out.Invoices[i].Items = items
	}
	return out, nil
}

// AllMatching returns every invoice matching the filter, ignoring pagination.
// Used by the exporters so a download reflects what is on screen.
func (s *Store) AllMatching(q Query) ([]Invoice, error) {
	q.Page, q.PerPage = 1, 500
	var all []Invoice
	for {
		page, err := s.Search(q)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Invoices...)
		if len(all) >= page.Total || len(page.Invoices) == 0 {
			return all, nil
		}
		q.Page++
	}
}

type scanner interface {
	Scan(dest ...any) error
}

func scanInvoice(sc scanner) (*Invoice, error) {
	var v Invoice
	var review, general int
	err := sc.Scan(&v.ID, &v.FileSHA256, &v.SourceFile, &v.MailUID, &v.MailSubject,
		&v.MailFrom, &v.MailDate, &v.Supplier, &v.InvoiceNumber, &v.InvoiceDate, &v.VehicleReg,
		&v.Currency, &v.Netto, &v.VATAmount, &v.VATRate, &v.Brutto,
		&review, &general, &v.Notes, &v.CreatedAt)
	if err != nil {
		return nil, err
	}
	v.NeedsReview = review != 0
	v.IsGeneral = general != 0
	return &v, nil
}

// Get returns one invoice with its items, or sql.ErrNoRows.
func (s *Store) Get(id int64) (*Invoice, error) {
	row := s.db.QueryRow(`
		SELECT i.id, i.file_sha256, i.source_file, i.mail_uid, i.mail_subject, i.mail_from,
		       i.mail_date, i.supplier, i.invoice_number, i.invoice_date, i.vehicle_reg,
		       i.currency, i.netto, i.vat_amount, i.vat_rate, i.brutto,
		       i.needs_review, i.is_general, i.notes, i.created_at
		FROM invoices i WHERE i.id = ?`, id)
	v, err := scanInvoice(row)
	if err != nil {
		return nil, err
	}
	items, err := s.itemsFor(id)
	if err != nil {
		return nil, err
	}
	v.Items = items
	return v, nil
}

// Patch applies human corrections. Only these fields are editable — the
// hash, source file and mail provenance stay as recorded.
type Patch struct {
	Supplier      *string  `json:"supplier"`
	InvoiceNumber *string  `json:"invoice_number"`
	InvoiceDate   *string  `json:"invoice_date"`
	VehicleReg    *string  `json:"vehicle_reg"`
	Currency      *string  `json:"currency"`
	Netto         *float64 `json:"netto"`
	VATAmount     *float64 `json:"vat_amount"`
	VATRate       *float64 `json:"vat_rate"`
	Brutto        *float64 `json:"brutto"`
	NeedsReview   *bool    `json:"needs_review"`
	Notes         *string  `json:"notes"`
}

func (s *Store) Update(id int64, p Patch) error {
	var sets []string
	var args []any

	add := func(col string, val any) {
		sets = append(sets, col+" = ?")
		args = append(args, val)
	}
	if p.Supplier != nil {
		add("supplier", *p.Supplier)
	}
	if p.InvoiceNumber != nil {
		add("invoice_number", *p.InvoiceNumber)
	}
	if p.InvoiceDate != nil {
		add("invoice_date", *p.InvoiceDate)
	}
	if p.VehicleReg != nil {
		add("vehicle_reg", NormalizeReg(*p.VehicleReg))
	}
	if p.Currency != nil {
		add("currency", strings.ToUpper(*p.Currency))
	}
	if p.Netto != nil {
		add("netto", *p.Netto)
	}
	if p.VATAmount != nil {
		add("vat_amount", *p.VATAmount)
	}
	if p.VATRate != nil {
		add("vat_rate", *p.VATRate)
	}
	if p.Brutto != nil {
		add("brutto", *p.Brutto)
	}
	if p.NeedsReview != nil {
		add("needs_review", boolToInt(*p.NeedsReview))
	}
	if p.Notes != nil {
		add("notes", *p.Notes)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := s.db.Exec("UPDATE invoices SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	return err
}

// Delete removes an invoice and its items. The archived PDF is left on disk
// deliberately: the document is the legal record, the row is only our reading
// of it, and re-ingesting the file is how you recover from a bad extraction.
func (s *Store) Delete(id int64) (sourceFile string, err error) {
	if err := s.db.QueryRow(`SELECT source_file FROM invoices WHERE id = ?`, id).Scan(&sourceFile); err != nil {
		return "", err
	}
	// Clearing the hash record too would let the same file be re-ingested.
	_, err = s.db.Exec(`DELETE FROM invoices WHERE id = ?`, id)
	return sourceFile, err
}

// ── Aggregates ────────────────────────────────────────────────────────────

type VehicleAgg struct {
	VehicleReg string  `json:"vehicle_reg"`
	Invoices   int     `json:"invoices"`
	Parts      int     `json:"parts"`
	Netto      float64 `json:"netto"`
	VAT        float64 `json:"vat"`
	Brutto     float64 `json:"brutto"`
	LastDate   string  `json:"last_date"`
	// From the fleet registry, so a plate can be read as "Skoda Superb,
	// callsign 603" rather than seven characters nobody recognises.
	Make     string `json:"make"`
	Model    string `json:"model"`
	Callsign string `json:"callsign"`
}

// Vehicles totals spend per registration — for a taxi fleet this is the view
// that answers "which car is costing me money".
func (s *Store) Vehicles() ([]VehicleAgg, error) {
	rows, err := s.db.Query(`
		SELECT i.vehicle_reg,
		       COUNT(DISTINCT i.id),
		       (SELECT COUNT(1) FROM invoice_items it
		         WHERE it.invoice_id IN (SELECT id FROM invoices WHERE vehicle_reg = i.vehicle_reg)
		           AND it.part_number <> ''),
		       COALESCE(SUM(i.netto),0), COALESCE(SUM(i.vat_amount),0), COALESCE(SUM(i.brutto),0),
		       MAX(i.invoice_date),
		       COALESCE(MAX(v.make),''), COALESCE(MAX(v.model),''), COALESCE(MAX(v.notes),'')
		FROM invoices i
		LEFT JOIN vehicles v ON v.registration = i.vehicle_reg
		WHERE i.vehicle_reg <> ''
		GROUP BY i.vehicle_reg
		ORDER BY SUM(i.brutto) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []VehicleAgg{}
	for rows.Next() {
		var v VehicleAgg
		var last sql.NullString
		var notes string
		if err := rows.Scan(&v.VehicleReg, &v.Invoices, &v.Parts, &v.Netto, &v.VAT, &v.Brutto, &last,
			&v.Make, &v.Model, &notes); err != nil {
			return nil, err
		}
		v.LastDate = last.String
		v.Callsign = callsignFrom(notes)
		out = append(out, v)
	}
	return out, rows.Err()
}

type SupplierAgg struct {
	Supplier string  `json:"supplier"`
	Invoices int     `json:"invoices"`
	Netto    float64 `json:"netto"`
	VAT      float64 `json:"vat"`
	Brutto   float64 `json:"brutto"`
	LastDate string  `json:"last_date"`
}

func (s *Store) Suppliers() ([]SupplierAgg, error) {
	rows, err := s.db.Query(`
		SELECT supplier, COUNT(1),
		       COALESCE(SUM(netto),0), COALESCE(SUM(vat_amount),0), COALESCE(SUM(brutto),0),
		       MAX(invoice_date)
		FROM invoices WHERE supplier <> ''
		GROUP BY supplier ORDER BY SUM(brutto) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []SupplierAgg{}
	for rows.Next() {
		var v SupplierAgg
		var last sql.NullString
		if err := rows.Scan(&v.Supplier, &v.Invoices, &v.Netto, &v.VAT, &v.Brutto, &last); err != nil {
			return nil, err
		}
		v.LastDate = last.String
		out = append(out, v)
	}
	return out, rows.Err()
}

type PartAgg struct {
	PartNumber string  `json:"part_number"`
	Desc       string  `json:"description"`
	Times      int     `json:"times"`
	Quantity   float64 `json:"quantity"`
	Netto      float64 `json:"netto"`
	Vehicles   int     `json:"vehicles"`
	LastDate   string  `json:"last_date"`
	// Stock is what's left on the shelf: everything ever invoiced for this
	// part, minus everything the parts counter has logged as taken. There is
	// no separate "receive stock" step — a part arrives the moment its
	// invoice is stored — so this is the true running count, not an estimate.
	Stock float64 `json:"stock"`
}

// Parts ranks part numbers by spend, and counts how many distinct vehicles
// each was fitted to — a part recurring across one vehicle suggests a problem.
func (s *Store) Parts() ([]PartAgg, error) {
	rows, err := s.db.Query(`
		SELECT it.part_number,
		       MAX(it.description),
		       COUNT(1),
		       COALESCE(SUM(it.quantity),0),
		       COALESCE(SUM(it.netto),0),
		       COUNT(DISTINCT COALESCE(NULLIF(it.vehicle_reg,''), i.vehicle_reg)),
		       MAX(i.invoice_date),
		       COALESCE(SUM(it.quantity),0) - COALESCE((
		           SELECT SUM(st.quantity) FROM stock_takes st WHERE st.part_number = it.part_number
		       ), 0)
		FROM invoice_items it
		JOIN invoices i ON i.id = it.invoice_id
		WHERE it.part_number <> ''
		GROUP BY it.part_number
		ORDER BY SUM(it.netto) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PartAgg{}
	for rows.Next() {
		var v PartAgg
		var desc, last sql.NullString
		if err := rows.Scan(&v.PartNumber, &desc, &v.Times, &v.Quantity, &v.Netto, &v.Vehicles, &last, &v.Stock); err != nil {
			return nil, err
		}
		v.Desc, v.LastDate = desc.String, last.String
		out = append(out, v)
	}
	return out, rows.Err()
}

type MonthAgg struct {
	Month    string  `json:"month"`
	Invoices int     `json:"invoices"`
	Netto    float64 `json:"netto"`
	VAT      float64 `json:"vat"`
	Brutto   float64 `json:"brutto"`
}

func (s *Store) Months() ([]MonthAgg, error) {
	rows, err := s.db.Query(`
		SELECT substr(invoice_date,1,7) AS m, COUNT(1),
		       COALESCE(SUM(netto),0), COALESCE(SUM(vat_amount),0), COALESCE(SUM(brutto),0)
		FROM invoices WHERE invoice_date <> ''
		GROUP BY m ORDER BY m DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []MonthAgg{}
	for rows.Next() {
		var v MonthAgg
		if err := rows.Scan(&v.Month, &v.Invoices, &v.Netto, &v.VAT, &v.Brutto); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Overview is the headline set shown on the dashboard.
type Overview struct {
	Invoices    int     `json:"invoices"`
	Items       int     `json:"items"`
	VehicleCnt  int     `json:"vehicles"`
	SupplierCnt int     `json:"suppliers"`
	NeedsReview int     `json:"needs_review"`
	Netto       float64 `json:"netto"`
	VAT         float64 `json:"vat"`
	Brutto      float64 `json:"brutto"`
	// Purchases and Credits split Brutto in two. Credits is negative or zero.
	Purchases   float64    `json:"purchases"`
	Credits     float64    `json:"credits"`
	CreditCount int        `json:"credit_count"`
	Months      []MonthAgg `json:"months"`
}

func (s *Store) Overview() (*Overview, error) {
	var o Overview
	// Purchases and credit notes are summed separately. A credit note is stored
	// with negative totals, which is arithmetically right — it does reduce the
	// VAT you reclaim — but netting them into a single "spend" figure is
	// misleading: one large credit can turn a month of real purchasing into a
	// negative number that reads as nonsense.
	err := s.db.QueryRow(`
		SELECT COUNT(1),
		       COALESCE(SUM(netto),0), COALESCE(SUM(vat_amount),0), COALESCE(SUM(brutto),0),
		       COALESCE(SUM(needs_review),0),
		       COUNT(DISTINCT NULLIF(vehicle_reg,'')),
		       COUNT(DISTINCT NULLIF(supplier,'')),
		       COALESCE(SUM(CASE WHEN brutto >= 0 THEN brutto ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN brutto <  0 THEN brutto ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN brutto <  0 THEN 1 ELSE 0 END),0)
		FROM invoices`).
		Scan(&o.Invoices, &o.Netto, &o.VAT, &o.Brutto, &o.NeedsReview, &o.VehicleCnt, &o.SupplierCnt,
			&o.Purchases, &o.Credits, &o.CreditCount)
	if err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM invoice_items`).Scan(&o.Items); err != nil {
		return nil, err
	}
	months, err := s.Months()
	if err != nil {
		return nil, err
	}
	// Twelve months is enough for the trend bars without bloating the payload.
	if len(months) > 12 {
		months = months[:12]
	}
	o.Months = months
	return &o, nil
}

// DistinctValues feeds the filter dropdowns.
func (s *Store) DistinctValues() (suppliers, regs []string, err error) {
	suppliers, err = s.distinct(`SELECT DISTINCT supplier FROM invoices WHERE supplier <> '' ORDER BY LOWER(supplier)`)
	if err != nil {
		return nil, nil, err
	}
	regs, err = s.distinct(`SELECT DISTINCT vehicle_reg FROM invoices WHERE vehicle_reg <> '' ORDER BY vehicle_reg`)
	return suppliers, regs, err
}

func (s *Store) distinct(q string) ([]string, error) {
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// callsignFrom pulls the dispatch callsign back out of the notes field, which
// is where the fleet import records it.
func callsignFrom(notes string) string {
	const prefix = "Callsign "
	if !strings.HasPrefix(notes, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(notes, prefix)
	if cut, _, found := strings.Cut(rest, " · "); found {
		return cut
	}
	return rest
}
