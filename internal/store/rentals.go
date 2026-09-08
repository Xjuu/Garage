package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
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
	Mileage      float64 `json:"mileage"`
	MOTExpires   string  `json:"mot_expires"`
	Status       string  `json:"status"`
	Notes        string  `json:"notes"`
	CreatedAt    string  `json:"created_at"`
}

const rentalVehicleCols = `id, registration, make, model, year, colour, daily_rate,
	mileage, mot_expires, status, notes, created_at`

func scanRentalVehicle(scan func(...any) error) (*RentalVehicle, error) {
	var v RentalVehicle
	if err := scan(&v.ID, &v.Registration, &v.Make, &v.Model, &v.Year,
		&v.Colour, &v.DailyRate, &v.Mileage, &v.MOTExpires, &v.Status,
		&v.Notes, &v.CreatedAt); err != nil {
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
		(registration, make, model, year, colour, daily_rate, mileage, mot_expires,
		 status, notes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		reg, strings.TrimSpace(v.Make), strings.TrimSpace(v.Model), strings.TrimSpace(v.Year),
		strings.TrimSpace(v.Colour), v.DailyRate, v.Mileage, strings.TrimSpace(v.MOTExpires),
		v.Status, v.Notes)
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
		    daily_rate = ?, mot_expires = ?, status = ?, notes = ?
		WHERE id = ?`,
		reg, strings.TrimSpace(v.Make), strings.TrimSpace(v.Model), strings.TrimSpace(v.Year),
		strings.TrimSpace(v.Colour), v.DailyRate, strings.TrimSpace(v.MOTExpires),
		v.Status, v.Notes, id)
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
	ID              int64   `json:"id"`
	VehicleID       int64   `json:"vehicle_id"`
	CustomerID      int64   `json:"customer_id"`
	StartsOn        string  `json:"starts_on"`
	EndsOn          string  `json:"ends_on"`
	ReturnedOn      string  `json:"returned_on"`
	Status          string  `json:"status"`
	DailyRate       float64 `json:"daily_rate"`
	MileageOut      float64 `json:"mileage_out"`
	CourtesyForReg  string  `json:"courtesy_for_reg"`
	InsurancePerDay float64 `json:"insurance_per_day"`
	LateFeePerDay   float64 `json:"late_fee_per_day"`
	LateFee         float64 `json:"late_fee"`
	ExtraCharges    float64 `json:"extra_charges"`
	ExtraNote       string  `json:"extra_note"`
	Deposit         float64 `json:"deposit"`
	DepositReturned bool    `json:"deposit_returned"`
	Paid            bool    `json:"paid"`
	PaymentSession  string  `json:"payment_session"`
	PaymentURL      string  `json:"payment_url"`
	ReadyTextedAt   string  `json:"ready_texted_at"`
	Notes           string  `json:"notes"`
	CreatedAt       string  `json:"created_at"`
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
	// Total is Days × DailyRate — the hire itself, before anything else.
	Total float64 `json:"total"`
	// Insurance is Days × InsurancePerDay.
	Insurance float64 `json:"insurance"`
	// DaysLate is how far past the agreed end this went — against today
	// while the car is still out, against the day it came back once it is.
	DaysLate int `json:"days_late"`
	// LateFeeDue is what that lateness comes to at the agreed rate; LateFee
	// (stored) is what someone has actually decided to charge.
	LateFeeDue float64 `json:"late_fee_due"`
	// Chargeable is the one number that matters: hire, insurance, any late
	// fee actually applied, and any extras.
	Chargeable float64 `json:"chargeable"`
}

const rentalAgreementView = `
	SELECT a.id, a.vehicle_id, a.customer_id, a.starts_on, a.ends_on, a.returned_on,
	       a.status, a.daily_rate, a.mileage_out, a.courtesy_for_reg,
	       a.insurance_per_day, a.late_fee_per_day, a.late_fee, a.extra_charges,
	       a.extra_note, a.deposit, a.deposit_returned,
	       a.paid, a.payment_session, a.payment_url, a.ready_texted_at, a.notes, a.created_at,
	       c.name, c.phone, v.registration, v.make, v.model,
	       CAST(julianday(a.ends_on) - julianday(a.starts_on) AS INTEGER) + 1
	FROM rental_agreements a
	JOIN rental_customers c ON c.id = a.customer_id
	JOIN rental_vehicles  v ON v.id = a.vehicle_id`

func scanRentalAgreementView(scan func(...any) error) (*RentalAgreementView, error) {
	var a RentalAgreementView
	var paid, depositReturned int
	if err := scan(&a.ID, &a.VehicleID, &a.CustomerID, &a.StartsOn, &a.EndsOn, &a.ReturnedOn,
		&a.Status, &a.DailyRate, &a.MileageOut, &a.CourtesyForReg,
		&a.InsurancePerDay, &a.LateFeePerDay, &a.LateFee, &a.ExtraCharges,
		&a.ExtraNote, &a.Deposit, &depositReturned,
		&paid, &a.PaymentSession, &a.PaymentURL, &a.ReadyTextedAt, &a.Notes, &a.CreatedAt,
		&a.CustomerName, &a.Phone, &a.Registration, &a.Make, &a.Model, &a.Days); err != nil {
		return nil, err
	}
	a.Paid = paid != 0
	a.DepositReturned = depositReturned != 0
	if a.Days < 1 {
		a.Days = 1
	}
	a.Total = float64(a.Days) * a.DailyRate
	a.priceUp()
	return &a, nil
}

// priceUp fills in everything derived from the stored figures: what the
// insurance comes to, how late the car is, what that lateness would cost,
// and the single number someone actually has to pay.
//
// Lateness is worked out here rather than in SQL because it has two
// different clocks — a car still out is late against today, one already
// back is late against the day it came back — and expressing that twice in
// two dialects is how two screens come to disagree about a debt.
func (a *RentalAgreementView) priceUp() {
	a.Insurance = float64(a.Days) * a.InsurancePerDay

	end, err := time.Parse("2006-01-02", a.EndsOn)
	if err == nil && a.Status != RentalCancelled {
		against := time.Now()
		if a.ReturnedOn != "" {
			if back, err := time.Parse("2006-01-02", a.ReturnedOn); err == nil {
				against = back
			}
		}
		if d := int(against.Sub(end).Hours() / 24); d > 0 {
			a.DaysLate = d
		}
	}
	// What lateness WOULD cost. Deliberately not the same field as LateFee:
	// an overdue car should show what it is running up without that
	// silently becoming a debt nobody has agreed to charge.
	a.LateFeeDue = float64(a.DaysLate) * a.LateFeePerDay

	a.Chargeable = a.Total + a.Insurance + a.LateFee + a.ExtraCharges
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
		(vehicle_id, customer_id, starts_on, ends_on, status, daily_rate,
		 mileage_out, courtesy_for_reg, insurance_per_day, late_fee_per_day,
		 deposit, notes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		a.VehicleID, a.CustomerID, a.StartsOn, a.EndsOn, a.Status, a.DailyRate,
		a.MileageOut, NormalizeReg(a.CourtesyForReg),
		a.InsurancePerDay, a.LateFeePerDay, a.Deposit, a.Notes)
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

// LendCar is the counter action the whole hire desk turns on: this car,
// this customer, back on this date, handed over now. It is deliberately not
// CreateRentalAgreement with today's date filled in — the difference is
// that a car being lent out is going out of the door as the button is
// pressed, so it starts today, opens as 'out' rather than 'booked', and
// takes the odometer reading and any damage note that only exist at the
// moment the keys change hands.
//
// courtesyForReg, when set, is the customer's OWN car sitting in the
// workshop — what makes this a courtesy car rather than a plain hire.
// LendRequest is everything agreed at the counter. A struct rather than
// nine positional arguments: half of them are money, and a caller swapping
// two floats by accident is not a mistake any compiler would catch.
type LendRequest struct {
	VehicleID      int64   `json:"vehicle_id"`
	CustomerID     int64   `json:"customer_id"`
	BackOn         string  `json:"back_on"`
	MileageNow     float64 `json:"mileage_now"`
	CourtesyForReg string  `json:"courtesy_for_reg"`
	Note           string  `json:"note"`
	// Rates agreed now and snapshotted onto the hire, so tomorrow's price
	// list never rewrites today's agreement.
	InsurancePerDay float64 `json:"insurance_per_day"`
	LateFeePerDay   float64 `json:"late_fee_per_day"`
	Deposit         float64 `json:"deposit"`
}

func (s *Store) LendCar(req LendRequest) (int64, error) {
	vehicleID, customerID := req.VehicleID, req.CustomerID
	backOn, mileageNow := req.BackOn, req.MileageNow
	courtesyForReg, note := req.CourtesyForReg, req.Note
	if req.InsurancePerDay < 0 || req.LateFeePerDay < 0 || req.Deposit < 0 {
		return 0, fmt.Errorf("rates and deposits cannot be negative")
	}
	if !isoDate(backOn) {
		return 0, fmt.Errorf("a date for bringing it back is required")
	}
	today := time.Now().Format("2006-01-02")
	if backOn < today {
		return 0, fmt.Errorf("the return date has already passed")
	}
	if mileageNow < 0 {
		return 0, fmt.Errorf("mileage cannot be negative")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var status string
	var rate, onTheClock float64
	if err := tx.QueryRow(`SELECT status, daily_rate, mileage FROM rental_vehicles WHERE id = ?`,
		vehicleID).Scan(&status, &rate, &onTheClock); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("no such loan car")
		}
		return 0, err
	}
	if status != RentalAvailable {
		return 0, fmt.Errorf("that car is marked %s and cannot go out", status)
	}

	// An odometer only goes up. Same guard the repairs site applies to a
	// service visit, for the same reason: the reading is almost always a
	// typo rather than a car that has travelled backwards.
	if mileageNow > 0 && mileageNow < onTheClock {
		return 0, fmt.Errorf("mileage cannot be lower than the %g already recorded for this car", onTheClock)
	}

	var clash int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM rental_agreements
		WHERE vehicle_id = ? AND `+rentalHoldsCar+`
		  AND starts_on <= ? AND ends_on >= ?`,
		vehicleID, backOn, today).Scan(&clash); err != nil {
		return 0, err
	}
	if clash > 0 {
		return 0, fmt.Errorf("that car is already out or booked over those dates")
	}

	res, err := tx.Exec(`INSERT INTO rental_agreements
		(vehicle_id, customer_id, starts_on, ends_on, status, daily_rate,
		 mileage_out, courtesy_for_reg, insurance_per_day, late_fee_per_day,
		 deposit, notes, created_at)
		VALUES (?, ?, ?, ?, 'out', ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		vehicleID, customerID, today, backOn, rate,
		mileageNow, NormalizeReg(courtesyForReg),
		req.InsurancePerDay, req.LateFeePerDay, req.Deposit, strings.TrimSpace(note))
	if err != nil {
		return 0, err
	}
	// The reading taken at the counter is the car's mileage from now on —
	// otherwise the figure on the forecourt list drifts further from
	// reality with every loan.
	if mileageNow > 0 {
		if _, err := tx.Exec(`UPDATE rental_vehicles SET mileage = ? WHERE id = ?`,
			mileageNow, vehicleID); err != nil {
			return 0, err
		}
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// BringCarBack closes a loan and records the odometer as it came in.
func (s *Store) BringCarBack(agreementID int64, mileageIn float64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var vehicleID int64
	var onTheClock float64
	if err := tx.QueryRow(`SELECT a.vehicle_id, v.mileage
		FROM rental_agreements a JOIN rental_vehicles v ON v.id = a.vehicle_id
		WHERE a.id = ?`, agreementID).Scan(&vehicleID, &onTheClock); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("no such loan")
		}
		return err
	}
	if mileageIn > 0 && mileageIn < onTheClock {
		return fmt.Errorf("mileage cannot be lower than the %g already recorded for this car", onTheClock)
	}

	if _, err := tx.Exec(`UPDATE rental_agreements
		SET status = 'returned', returned_on = date('now') WHERE id = ?`, agreementID); err != nil {
		return err
	}
	if mileageIn > 0 {
		if _, err := tx.Exec(`UPDATE rental_vehicles SET mileage = ? WHERE id = ?`,
			mileageIn, vehicleID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RentalBoard is the hire desk's front page: the two columns it is made of.
// Free to lend is every car that could go out of the door right now —
// available, and not already spoken for today.
type RentalBoard struct {
	Free []RentalVehicle       `json:"free"`
	Out  []RentalAgreementView `json:"out"`
}

func (s *Store) RentalBoard() (*RentalBoard, error) {
	today := time.Now().Format("2006-01-02")
	free, err := s.AvailableRentalVehicles(today, today)
	if err != nil {
		return nil, err
	}
	out, err := s.RentalAgreements(RentalAgreementsFilter{Status: RentalOut})
	if err != nil {
		return nil, err
	}
	return &RentalBoard{Free: free, Out: out}, nil
}

// ── payment and messages ──────────────────────────────────────────────────

// StartRentalPayment records the checkout page opened for a hire. The URL
// is kept so the same link can be sent again — pressing the button twice
// should hand over the same page, not open a second one Stripe would then
// be waiting on forever.
func (s *Store) StartRentalPayment(agreementID int64, sessionID, url string) error {
	_, err := s.db.Exec(`UPDATE rental_agreements
		SET payment_session = ?, payment_url = ? WHERE id = ?`, sessionID, url, agreementID)
	return err
}

// MarkRentalPaid is called once Stripe confirms the money arrived.
func (s *Store) MarkRentalPaid(agreementID int64) error {
	_, err := s.db.Exec(`UPDATE rental_agreements SET paid = 1 WHERE id = ?`, agreementID)
	return err
}

// MarkReadyTexted stamps when the "your car is ready" message went out, so
// the desk can see it has been sent rather than sending it again.
func (s *Store) MarkReadyTexted(agreementID int64) error {
	_, err := s.db.Exec(`UPDATE rental_agreements
		SET ready_texted_at = datetime('now') WHERE id = ?`, agreementID)
	return err
}

// ── statistics ────────────────────────────────────────────────────────────

// RentalStats is the money view: what is on hire right now, what has been
// earned, and which cars are actually earning it.
type RentalStats struct {
	// OnHireNow is the value of every hire currently out — money committed,
	// not yet necessarily collected.
	OnHireNow float64 `json:"on_hire_now"`
	// Billed counts every hire that was not cancelled; Collected is the part
	// Stripe has actually confirmed. The gap between them is what is owed.
	BilledAllTime    float64 `json:"billed_all_time"`
	CollectedAllTime float64 `json:"collected_all_time"`
	OutstandingNow   float64 `json:"outstanding_now"`
	BilledThisMonth  float64 `json:"billed_this_month"`
	HiresThisMonth   int     `json:"hires_this_month"`
	AvgHireDays      float64 `json:"avg_hire_days"`
	AvgHireValue     float64 `json:"avg_hire_value"`
	// UtilisationPct is the share of the loan fleet out on hire right now —
	// the number that says whether more cars are needed or fewer.
	UtilisationPct float64          `json:"utilisation_pct"`
	TopCars        []RentalCarStats `json:"top_cars"`
}

// RentalCarStats is one car's earning record.
type RentalCarStats struct {
	VehicleID    int64   `json:"vehicle_id"`
	Registration string  `json:"registration"`
	Make         string  `json:"make"`
	Model        string  `json:"model"`
	Hires        int     `json:"hires"`
	Days         int     `json:"days"`
	Billed       float64 `json:"billed"`
}

// hireValue is the agreed length in whole days, inclusive of both ends,
// times the rate. A function rather than a constant because one of the
// queries below joins rental_vehicles, which has a daily_rate of its own —
// the alias has to be explicit there, and having two hand-written copies of
// this arithmetic is exactly how two figures on one screen end up
// disagreeing. Pass "" when only one table is in scope.
func hireValue(alias string) string {
	if alias != "" {
		alias += "."
	}
	return alias + `daily_rate * (CAST(julianday(` + alias + `ends_on) - julianday(` +
		alias + `starts_on) AS INTEGER) + 1)`
}

func (s *Store) RentalStats() (*RentalStats, error) {
	st := &RentalStats{}
	err := s.db.QueryRow(`
		SELECT
		  (SELECT COALESCE(SUM(`+hireValue("")+`),0) FROM rental_agreements WHERE status = 'out'),
		  (SELECT COALESCE(SUM(`+hireValue("")+`),0) FROM rental_agreements WHERE status <> 'cancelled'),
		  (SELECT COALESCE(SUM(`+hireValue("")+`),0) FROM rental_agreements WHERE status <> 'cancelled' AND paid = 1),
		  (SELECT COALESCE(SUM(`+hireValue("")+`),0) FROM rental_agreements
		     WHERE status <> 'cancelled' AND strftime('%Y-%m', starts_on) = strftime('%Y-%m', 'now')),
		  (SELECT COUNT(1) FROM rental_agreements
		     WHERE status <> 'cancelled' AND strftime('%Y-%m', starts_on) = strftime('%Y-%m', 'now')),
		  (SELECT COALESCE(AVG(CAST(julianday(ends_on) - julianday(starts_on) AS INTEGER) + 1),0)
		     FROM rental_agreements WHERE status <> 'cancelled'),
		  (SELECT COALESCE(AVG(`+hireValue("")+`),0) FROM rental_agreements WHERE status <> 'cancelled'),
		  (SELECT COUNT(1) FROM rental_vehicles WHERE status <> 'retired')`).
		Scan(&st.OnHireNow, &st.BilledAllTime, &st.CollectedAllTime,
			&st.BilledThisMonth, &st.HiresThisMonth, &st.AvgHireDays, &st.AvgHireValue,
			new(int))
	if err != nil {
		return nil, err
	}
	st.OutstandingNow = st.BilledAllTime - st.CollectedAllTime

	// Utilisation is worked out separately rather than squeezed into the row
	// above: dividing by a fleet of zero is a real state on a fresh install.
	var fleet, out int
	if err := s.db.QueryRow(`SELECT
		(SELECT COUNT(1) FROM rental_vehicles WHERE status <> 'retired'),
		(SELECT COUNT(1) FROM rental_agreements WHERE status = 'out')`).Scan(&fleet, &out); err != nil {
		return nil, err
	}
	if fleet > 0 {
		st.UtilisationPct = float64(out) / float64(fleet) * 100
	}

	rows, err := s.db.Query(`
		SELECT v.id, v.registration, v.make, v.model,
		       COUNT(a.id),
		       COALESCE(SUM(CAST(julianday(a.ends_on) - julianday(a.starts_on) AS INTEGER) + 1), 0),
		       COALESCE(SUM(` + hireValue("a") + `), 0)
		FROM rental_vehicles v
		LEFT JOIN rental_agreements a ON a.vehicle_id = v.id AND a.status <> 'cancelled'
		GROUP BY v.id
		ORDER BY 7 DESC, v.registration
		LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	st.TopCars = []RentalCarStats{}
	for rows.Next() {
		var c RentalCarStats
		if err := rows.Scan(&c.VehicleID, &c.Registration, &c.Make, &c.Model,
			&c.Hires, &c.Days, &c.Billed); err != nil {
			return nil, err
		}
		st.TopCars = append(st.TopCars, c)
	}
	return st, rows.Err()
}

// CourtesyLoans answers the question this feature exists for: whose car is
// in the workshop, and what are they driving in the meantime. Only loans
// that are actually out, and only those standing in for a specific car.
func (s *Store) CourtesyLoans() ([]RentalAgreementView, error) {
	rows, err := s.db.Query(rentalAgreementView + `
		WHERE a.status = 'out' AND a.courtesy_for_reg <> ''
		ORDER BY a.starts_on`)
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

// ── charges ───────────────────────────────────────────────────────────────

// RentalCharges is what the desk can change about a hire's money after it
// has started: the extras it picked up, and whether the deposit went back.
// Rates (insurance, late fee per day) are deliberately absent — those were
// agreed when the keys were handed over and re-pricing them afterwards is
// not an edit, it is a different agreement.
type RentalCharges struct {
	ExtraCharges float64 `json:"extra_charges"`
	ExtraNote    string  `json:"extra_note"`
}

func (s *Store) SetRentalCharges(agreementID int64, c RentalCharges) error {
	if c.ExtraCharges < 0 {
		return fmt.Errorf("an extra charge cannot be negative — take the deposit back instead")
	}
	_, err := s.db.Exec(`UPDATE rental_agreements
		SET extra_charges = ?, extra_note = ? WHERE id = ?`,
		c.ExtraCharges, strings.TrimSpace(c.ExtraNote), agreementID)
	return err
}

// ApplyLateFee turns what a late car is running up into an actual charge.
// It is an explicit act rather than something that accrues on its own: a
// customer who rang ahead is not to be billed by a background job.
//
// Passing 0 waives it, which is the other half of the same decision.
func (s *Store) ApplyLateFee(agreementID int64, amount float64) error {
	if amount < 0 {
		return fmt.Errorf("a late fee cannot be negative")
	}
	_, err := s.db.Exec(`UPDATE rental_agreements SET late_fee = ? WHERE id = ?`,
		amount, agreementID)
	return err
}

// SetDepositReturned records handing the deposit back. Separate from
// payment: a deposit is held, not earned, and it never belonged in any
// figure of what the business took.
func (s *Store) SetDepositReturned(agreementID int64, returned bool) error {
	_, err := s.db.Exec(`UPDATE rental_agreements SET deposit_returned = ? WHERE id = ?`,
		boolToInt(returned), agreementID)
	return err
}

// ── messages ──────────────────────────────────────────────────────────────

// RentalMessage is one text sent to a customer, kept whether or not it got
// through — a failure is the more useful record of the two.
type RentalMessage struct {
	ID          int64  `json:"id"`
	CustomerID  int64  `json:"customer_id"`
	AgreementID *int64 `json:"agreement_id"`
	Phone       string `json:"phone"`
	Body        string `json:"body"`
	ProviderSID string `json:"provider_sid"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	SentBy      string `json:"sent_by"`
	CreatedAt   string `json:"created_at"`
	// Joined for display, so a log line reads without a second lookup.
	CustomerName string `json:"customer_name"`
}

// LogRentalMessage records an attempt. Called for successes and failures
// alike: "we texted them and Twilio bounced it" is exactly the thing
// somebody needs to see when a customer says nobody told them.
func (s *Store) LogRentalMessage(m RentalMessage) (int64, error) {
	var agreement any
	if m.AgreementID != nil && *m.AgreementID > 0 {
		agreement = *m.AgreementID
	}
	if m.Status == "" {
		m.Status = "sent"
	}
	res, err := s.db.Exec(`INSERT INTO rental_messages
		(customer_id, agreement_id, phone, body, provider_sid, status, error, sent_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		m.CustomerID, agreement, m.Phone, m.Body, m.ProviderSID, m.Status, m.Error, m.SentBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RentalMessages lists what was sent, newest first — everything, or just
// one customer's.
func (s *Store) RentalMessages(customerID int64, limit int) ([]RentalMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT m.id, m.customer_id, m.agreement_id, m.phone, m.body, m.provider_sid,
	             m.status, m.error, m.sent_by, m.created_at, c.name
	      FROM rental_messages m
	      JOIN rental_customers c ON c.id = m.customer_id`
	args := []any{}
	if customerID > 0 {
		q += ` WHERE m.customer_id = ?`
		args = append(args, customerID)
	}
	q += ` ORDER BY m.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RentalMessage{}
	for rows.Next() {
		var m RentalMessage
		var agreement sql.NullInt64
		if err := rows.Scan(&m.ID, &m.CustomerID, &agreement, &m.Phone, &m.Body,
			&m.ProviderSID, &m.Status, &m.Error, &m.SentBy, &m.CreatedAt,
			&m.CustomerName); err != nil {
			return nil, err
		}
		if agreement.Valid {
			id := agreement.Int64
			m.AgreementID = &id
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ── documents ─────────────────────────────────────────────────────────────

// Document kinds. Free text would make these unfilterable within a week;
// these cover what a hire desk actually scans.
const (
	DocLicence   = "licence"
	DocAgreement = "agreement"
	DocInsurance = "insurance"
	DocDamage    = "damage"
	DocOther     = "other"
)

func ValidDocumentKind(k string) bool {
	switch k {
	case DocLicence, DocAgreement, DocInsurance, DocDamage, DocOther:
		return true
	}
	return false
}

// RentalDocument is a file attached to a customer or a hire. The file
// itself is on disk; this is only where it is and what it is.
type RentalDocument struct {
	ID          int64  `json:"id"`
	CustomerID  *int64 `json:"customer_id"`
	AgreementID *int64 `json:"agreement_id"`
	Kind        string `json:"kind"`
	Filename    string `json:"filename"`
	StoredPath  string `json:"-"` // never sent to a browser: it is a server path
	Mime        string `json:"mime"`
	Bytes       int64  `json:"bytes"`
	UploadedBy  string `json:"uploaded_by"`
	CreatedAt   string `json:"created_at"`
}

func (s *Store) AddRentalDocument(d RentalDocument) (int64, error) {
	if strings.TrimSpace(d.Filename) == "" || strings.TrimSpace(d.StoredPath) == "" {
		return 0, fmt.Errorf("a document needs a file")
	}
	if !ValidDocumentKind(d.Kind) {
		d.Kind = DocOther
	}
	if (d.CustomerID == nil || *d.CustomerID == 0) && (d.AgreementID == nil || *d.AgreementID == 0) {
		return 0, fmt.Errorf("a document has to belong to a customer or a hire")
	}

	var customer, agreement any
	if d.CustomerID != nil && *d.CustomerID > 0 {
		customer = *d.CustomerID
	}
	if d.AgreementID != nil && *d.AgreementID > 0 {
		agreement = *d.AgreementID
	}
	res, err := s.db.Exec(`INSERT INTO rental_documents
		(customer_id, agreement_id, kind, filename, stored_path, mime, bytes, uploaded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		customer, agreement, d.Kind, d.Filename, d.StoredPath, d.Mime, d.Bytes, d.UploadedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RentalDocuments lists what is attached to one customer or one hire. A
// hire's documents include the customer's own — their licence is as
// relevant to this loan as it was to the last one.
func (s *Store) RentalDocuments(customerID, agreementID int64) ([]RentalDocument, error) {
	where := []string{}
	args := []any{}
	if customerID > 0 {
		where = append(where, "customer_id = ?")
		args = append(args, customerID)
	}
	if agreementID > 0 {
		where = append(where, "agreement_id = ?")
		args = append(args, agreementID)
	}
	if len(where) == 0 {
		return []RentalDocument{}, nil
	}

	rows, err := s.db.Query(`SELECT id, customer_id, agreement_id, kind, filename,
		stored_path, mime, bytes, uploaded_by, created_at
		FROM rental_documents WHERE `+strings.Join(where, " OR ")+`
		ORDER BY id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RentalDocument{}
	for rows.Next() {
		var d RentalDocument
		var cust, agr sql.NullInt64
		if err := rows.Scan(&d.ID, &cust, &agr, &d.Kind, &d.Filename,
			&d.StoredPath, &d.Mime, &d.Bytes, &d.UploadedBy, &d.CreatedAt); err != nil {
			return nil, err
		}
		if cust.Valid {
			id := cust.Int64
			d.CustomerID = &id
		}
		if agr.Valid {
			id := agr.Int64
			d.AgreementID = &id
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RentalDocument(id int64) (*RentalDocument, error) {
	var d RentalDocument
	var cust, agr sql.NullInt64
	err := s.db.QueryRow(`SELECT id, customer_id, agreement_id, kind, filename,
		stored_path, mime, bytes, uploaded_by, created_at
		FROM rental_documents WHERE id = ?`, id).
		Scan(&d.ID, &cust, &agr, &d.Kind, &d.Filename, &d.StoredPath, &d.Mime,
			&d.Bytes, &d.UploadedBy, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	if cust.Valid {
		v := cust.Int64
		d.CustomerID = &v
	}
	if agr.Valid {
		v := agr.Int64
		d.AgreementID = &v
	}
	return &d, nil
}

// DeleteRentalDocument removes the row and hands back the path, so the
// caller can delete the file too. The row goes first: a file left on disk
// with nothing pointing at it is litter, but a row pointing at a file that
// is gone is a broken link someone will click.
func (s *Store) DeleteRentalDocument(id int64) (storedPath string, err error) {
	if err := s.db.QueryRow(`SELECT stored_path FROM rental_documents WHERE id = ?`, id).
		Scan(&storedPath); err != nil {
		return "", err
	}
	_, err = s.db.Exec(`DELETE FROM rental_documents WHERE id = ?`, id)
	return storedPath, err
}
