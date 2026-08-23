package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// NormalizeReg matches what the extractor produces, so a plate typed by hand
// in the registry joins against one read off an invoice.
//
// It also rejects placeholders. Models and people alike fill this field with
// "-", "N/A" or "none" instead of leaving it empty, and those must become an
// empty registration: a literal "-" sorts ahead of every real plate and shows
// up as a vehicle in its own right. Every UK plate contains at least one
// digit, so a value without one is not a registration.
func NormalizeReg(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "-", "", ".", "", "/", "", ",", "").Replace(s)
	if !strings.ContainsFunc(s, unicode.IsDigit) {
		return ""
	}
	return fixConfusable(s)
}

// ukPlate is the current UK format: two letters, two digits, three letters.
var ukPlate = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Z]{3}$`)

// fixConfusable repairs the one typo this system cannot afford: O typed as 0,
// or I as 1, in a registration.
//
// In a plate the two are indistinguishable by eye, but to the database they
// are different vehicles — so a single mistyped character silently splits a
// car's cost history in two and leaves half of it under a plate nobody
// recognises. That already happened here with FG21OXA and FG210XA.
//
// The correction is applied only where the format makes the intent certain:
// positions three and four are always digits, the last three always letters.
// A value that does not become a valid plate is returned untouched, so
// personalised and older-format registrations are never rewritten.
func fixConfusable(s string) string {
	if len(s) != 7 || ukPlate.MatchString(s) {
		return s
	}

	b := []byte(s)
	for i := 0; i < 2; i++ { // leading letters
		switch b[i] {
		case '0':
			b[i] = 'O'
		case '1':
			b[i] = 'I'
		}
	}
	for i := 2; i < 4; i++ { // the two digits
		switch b[i] {
		case 'O':
			b[i] = '0'
		case 'I':
			b[i] = '1'
		}
	}
	for i := 4; i < 7; i++ { // trailing letters
		switch b[i] {
		case '0':
			b[i] = 'O'
		case '1':
			b[i] = 'I'
		}
	}

	if fixed := string(b); ukPlate.MatchString(fixed) {
		return fixed
	}
	return s
}

// ── Companies ─────────────────────────────────────────────────────────────

type Company struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	IsDefault bool    `json:"is_default"`
	Vehicles  int     `json:"vehicles"`
	Invoices  int     `json:"invoices"`
	Netto     float64 `json:"netto"`
	VAT       float64 `json:"vat"`
	Brutto    float64 `json:"brutto"`
}

// Companies lists each company with its fleet size and spend. Vehicles with no
// company yet are reported separately by UnassignedVehicles.
func (s *Store) Companies() ([]Company, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.name, c.is_default,
		       (SELECT COUNT(1) FROM vehicles v WHERE v.company_id = c.id),
		       COUNT(i.id),
		       COALESCE(SUM(i.netto),0), COALESCE(SUM(i.vat_amount),0), COALESCE(SUM(i.brutto),0)
		FROM companies c
		LEFT JOIN vehicles v2 ON v2.company_id = c.id
		LEFT JOIN invoices i  ON i.vehicle_reg = v2.registration
		GROUP BY c.id, c.name, c.is_default
		ORDER BY c.is_default, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Company{}
	for rows.Next() {
		var c Company
		var def int
		if err := rows.Scan(&c.ID, &c.Name, &def, &c.Vehicles, &c.Invoices,
			&c.Netto, &c.VAT, &c.Brutto); err != nil {
			return nil, err
		}
		c.IsDefault = def != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddCompany(name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("company name is required")
	}
	res, err := s.db.Exec(
		`INSERT INTO companies (name, is_default, created_at) VALUES (?, 0, ?)`,
		name, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RenameCompany(id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("company name is required")
	}
	_, err := s.db.Exec(`UPDATE companies SET name = ? WHERE id = ?`, name, id)
	return err
}

// DeleteCompany refuses while vehicles still point at it, rather than silently
// orphaning a fleet.
func (s *Store) DeleteCompany(id int64) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM vehicles WHERE company_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%d vehicle(s) still assigned to this company", n)
	}
	var isDefault int
	if err := s.db.QueryRow(`SELECT is_default FROM companies WHERE id = ?`, id).Scan(&isDefault); err != nil {
		return err
	}
	if isDefault != 0 {
		return fmt.Errorf("cannot delete the default company")
	}
	_, err := s.db.Exec(`DELETE FROM companies WHERE id = ?`, id)
	return err
}

func (s *Store) defaultCompanyID() (sql.NullInt64, error) {
	var id sql.NullInt64
	err := s.db.QueryRow(`SELECT id FROM companies WHERE is_default = 1 LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return id, nil
	}
	return id, err
}

