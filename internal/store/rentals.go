package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// Rental agreement statuses. Only StatusBooked and StatusOut hold a car
// against its dates — see rentalHoldsCar, the single place that decides
// whether two hires collide.
const (
	RentalBooked    = "booked"
	RentalOut       = "out"
	RentalReturned  = "returned"
	RentalCancelled = "cancelled"
)

// Rental vehicle statuses. Only RentalAvailable can be booked; the other two
// take a car out of circulation without deleting it and losing its history.
const (
	RentalAvailable   = "available"
	RentalMaintenance = "maintenance"
	RentalRetired     = "retired"
)

// rentalHoldsCar is the SQL fragment for "this agreement still has a claim
// on its car". Written once and reused by every availability check, so
// there is exactly one answer to what counts as a clash — a cancelled hire
// never blocks anything, and a returned one stops blocking the moment the
// keys come back.
const rentalHoldsCar = `status IN ('booked','out')`

// ── Customers ─────────────────────────────────────────────────────────────

// RentalCustomer is someone we hand keys to. Kept apart from companies and
// vehicles: this is the only table in the database holding personal contact
// details, and it is worth being able to point at exactly one place when
// asked where customer data lives.
type RentalCustomer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	Email     string `json:"email"`
	Address   string `json:"address"`
	LicenceNo string `json:"licence_no"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
}

const rentalCustomerCols = `id, name, phone, email, address, licence_no, notes, created_at`

func scanRentalCustomer(scan func(...any) error) (*RentalCustomer, error) {
	var c RentalCustomer
	if err := scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Address,
		&c.LicenceNo, &c.Notes, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// AddRentalCustomer records a new customer. A name is the one hard
// requirement — everything else can be filled in later, but a nameless
// customer is not something any later screen could sensibly display.
func (s *Store) AddRentalCustomer(c RentalCustomer) (int64, error) {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return 0, fmt.Errorf("customer name is required")
	}
	res, err := s.db.Exec(`INSERT INTO rental_customers
		(name, phone, email, address, licence_no, notes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'))`,
		name, strings.TrimSpace(c.Phone), strings.TrimSpace(c.Email),
		strings.TrimSpace(c.Address), strings.TrimSpace(c.LicenceNo), c.Notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateRentalCustomer(id int64, c RentalCustomer) error {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return fmt.Errorf("customer name is required")
	}
	_, err := s.db.Exec(`UPDATE rental_customers
		SET name = ?, phone = ?, email = ?, address = ?, licence_no = ?, notes = ?
		WHERE id = ?`,
		name, strings.TrimSpace(c.Phone), strings.TrimSpace(c.Email),
		strings.TrimSpace(c.Address), strings.TrimSpace(c.LicenceNo), c.Notes, id)
	return err
}

func (s *Store) RentalCustomer(id int64) (*RentalCustomer, error) {
	row := s.db.QueryRow(`SELECT `+rentalCustomerCols+` FROM rental_customers WHERE id = ?`, id)
	return scanRentalCustomer(row.Scan)
}