// ── Vehicles ──────────────────────────────────────────────────────────────

type Vehicle struct {
	Registration string  `json:"registration"`
	CompanyID    *int64  `json:"company_id"`
	CompanyName  string  `json:"company_name"`
	Make         string  `json:"make"`
	Model        string  `json:"model"`
	Year         string  `json:"year"`
	Driver       string  `json:"driver"`
	Notes        string  `json:"notes"`
	Active       bool    `json:"active"`
	Invoices     int     `json:"invoices"`
	Netto        float64 `json:"netto"`
	VAT          float64 `json:"vat"`
	Brutto       float64 `json:"brutto"`
	FirstSeen    string  `json:"first_seen"`
	LastSeen     string  `json:"last_seen"`

	// Spec fields learned from repairs.<domain> visits (see UpdateVehicleSpec)
	// — blank until a repair entry has ever supplied one.
	VIN              string `json:"vin"`
	Colour           string `json:"colour"`
	CylinderCapacity string `json:"cylinder_capacity"`
	FuelType         string `json:"fuel_type"`
	EngineSize       string `json:"engine_size"`
	EngineNumber     string `json:"engine_number"`
	TyreSize         string `json:"tyre_size"`
	RadioCode        string `json:"radio_code"`
	SpareKeys        string `json:"spare_keys"`
	// Capabilities is a fleet-level classification, not a repair-visit spec
	// fact — set from the dashboard or a bulk import, e.g. "F".
	Capabilities string `json:"capabilities"`
}

const vehicleSelect = `
	SELECT v.registration, v.company_id, COALESCE(c.name,''),
	       v.make, v.model, v.year, v.driver, v.notes, v.active,
	       COUNT(i.id),
	       COALESCE(SUM(i.netto),0), COALESCE(SUM(i.vat_amount),0), COALESCE(SUM(i.brutto),0),
	       COALESCE(MIN(NULLIF(i.invoice_date,'')),''), COALESCE(MAX(NULLIF(i.invoice_date,'')),''),
	       v.vin, v.colour, v.cylinder_capacity, v.fuel_type, v.engine_size, v.engine_number, v.tyre_size,
	       v.radio_code, v.spare_keys, v.capabilities
	FROM vehicles v
	LEFT JOIN companies c ON c.id = v.company_id
	LEFT JOIN invoices i  ON i.vehicle_reg = v.registration`

func scanVehicle(sc scanner) (*Vehicle, error) {
	var v Vehicle
	var companyID sql.NullInt64
	var active int
	if err := sc.Scan(&v.Registration, &companyID, &v.CompanyName, &v.Make, &v.Model,
		&v.Year, &v.Driver, &v.Notes, &active, &v.Invoices,
		&v.Netto, &v.VAT, &v.Brutto, &v.FirstSeen, &v.LastSeen,
		&v.VIN, &v.Colour, &v.CylinderCapacity, &v.FuelType, &v.EngineSize, &v.EngineNumber, &v.TyreSize,
		&v.RadioCode, &v.SpareKeys, &v.Capabilities); err != nil {
		return nil, err
	}
	if companyID.Valid {
		id := companyID.Int64
		v.CompanyID = &id
	}
	v.Active = active != 0
	return &v, nil
}

// RegisteredVehicles lists the registry. companyID filters when non-zero.
func (s *Store) RegisteredVehicles(companyID int64) ([]Vehicle, error) {
	q := vehicleSelect
	var args []any
	if companyID > 0 {
		q += ` WHERE v.company_id = ?`
		args = append(args, companyID)
	}
	q += ` GROUP BY v.registration ORDER BY SUM(i.brutto) DESC, v.registration`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Vehicle{}
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func (s *Store) GetVehicle(reg string) (*Vehicle, error) {
	reg = NormalizeReg(reg)
	row := s.db.QueryRow(vehicleSelect+` WHERE v.registration = ? GROUP BY v.registration`, reg)
	v, err := scanVehicle(row)
	if err == sql.ErrNoRows {
		// Seen on invoices but never registered — synthesise a record so the
		// stats page still works before anyone fills in the details.
		return s.unregisteredVehicle(reg)
	}
	return v, err
}

func (s *Store) unregisteredVehicle(reg string) (*Vehicle, error) {
	v := &Vehicle{Registration: reg, Active: true}
	err := s.db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(netto),0), COALESCE(SUM(vat_amount),0), COALESCE(SUM(brutto),0),
		       COALESCE(MIN(NULLIF(invoice_date,'')),''), COALESCE(MAX(NULLIF(invoice_date,'')),'')
		FROM invoices WHERE vehicle_reg = ?`, reg).
		Scan(&v.Invoices, &v.Netto, &v.VAT, &v.Brutto, &v.FirstSeen, &v.LastSeen)
	if err != nil {
		return nil, err
	}
	if v.Invoices == 0 {
		return nil, sql.ErrNoRows
	}
	return v, nil
}

type VehiclePatch struct {
	CompanyID *int64  `json:"company_id"`
	Make      *string `json:"make"`
	Model     *string `json:"model"`
	Year      *string `json:"year"`
	Driver    *string `json:"driver"`
	Notes     *string `json:"notes"`
	Active    *bool   `json:"active"`
}

// SaveVehicle upserts a registry entry. A vehicle with no company lands in the
// default one, so nothing is ever left unattributed by accident.
func (s *Store) SaveVehicle(reg string, p VehiclePatch) error {
	reg = NormalizeReg(reg)
	if reg == "" {
		return fmt.Errorf("registration is required")
	}

	companyID := sql.NullInt64{}
	if p.CompanyID != nil && *p.CompanyID > 0 {
		companyID = sql.NullInt64{Int64: *p.CompanyID, Valid: true}
	} else {
		def, err := s.defaultCompanyID()
		if err != nil {
			return err
		}
		companyID = def
	}

	_, err := s.db.Exec(`
		INSERT INTO vehicles (registration, company_id, make, model, year, driver, notes, active, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(registration) DO UPDATE SET
		  company_id = excluded.company_id,
		  make       = excluded.make,
		  model      = excluded.model,
		  year       = excluded.year,
		  driver     = excluded.driver,
		  notes      = excluded.notes,
		  active     = excluded.active`,
		reg, companyID, str(p.Make), str(p.Model), str(p.Year), str(p.Driver), str(p.Notes),
		boolToInt(boolOr(p.Active, true)), now())
	return err
}

func (s *Store) DeleteVehicle(reg string) error {
	_, err := s.db.Exec(`DELETE FROM vehicles WHERE registration = ?`, NormalizeReg(reg))
	return err
}

// SetVehicleCapabilities assigns the fleet-level capability code to reg —
// deliberately its own tiny setter, not folded into SaveVehicle's upsert:
// that one requires every identity field on every call (it isn't a true
// partial patch — an omitted make/company_id gets overwritten to blank or
// the default, not left alone), so reusing it here to change just one
// field would risk silently wiping a vehicle's driver, company or name.
// Registers the vehicle first if this is a plate with no registry row at
// all yet, same as a repair visit's own spec update does, so this works
// for a genuinely new car too, not only ones already on file.
func (s *Store) SetVehicleCapabilities(reg, capabilities string) error {
	reg = NormalizeReg(reg)
	if reg == "" {
		return fmt.Errorf("registration is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureVehicleRegistered(tx, reg); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE vehicles SET capabilities = ? WHERE registration = ?`,
		strings.TrimSpace(capabilities), reg); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureVehicleRegistered gives a plate seen on an invoice a registry row the
// moment it is first stored, landing it in the default company (Overall
// Clients) rather than leaving it to sit uncounted until someone notices it
// on the "unregistered plates" list and clicks Add to fleet. Nothing here
// overwrites an existing row — ON CONFLICT DO NOTHING — so a vehicle already
// assigned to a real company is never bumped back to the default.
//
// Run inside the same transaction as the invoice write, so a car's very first
// invoice is never the one invoice missing from every company's total.
func ensureVehicleRegistered(tx *sql.Tx, reg string) error {
	reg = NormalizeReg(reg)
	if reg == "" {
		return nil
	}
	var def sql.NullInt64
	if err := tx.QueryRow(`SELECT id FROM companies WHERE is_default = 1 LIMIT 1`).Scan(&def); err != nil && err != sql.ErrNoRows {
		return err
	}
	_, err := tx.Exec(`
		INSERT INTO vehicles (registration, company_id, make, model, year, driver, notes, active, created_at)
		VALUES (?, ?, '', '', '', '', '', 1, ?)
		ON CONFLICT(registration) DO NOTHING`,
		reg, def, now())
	return err
}

// UnassignedVehicles are plates that appear on invoices but are not in the
// registry yet. In normal operation this stays empty — ensureVehicleRegistered
// registers a plate the moment its first invoice is stored — so anything that
// does show up here got in some other way (a restored backup made before this
// existed, a row deleted by hand) and is worth a look rather than routine
// triage.
func (s *Store) UnassignedVehicles() ([]VehicleAgg, error) {
	rows, err := s.db.Query(`
		SELECT i.vehicle_reg, COUNT(1), 0,
		       COALESCE(SUM(i.netto),0), COALESCE(SUM(i.vat_amount),0), COALESCE(SUM(i.brutto),0),
		       MAX(i.invoice_date)
		FROM invoices i
		WHERE i.vehicle_reg <> ''
		  AND NOT EXISTS (SELECT 1 FROM vehicles v WHERE v.registration = i.vehicle_reg)
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
		if err := rows.Scan(&v.VehicleReg, &v.Invoices, &v.Parts, &v.Netto, &v.VAT, &v.Brutto, &last); err != nil {
			return nil, err
		}
		v.LastDate = last.String
		out = append(out, v)
	}
	return out, rows.Err()
}

// ── Per-vehicle statistics ────────────────────────────────────────────────

type VehicleStats struct {
	Vehicle      *Vehicle      `json:"vehicle"`
	Months       []MonthAgg    `json:"months"`
	BySupplier   []SupplierAgg `json:"by_supplier"`
	Parts        []PartAgg     `json:"parts"`
	Invoices     []Invoice     `json:"invoices"`
	AvgPerMonth  float64       `json:"avg_per_month"`
	MonthsActive int           `json:"months_active"`

	// Repairs is this vehicle's service history logged from
	// repairs.<domain>. LastTimingBelt is tracked separately from the rest
	// of that history — a workshop watches a belt's interval on its own,
	// not lumped in with every other kind of visit.
	Repairs        []Repair `json:"repairs"`
	LastTimingBelt string   `json:"last_timing_belt"`
	HasTimingBelt  bool     `json:"has_timing_belt"`
}

func (s *Store) VehicleStats(reg string) (*VehicleStats, error) {
	reg = NormalizeReg(reg)
	v, err := s.GetVehicle(reg)
	if err != nil {
		return nil, err
	}
	out := &VehicleStats{Vehicle: v}

	if out.Months, err = s.monthsWhere(`vehicle_reg = ?`, reg); err != nil {
		return nil, err
	}
	if out.BySupplier, err = s.suppliersWhere(`vehicle_reg = ?`, reg); err != nil {
		return nil, err
	}
	if out.Parts, err = s.partsForVehicle(reg); err != nil {
		return nil, err
	}
	if out.Invoices, err = s.invoicesWhere(`i.vehicle_reg = ?`, reg); err != nil {
		return nil, err
	}
	if out.Repairs, err = s.ListRepairsForVehicle(reg); err != nil {
		return nil, err
	}
	if out.LastTimingBelt, out.HasTimingBelt, err = s.LastTimingBeltChange(reg); err != nil {
		return nil, err
	}

	out.MonthsActive = len(out.Months)
	if out.MonthsActive > 0 {
		out.AvgPerMonth = v.Brutto / float64(out.MonthsActive)
	}
	return out, nil
}

func (s *Store) monthsWhere(where string, args ...any) ([]MonthAgg, error) {
	rows, err := s.db.Query(`
		SELECT substr(invoice_date,1,7) AS m, COUNT(1),
		       COALESCE(SUM(netto),0), COALESCE(SUM(vat_amount),0), COALESCE(SUM(brutto),0)
		FROM invoices WHERE invoice_date <> '' AND (`+where+`)
		GROUP BY m ORDER BY m DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []MonthAgg{}
	for rows.Next() {
		var m MonthAgg
		if err := rows.Scan(&m.Month, &m.Invoices, &m.Netto, &m.VAT, &m.Brutto); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) suppliersWhere(where string, args ...any) ([]SupplierAgg, error) {
	rows, err := s.db.Query(`
		SELECT supplier, COUNT(1),
		       COALESCE(SUM(netto),0), COALESCE(SUM(vat_amount),0), COALESCE(SUM(brutto),0),
		       MAX(invoice_date)
		FROM invoices WHERE supplier <> '' AND (`+where+`)
		GROUP BY supplier ORDER BY SUM(brutto) DESC`, args...)
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

func (s *Store) partsForVehicle(reg string) ([]PartAgg, error) {
	rows, err := s.db.Query(`
		SELECT it.part_number, MAX(it.description), COUNT(1),
		       COALESCE(SUM(it.quantity),0), COALESCE(SUM(it.netto),0), 1,
		       MAX(i.invoice_date)
		FROM invoice_items it
		JOIN invoices i ON i.id = it.invoice_id
		WHERE it.part_number <> ''
		  AND COALESCE(NULLIF(it.vehicle_reg,''), i.vehicle_reg) = ?
		GROUP BY it.part_number
		ORDER BY SUM(it.netto) DESC`, reg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PartAgg{}
	for rows.Next() {
		var p PartAgg
		var desc, last sql.NullString
		if err := rows.Scan(&p.PartNumber, &desc, &p.Times, &p.Quantity, &p.Netto, &p.Vehicles, &last); err != nil {
			return nil, err
		}
		p.Desc, p.LastDate = desc.String, last.String
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) invoicesWhere(where string, args ...any) ([]Invoice, error) {
	rows, err := s.db.Query(`
		SELECT i.id, i.file_sha256, i.source_file, i.mail_uid, i.mail_subject, i.mail_from,
		       i.mail_date, i.supplier, i.invoice_number, i.invoice_date, i.vehicle_reg,
		       i.currency, i.netto, i.vat_amount, i.vat_rate, i.brutto,
		       i.needs_review, i.is_general, i.returned, i.credit_of, i.notes, i.created_at
		FROM invoices i WHERE `+where+`
		ORDER BY COALESCE(NULLIF(i.invoice_date,''), i.created_at) DESC, i.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Invoice{}
	for rows.Next() {
		v, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		items, err := s.itemsFor(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

// ── Per-part statistics ───────────────────────────────────────────────────

// PricePoint is one purchase of a part, for the price history chart.
type PricePoint struct {
	Date      string  `json:"date"`
	Supplier  string  `json:"supplier"`
	Vehicle   string  `json:"vehicle"`
	UnitPrice float64 `json:"unit_price"`
	Quantity  float64 `json:"quantity"`
	Netto     float64 `json:"netto"`
	VAT       float64 `json:"vat"`
	Brutto    float64 `json:"brutto"`
	InvoiceID int64   `json:"invoice_id"`
}

type PartStats struct {
	PartNumber   string        `json:"part_number"`
	Desc         string        `json:"description"`
	Times        int           `json:"times"`
	Quantity     float64       `json:"quantity"`
	Netto        float64       `json:"netto"`
	VAT          float64       `json:"vat"`
	Brutto       float64       `json:"brutto"`
	AvgUnitPrice float64       `json:"avg_unit_price"`
	MinUnitPrice float64       `json:"min_unit_price"`
	MaxUnitPrice float64       `json:"max_unit_price"`
	PriceChange  float64       `json:"price_change_pct"`
	History      []PricePoint  `json:"history"`
	ByVehicle    []VehicleAgg  `json:"by_vehicle"`
	BySupplier   []SupplierAgg `json:"by_supplier"`
}

// PartStats builds the price history for one part number. Rising unit prices
// across time, or one vehicle consuming the same part repeatedly, are both
// things worth noticing, so both are surfaced.
func (s *Store) PartStats(part string) (*PartStats, error) {
	part = strings.TrimSpace(part)
	if part == "" {
		return nil, fmt.Errorf("part number is required")
	}
	out := &PartStats{PartNumber: part, History: []PricePoint{}}

	rows, err := s.db.Query(`
		SELECT COALESCE(NULLIF(i.invoice_date,''),''), i.supplier,
		       COALESCE(NULLIF(it.vehicle_reg,''), i.vehicle_reg),
		       it.unit_price, it.quantity, it.netto, it.vat_amount, it.brutto, i.id
		FROM invoice_items it
		JOIN invoices i ON i.id = it.invoice_id
		WHERE it.part_number = ?
		ORDER BY i.invoice_date, i.id`, part)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var p PricePoint
		if err := rows.Scan(&p.Date, &p.Supplier, &p.Vehicle, &p.UnitPrice,
			&p.Quantity, &p.Netto, &p.VAT, &p.Brutto, &p.InvoiceID); err != nil {
			return nil, err
		}
		out.History = append(out.History, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.History) == 0 {
		return nil, sql.ErrNoRows
	}

	out.Times = len(out.History)
	out.MinUnitPrice, out.MaxUnitPrice = out.History[0].UnitPrice, out.History[0].UnitPrice
	var unitSum float64
	var priced int
	for _, p := range out.History {
		out.Quantity += p.Quantity
		out.Netto += p.Netto
		out.VAT += p.VAT
		out.Brutto += p.Brutto
		if p.UnitPrice > 0 {
			unitSum += p.UnitPrice
			priced++
			if p.UnitPrice < out.MinUnitPrice || out.MinUnitPrice == 0 {
				out.MinUnitPrice = p.UnitPrice
			}
			if p.UnitPrice > out.MaxUnitPrice {
				out.MaxUnitPrice = p.UnitPrice
			}
		}
	}
	if priced > 0 {
		out.AvgUnitPrice = unitSum / float64(priced)
	}
	// Percentage move from the first priced purchase to the most recent one.
	first, last := firstPriced(out.History), lastPriced(out.History)
	if first > 0 && last > 0 {
		out.PriceChange = (last - first) / first * 100
	}

	if err := s.db.QueryRow(
		`SELECT MAX(description) FROM invoice_items WHERE part_number = ? AND description <> ''`,
		part).Scan(&out.Desc); err != nil && err != sql.ErrNoRows {
		// A missing description is cosmetic, never fatal.
		out.Desc = ""
	}

	if out.ByVehicle, err = s.partByVehicle(part); err != nil {
		return nil, err
	}
	if out.BySupplier, err = s.partBySupplier(part); err != nil {
		return nil, err
	}
	return out, nil
}

func firstPriced(h []PricePoint) float64 {
	for _, p := range h {
		if p.UnitPrice > 0 {
			return p.UnitPrice
		}
	}
	return 0
}

func lastPriced(h []PricePoint) float64 {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].UnitPrice > 0 {
			return h[i].UnitPrice
		}
	}
	return 0
}

func (s *Store) partByVehicle(part string) ([]VehicleAgg, error) {
	rows, err := s.db.Query(`
		SELECT COALESCE(NULLIF(it.vehicle_reg,''), i.vehicle_reg) AS reg,
		       COUNT(1), COUNT(1), 0, 0, COALESCE(SUM(it.netto),0), MAX(i.invoice_date)
		FROM invoice_items it
		JOIN invoices i ON i.id = it.invoice_id
		WHERE it.part_number = ? AND COALESCE(NULLIF(it.vehicle_reg,''), i.vehicle_reg) <> ''
		GROUP BY reg ORDER BY SUM(it.netto) DESC`, part)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []VehicleAgg{}
	for rows.Next() {
		var v VehicleAgg
		var last sql.NullString
		if err := rows.Scan(&v.VehicleReg, &v.Invoices, &v.Parts, &v.Netto, &v.VAT, &v.Brutto, &last); err != nil {
			return nil, err
		}
		v.LastDate = last.String
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) partBySupplier(part string) ([]SupplierAgg, error) {
	rows, err := s.db.Query(`
		SELECT i.supplier, COUNT(1), COALESCE(SUM(it.netto),0), 0, COALESCE(SUM(it.brutto),0),
		       MAX(i.invoice_date)
		FROM invoice_items it
		JOIN invoices i ON i.id = it.invoice_id
		WHERE it.part_number = ? AND i.supplier <> ''
		GROUP BY i.supplier ORDER BY SUM(it.netto) DESC`, part)
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

// ── helpers ───────────────────────────────────────────────────────────────

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