// RentalCustomers lists customers, newest first, optionally narrowed by a
// substring of the name, phone or email — the three things someone at the
// desk actually has to hand when a customer walks in.
func (s *Store) RentalCustomers(q string) ([]RentalCustomer, error) {
	sqlStr := `SELECT ` + rentalCustomerCols + ` FROM rental_customers`
	var args []any
	if q = strings.TrimSpace(q); q != "" {
		sqlStr += ` WHERE name LIKE ? OR phone LIKE ? OR email LIKE ?`
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	sqlStr += ` ORDER BY name COLLATE NOCASE`

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RentalCustomer{}
	for rows.Next() {
		c, err := scanRentalCustomer(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// DeleteRentalCustomer refuses while any hire still references them —
// deleting would either orphan those rows or, with the foreign key on,
// fail with an error nobody could act on. Better to say why.
func (s *Store) DeleteRentalCustomer(id int64) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM rental_agreements WHERE customer_id = ?`, id).
		Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("this customer has %d hire(s) on file — cancel or keep them rather than deleting the customer", n)
	}
	_, err := s.db.Exec(`DELETE FROM rental_customers WHERE id = ?`, id)
	return err
}

// ── Vehicles ──────────────────────────────────────────────────────────────

type RentalVehicle struct {
	ID           int64   `json:"id"`
	Registration string  `json:"registration"`
	Make         string  `json:"make"`
	Model        string  `json:"model"`
	Year         string  `json:"year"`
	Colour       string  `json:"colour"`
	DailyRate    float64 `json:"daily_rate"`
	Status       string  `json:"status"`
	Notes        string  `json:"notes"`
	CreatedAt    string  `json:"created_at"`
}

const rentalVehicleCols = `id, registration, make, model, year, colour, daily_rate, status, notes, created_at`

func scanRentalVehicle(scan func(...any) error) (*RentalVehicle, error) {
	var v RentalVehicle
	if err := scan(&v.ID, &v.Registration, &v.Make, &v.Model, &v.Year,
		&v.Colour, &v.DailyRate, &v.Status, &v.Notes, &v.CreatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

func validRentalVehicleStatus(s string) bool {
	switch s {
	case RentalAvailable, RentalMaintenance, RentalRetired:
		return true
	}
	return false
}

// AddRentalVehicle puts a car in the hire pool. The registration goes
// through the same NormalizeReg the rest of the system uses, so a plate
// typed with a space here and without one there is still one car.
func (s *Store) AddRentalVehicle(v RentalVehicle) (int64, error) {
	reg := NormalizeReg(v.Registration)
	if reg == "" {
		return 0, fmt.Errorf("a valid registration is required")
	}
	if v.Status == "" {
		v.Status = RentalAvailable
	}
	if !validRentalVehicleStatus(v.Status) {
		return 0, fmt.Errorf("status must be available, maintenance or retired, got %q", v.Status)
	}
	if v.DailyRate < 0 {
		return 0, fmt.Errorf("daily rate cannot be negative")
	}
	res, err := s.db.Exec(`INSERT INTO rental_vehicles
		(registration, make, model, year, colour, daily_rate, status, notes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		reg, strings.TrimSpace(v.Make), strings.TrimSpace(v.Model), strings.TrimSpace(v.Year),
		strings.TrimSpace(v.Colour), v.DailyRate, v.Status, v.Notes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, fmt.Errorf("%s is already in the rental pool", reg)
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateRentalVehicle(id int64, v RentalVehicle) error {
	reg := NormalizeReg(v.Registration)
	if reg == "" {
		return fmt.Errorf("a valid registration is required")
	}
	if !validRentalVehicleStatus(v.Status) {
		return fmt.Errorf("status must be available, maintenance or retired, got %q", v.Status)
	}
	if v.DailyRate < 0 {
		return fmt.Errorf("daily rate cannot be negative")
	}
	_, err := s.db.Exec(`UPDATE rental_vehicles
		SET registration = ?, make = ?, model = ?, year = ?, colour = ?,
		    daily_rate = ?, status = ?, notes = ?
		WHERE id = ?`,
		reg, strings.TrimSpace(v.Make), strings.TrimSpace(v.Model), strings.TrimSpace(v.Year),
		strings.TrimSpace(v.Colour), v.DailyRate, v.Status, v.Notes, id)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%s is already in the rental pool", reg)
	}
	return err
}

func (s *Store) RentalVehicle(id int64) (*RentalVehicle, error) {
	row := s.db.QueryRow(`SELECT `+rentalVehicleCols+` FROM rental_vehicles WHERE id = ?`, id)
	return scanRentalVehicle(row.Scan)
}

func (s *Store) RentalVehicles() ([]RentalVehicle, error) {
	rows, err := s.db.Query(`SELECT ` + rentalVehicleCols + ` FROM rental_vehicles
		ORDER BY status = 'retired', registration`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RentalVehicle{}
	for rows.Next() {
		v, err := scanRentalVehicle(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// DeleteRentalVehicle refuses while the car has hire history, same
// reasoning as DeleteRentalCustomer — retiring it is what's wanted there.
func (s *Store) DeleteRentalVehicle(id int64) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM rental_agreements WHERE vehicle_id = ?`, id).
		Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("this car has %d hire(s) on file — set it to retired rather than deleting it", n)
	}
	_, err := s.db.Exec(`DELETE FROM rental_vehicles WHERE id = ?`, id)
	return err
}

// ── Agreements ────────────────────────────────────────────────────────────

// RentalAgreement is one hire, as written. See RentalAgreementView for the
// same row joined to the names a screen actually needs.
type RentalAgreement struct {
	ID         int64   `json:"id"`
	VehicleID  int64   `json:"vehicle_id"`
	CustomerID int64   `json:"customer_id"`
	StartsOn   string  `json:"starts_on"`
	EndsOn     string  `json:"ends_on"`
	ReturnedOn string  `json:"returned_on"`
	Status     string  `json:"status"`
	DailyRate  float64 `json:"daily_rate"`
	Notes      string  `json:"notes"`
	CreatedAt  string  `json:"created_at"`
}

// RentalAgreementView is what every list screen wants: the hire plus who
// and which car, resolved in the same query rather than N lookups after it.
type RentalAgreementView struct {
	RentalAgreement
	CustomerName string `json:"customer_name"`
	Phone        string `json:"phone"`
	Registration string `json:"registration"`
	Make         string `json:"make"`
	Model        string `json:"model"`
	// Days is the agreed length in whole days, inclusive of both ends —
	// a car out Monday to Monday is one day's hire, not zero.
	Days int `json:"days"`
	// Total is Days × DailyRate, the figure a payment would be for.
	Total float64 `json:"total"`
}

const rentalAgreementView = `
	SELECT a.id, a.vehicle_id, a.customer_id, a.starts_on, a.ends_on, a.returned_on,
	       a.status, a.daily_rate, a.notes, a.created_at,
	       c.name, c.phone, v.registration, v.make, v.model,
	       CAST(julianday(a.ends_on) - julianday(a.starts_on) AS INTEGER) + 1
	FROM rental_agreements a
	JOIN rental_customers c ON c.id = a.customer_id
	JOIN rental_vehicles  v ON v.id = a.vehicle_id`

func scanRentalAgreementView(scan func(...any) error) (*RentalAgreementView, error) {
	var a RentalAgreementView
	if err := scan(&a.ID, &a.VehicleID, &a.CustomerID, &a.StartsOn, &a.EndsOn, &a.ReturnedOn,
		&a.Status, &a.DailyRate, &a.Notes, &a.CreatedAt,
		&a.CustomerName, &a.Phone, &a.Registration, &a.Make, &a.Model, &a.Days); err != nil {
		return nil, err
	}
	if a.Days < 1 {
		a.Days = 1
	}
	a.Total = float64(a.Days) * a.DailyRate
	return &a, nil
}

// isoDate is a cheap shape check. The dates arriving here come from a date
// input, so this is guarding against an empty or malformed value reaching
// SQL date arithmetic — where a bad string silently produces NULL rather
// than an error — not against a hostile caller.
func isoDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, r := range s {
		if i == 4 || i == 7 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// CreateRentalAgreement books a car out. Refuses to double-book: the
// overlap check and the insert share one transaction, so two people
// booking the same car for the same week at the same moment cannot both
// win — the second sees the first and is turned away.
func (s *Store) CreateRentalAgreement(a RentalAgreement) (int64, error) {
	if !isoDate(a.StartsOn) || !isoDate(a.EndsOn) {
		return 0, fmt.Errorf("both a start and an end date are required")
	}
	if a.EndsOn < a.StartsOn {
		return 0, fmt.Errorf("the end date cannot be before the start date")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var status string
	var rate float64
	if err := tx.QueryRow(`SELECT status, daily_rate FROM rental_vehicles WHERE id = ?`, a.VehicleID).
		Scan(&status, &rate); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("no such rental car")
		}
		return 0, err
	}
	if status != RentalAvailable {
		return 0, fmt.Errorf("that car is marked %s and cannot be booked", status)
	}

	var clash int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM rental_agreements
		WHERE vehicle_id = ? AND `+rentalHoldsCar+`
		  AND starts_on <= ? AND ends_on >= ?`,
		a.VehicleID, a.EndsOn, a.StartsOn).Scan(&clash); err != nil {
		return 0, err
	}
	if clash > 0 {
		return 0, fmt.Errorf("that car is already booked over those dates")
	}

	// The rate is snapshotted from the car unless the caller set one
	// deliberately, so re-pricing the car tomorrow never rewrites what this
	// hire was agreed at.
	if a.DailyRate == 0 {
		a.DailyRate = rate
	}
	if a.Status == "" {
		a.Status = RentalBooked
	}

	res, err := tx.Exec(`INSERT INTO rental_agreements
		(vehicle_id, customer_id, starts_on, ends_on, status, daily_rate, notes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		a.VehicleID, a.CustomerID, a.StartsOn, a.EndsOn, a.Status, a.DailyRate, a.Notes)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// RentalAgreementsFilter narrows what RentalAgreements returns. Zero value
// means everything, newest first.
type RentalAgreementsFilter struct {
	Status     string // one status, or empty for all
	VehicleID  int64
	CustomerID int64
	// OnDate, when set, keeps only hires whose agreed dates span it — the
	// "who had what on the 14th" question.
	OnDate string
}

func (s *Store) RentalAgreements(f RentalAgreementsFilter) ([]RentalAgreementView, error) {
	where := []string{}
	var args []any
	if f.Status != "" {
		where = append(where, "a.status = ?")
		args = append(args, f.Status)
	}
	if f.VehicleID > 0 {
		where = append(where, "a.vehicle_id = ?")
		args = append(args, f.VehicleID)
	}
	if f.CustomerID > 0 {
		where = append(where, "a.customer_id = ?")
		args = append(args, f.CustomerID)
	}
	if isoDate(f.OnDate) {
		where = append(where, "a.starts_on <= ? AND a.ends_on >= ?")
		args = append(args, f.OnDate, f.OnDate)
	}

	q := rentalAgreementView
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY a.starts_on DESC, a.id DESC"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RentalAgreementView{}
	for rows.Next() {
		a, err := scanRentalAgreementView(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) RentalAgreement(id int64) (*RentalAgreementView, error) {
	row := s.db.QueryRow(rentalAgreementView+` WHERE a.id = ?`, id)
	return scanRentalAgreementView(row.Scan)
}

// SetRentalAgreementStatus moves a hire along: booked → out when the keys
// are handed over, out → returned when they come back. Returning stamps
// returned_on with today unless a date is given, which is what frees the
// car for the next booking.
func (s *Store) SetRentalAgreementStatus(id int64, status, returnedOn string) error {
	switch status {
	case RentalBooked, RentalOut, RentalCancelled:
		_, err := s.db.Exec(`UPDATE rental_agreements SET status = ?, returned_on = '' WHERE id = ?`,
			status, id)
		return err
	case RentalReturned:
		if returnedOn == "" {
			_, err := s.db.Exec(`UPDATE rental_agreements
				SET status = ?, returned_on = date('now') WHERE id = ?`, status, id)
			return err
		}
		if !isoDate(returnedOn) {
			return fmt.Errorf("returned date must be a date")
		}
		_, err := s.db.Exec(`UPDATE rental_agreements
			SET status = ?, returned_on = ? WHERE id = ?`, status, returnedOn, id)
		return err
	}
	return fmt.Errorf("unknown status %q", status)
}

func (s *Store) DeleteRentalAgreement(id int64) error {
	_, err := s.db.Exec(`DELETE FROM rental_agreements WHERE id = ?`, id)
	return err
}

// AvailableRentalVehicles answers the question the desk actually asks:
// what can I give someone between these two dates. A car qualifies if it
// is marked available and nothing already booked or out overlaps the
// range — the same overlap rule CreateRentalAgreement enforces, so what
// this offers is exactly what that will accept.
func (s *Store) AvailableRentalVehicles(start, end string) ([]RentalVehicle, error) {
	if !isoDate(start) || !isoDate(end) {
		return nil, fmt.Errorf("both a start and an end date are required")
	}
	if end < start {
		return nil, fmt.Errorf("the end date cannot be before the start date")
	}

	rows, err := s.db.Query(`SELECT `+rentalVehicleCols+` FROM rental_vehicles v
		WHERE v.status = 'available'
		  AND NOT EXISTS (
		        SELECT 1 FROM rental_agreements a
		        WHERE a.vehicle_id = v.id AND a.`+rentalHoldsCar+`
		          AND a.starts_on <= ? AND a.ends_on >= ?)
		ORDER BY v.registration`, end, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RentalVehicle{}
	for rows.Next() {
		v, err := scanRentalVehicle(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// RentalOverview is the summary the rentals front page opens on.
type RentalOverview struct {
	OutNow    int     `json:"out_now"`
	Overdue   int     `json:"overdue"`
	Upcoming  int     `json:"upcoming"`
	Available int     `json:"available"`
	Fleet     int     `json:"fleet"`
	Customers int     `json:"customers"`
	DueToday  int     `json:"due_today"`
	OutValue  float64 `json:"out_value"`
}

func (s *Store) RentalOverview() (*RentalOverview, error) {
	o := &RentalOverview{}
	err := s.db.QueryRow(`
		SELECT
		  (SELECT COUNT(1) FROM rental_agreements WHERE status = 'out'),
		  (SELECT COUNT(1) FROM rental_agreements WHERE status = 'out' AND ends_on < date('now')),
		  (SELECT COUNT(1) FROM rental_agreements WHERE status = 'booked' AND starts_on >= date('now')),
		  (SELECT COUNT(1) FROM rental_agreements WHERE status = 'out' AND ends_on = date('now')),
		  (SELECT COUNT(1) FROM rental_vehicles WHERE status = 'available'),
		  (SELECT COUNT(1) FROM rental_vehicles WHERE status <> 'retired'),
		  (SELECT COUNT(1) FROM rental_customers),
		  (SELECT COALESCE(SUM(daily_rate * (CAST(julianday(ends_on) - julianday(starts_on) AS INTEGER) + 1)), 0)
		     FROM rental_agreements WHERE status = 'out')`).
		Scan(&o.OutNow, &o.Overdue, &o.Upcoming, &o.DueToday,
			&o.Available, &o.Fleet, &o.Customers, &o.OutValue)
	if err != nil {
		return nil, err
	}
	return o, nil
}
