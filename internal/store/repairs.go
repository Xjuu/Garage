package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ── repairs ──────────────────────────────────────────────────────────────

// Repair is one service/repair visit logged from repairs.<domain>.
type Repair struct {
	ID                int64   `json:"id"`
	VehicleReg        string  `json:"vehicle_reg"`
	ServiceDate       string  `json:"service_date"`
	ServiceType       string  `json:"service_type"`       // "full", "mini", or "other"
	ServiceTypeOther  string  `json:"service_type_other"` // free text when ServiceType == "other"
	Mileage           float64 `json:"mileage"`
	TimingBeltChanged bool    `json:"timing_belt_changed"`
	Description       string  `json:"description"`
	VIN               string  `json:"vin"`
	Make              string  `json:"make"`
	Model             string  `json:"model"`
	Colour            string  `json:"colour"`
	CylinderCapacity  string  `json:"cylinder_capacity"`
	SpareKeys         string  `json:"spare_keys"`
	FuelType          string  `json:"fuel_type"`
	EngineSize        string  `json:"engine_size"`
	EngineNumber      string  `json:"engine_number"`
	TyreSize          string  `json:"tyre_size"`
	RadioCode         string  `json:"radio_code"`
	OilAmount         string  `json:"oil_amount"`
	DeviceID          string  `json:"device_id,omitempty"`
	DeviceName        string  `json:"device_name,omitempty"`
	CreatedAt         string  `json:"created_at"`
}

var serviceTypes = map[string]bool{"full": true, "mini": true, "other": true}

// LogRepair records one visit and folds whatever vehicle-spec fields it
// carries (make, model, VIN, colour, ...) into the vehicle's registry row,
// so the latest known spec is visible everywhere else in the dashboard too,
// not just buried in this one visit's record.
//
// r.ServiceDate is normally left empty by the worker app — this is "log
// what happened today" — and defaults to right now. The historical import
// is the one caller that sets it explicitly, since backfilling years of
// visits as though they all happened on import day would make every
// "last serviced" and "last timing belt" reading on the dashboard wrong.
func (s *Store) LogRepair(r Repair, deviceID string) (int64, error) {
	reg := NormalizeReg(r.VehicleReg)
	if reg == "" {
		return 0, fmt.Errorf("vehicle registration is required")
	}
	r.ServiceType = strings.ToLower(strings.TrimSpace(r.ServiceType))
	if !serviceTypes[r.ServiceType] {
		return 0, fmt.Errorf("service type must be full, mini, or other")
	}
	if r.ServiceType == "other" && strings.TrimSpace(r.ServiceTypeOther) == "" {
		return 0, fmt.Errorf("describe the service type when \"other\" is selected")
	}
	if r.ServiceType != "other" {
		r.ServiceTypeOther = ""
	}
	serviceDate := strings.TrimSpace(r.ServiceDate)
	if serviceDate == "" {
		serviceDate = now()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO repairs (
			vehicle_reg, service_date, service_type, service_type_other, mileage,
			timing_belt_changed, description, vin, make, model, colour,
			cylinder_capacity, spare_keys, fuel_type, engine_size, engine_number, tyre_size,
			radio_code, oil_amount, device_id, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		reg, serviceDate, r.ServiceType, r.ServiceTypeOther, r.Mileage,
		boolToInt(r.TimingBeltChanged), strings.TrimSpace(r.Description),
		strings.TrimSpace(r.VIN), strings.TrimSpace(r.Make), strings.TrimSpace(r.Model),
		strings.TrimSpace(r.Colour), strings.TrimSpace(r.CylinderCapacity),
		strings.TrimSpace(r.SpareKeys), strings.TrimSpace(r.FuelType),
		strings.TrimSpace(r.EngineSize), strings.TrimSpace(r.EngineNumber), strings.TrimSpace(r.TyreSize),
		strings.TrimSpace(r.RadioCode), strings.TrimSpace(r.OilAmount),
		deviceID, now())
	if err != nil {
		return 0, fmt.Errorf("insert repair: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if err := ensureVehicleRegistered(tx, reg); err != nil {
		return 0, fmt.Errorf("register vehicle: %w", err)
	}
	if err := updateVehicleSpecTx(tx, reg, VehicleSpecPatch{
		Make: r.Make, Model: r.Model, VIN: r.VIN, Colour: r.Colour,
		CylinderCapacity: r.CylinderCapacity, FuelType: r.FuelType,
		EngineSize: r.EngineSize, EngineNumber: r.EngineNumber, TyreSize: r.TyreSize,
		RadioCode: r.RadioCode, SpareKeys: r.SpareKeys,
	}); err != nil {
		return 0, fmt.Errorf("update vehicle spec: %w", err)
	}

	return id, tx.Commit()
}

// ListRepairsForVehicle is the service history for one car, newest first by
// when the visit actually happened — used both by the worker app (type a
// reg, see what's been done before, and prefill the spec fields from the
// most recent visit) and the dashboard's vehicle drawer.
//
// Ordered by service_date, not insertion id: a live-logged visit is always
// inserted in chronological order, but the historical import inserted rows
// in the source spreadsheet's own row order, which is not guaranteed
// chronological (some vehicles' sheets have a later visit's row placed
// above an earlier one). Sorting by id would silently put the wrong visit
// first for exactly those vehicles.
func (s *Store) ListRepairsForVehicle(reg string) ([]Repair, error) {
	reg = NormalizeReg(reg)
	rows, err := s.db.Query(`
		SELECT r.id, r.vehicle_reg, r.service_date, r.service_type, r.service_type_other,
		       r.mileage, r.timing_belt_changed, r.description, r.vin, r.make, r.model,
		       r.colour, r.cylinder_capacity, r.spare_keys, r.fuel_type, r.engine_size, r.engine_number,
		       r.tyre_size, r.radio_code, r.oil_amount, r.device_id,
		       COALESCE(rd.label, ''), r.created_at
		FROM repairs r
		LEFT JOIN repairs_devices rd ON rd.id = r.device_id
		WHERE r.vehicle_reg = ?
		ORDER BY r.service_date DESC, r.id DESC`, reg)
	if err != nil {
		return nil, err
	}
	return scanRepairs(rows)
}

// RecentRepairs is the admin audit view — every visit logged, across every
// vehicle, newest first.
func (s *Store) RecentRepairs(limit int) ([]Repair, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT r.id, r.vehicle_reg, r.service_date, r.service_type, r.service_type_other,
		       r.mileage, r.timing_belt_changed, r.description, r.vin, r.make, r.model,
		       r.colour, r.cylinder_capacity, r.spare_keys, r.fuel_type, r.engine_size, r.engine_number,
		       r.tyre_size, r.radio_code, r.oil_amount, r.device_id,
		       COALESCE(rd.label, ''), r.created_at
		FROM repairs r
		LEFT JOIN repairs_devices rd ON rd.id = r.device_id
		ORDER BY r.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanRepairs(rows)
}

func scanRepairs(rows *sql.Rows) ([]Repair, error) {
	defer rows.Close()
	out := []Repair{}
	for rows.Next() {
		var r Repair
		var beltChanged int
		if err := rows.Scan(&r.ID, &r.VehicleReg, &r.ServiceDate, &r.ServiceType, &r.ServiceTypeOther,
			&r.Mileage, &beltChanged, &r.Description, &r.VIN, &r.Make, &r.Model,
			&r.Colour, &r.CylinderCapacity, &r.SpareKeys, &r.FuelType, &r.EngineSize, &r.EngineNumber,
			&r.TyreSize, &r.RadioCode, &r.OilAmount, &r.DeviceID, &r.DeviceName, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		r.TimingBeltChanged = beltChanged != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastTimingBeltChange reports the most recent service_date among visits
// that recorded a timing belt change for this vehicle — kept as its own
// lookup because a workshop tracks a timing belt's interval separately from
// "when was this car last seen at all".
func (s *Store) LastTimingBeltChange(reg string) (date string, found bool, err error) {
	reg = NormalizeReg(reg)
	var d sql.NullString
	err = s.db.QueryRow(`
		SELECT MAX(service_date) FROM repairs WHERE vehicle_reg = ? AND timing_belt_changed = 1`, reg).
		Scan(&d)
	if err != nil {
		return "", false, err
	}
	return d.String, d.Valid, nil
}

// SearchRepairVehicles finds registrations from the fleet registry — an
// empty q matches everything, the "browse all" case the worker app's search
// box supports the same way the (now-removed) parts counter's did.
func (s *Store) SearchRepairVehicles(q string, limit int) ([]string, error) {
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	q = strings.ToUpper(strings.TrimSpace(q))
	like := "%" + q + "%"
	rows, err := s.db.Query(`
		SELECT registration FROM vehicles
		WHERE registration LIKE ? ORDER BY registration LIMIT ?`, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var reg string
		if err := rows.Scan(&reg); err != nil {
			return nil, err
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

// RegExists reports whether a registration is known to the system at all —
// in the vehicle registry, on an invoice, or already in the repairs log —
// so repairs.<domain> can ask "add this as a new registration?" before
// treating an unrecognized plate as fair game. The same protection
// NormalizeReg's confusable-character fix exists for: a typo must never
// silently open a second history for a car that already has one, and a
// genuinely new vehicle should be added on purpose, not by accident.
func (s *Store) RegExists(reg string) (bool, error) {
	reg = NormalizeReg(reg)
	if reg == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRow(`
		SELECT (SELECT COUNT(1) FROM vehicles WHERE registration = ?)
		     + (SELECT COUNT(1) FROM invoices WHERE vehicle_reg = ?)
		     + (SELECT COUNT(1) FROM repairs WHERE vehicle_reg = ?)`,
		reg, reg, reg).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ── vehicle spec ─────────────────────────────────────────────────────────

// VehicleSpecPatch updates only the fields actually given — unlike
// SaveVehicle's full upsert, a blank field here leaves whatever was already
// on file alone, since a repair visit rarely fills in every spec field at
// once and must never blank out ones a previous visit already recorded.
type VehicleSpecPatch struct {
	Make, Model, VIN, Colour                         string
	CylinderCapacity, FuelType, EngineSize, TyreSize string
	RadioCode, SpareKeys, EngineNumber               string
}

// UpdateVehicleSpec applies a partial spec update, registering the vehicle
// first if this is the first time it's been seen.
func (s *Store) UpdateVehicleSpec(reg string, p VehicleSpecPatch) error {
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
	if err := updateVehicleSpecTx(tx, reg, p); err != nil {
		return err
	}
	return tx.Commit()
}

func updateVehicleSpecTx(tx *sql.Tx, reg string, p VehicleSpecPatch) error {
	var sets []string
	var args []any
	add := func(col, val string) {
		if strings.TrimSpace(val) == "" {
			return
		}
		sets = append(sets, col+" = ?")
		args = append(args, strings.TrimSpace(val))
	}
	add("make", p.Make)
	add("model", p.Model)
	add("vin", p.VIN)
	add("colour", p.Colour)
	add("cylinder_capacity", p.CylinderCapacity)
	add("fuel_type", p.FuelType)
	add("engine_size", p.EngineSize)
	add("engine_number", p.EngineNumber)
	add("tyre_size", p.TyreSize)
	add("radio_code", p.RadioCode)
	add("spare_keys", p.SpareKeys)
	if len(sets) == 0 {
		return nil
	}
	args = append(args, reg)
	_, err := tx.Exec("UPDATE vehicles SET "+strings.Join(sets, ", ")+" WHERE registration = ?", args...)
	return err
}

// OverwriteVehicleSpec replaces every spec field with exactly what's given,
// including deliberately blanking one out — unlike UpdateVehicleSpec's
// partial-update semantics (used when a repair visit only mentions some
// fields and must never erase what an earlier visit already recorded),
// this backs the "correct this vehicle's record" upload tool, where the
// operator is looking straight at the current values and a field they
// clear is a conscious choice, not an accidental gap.
func (s *Store) OverwriteVehicleSpec(reg string, p VehicleSpecPatch) error {
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
	_, err = tx.Exec(`
		UPDATE vehicles SET
			make = ?, model = ?, vin = ?, colour = ?, cylinder_capacity = ?,
			fuel_type = ?, engine_size = ?, engine_number = ?, tyre_size = ?,
			radio_code = ?, spare_keys = ?
		WHERE registration = ?`,
		strings.TrimSpace(p.Make), strings.TrimSpace(p.Model), strings.TrimSpace(p.VIN),
		strings.TrimSpace(p.Colour), strings.TrimSpace(p.CylinderCapacity), strings.TrimSpace(p.FuelType),
		strings.TrimSpace(p.EngineSize), strings.TrimSpace(p.EngineNumber), strings.TrimSpace(p.TyreSize),
		strings.TrimSpace(p.RadioCode), strings.TrimSpace(p.SpareKeys), reg)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ── devices ──────────────────────────────────────────────────────────────

type RepairsDevice struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Active    bool   `json:"active"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// RegisterRepairsDevice runs every time the PIN is typed in correctly —
// which is also exactly the moment the upload throttle's budget should
// reset, since typing the PIN again is a fresh proof of who's using the
// device.
func (s *Store) RegisterRepairsDevice(id, label string) error {
	_, err := s.db.Exec(`
		INSERT INTO repairs_devices (id, label, active, first_seen, last_seen, upload_count, upload_unlocked_at)
		VALUES (?, ?, 1, ?, ?, 0, ?)
		ON CONFLICT(id) DO UPDATE SET
			last_seen = excluded.last_seen, upload_count = 0, upload_unlocked_at = excluded.upload_unlocked_at`,
		id, label, now(), now(), now())
	return err
}

func (s *Store) RepairsDeviceActive(id string) (bool, error) {
	var active int
	err := s.db.QueryRow(`SELECT active FROM repairs_devices WHERE id = ?`, id).Scan(&active)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active != 0, nil
}

func (s *Store) ListRepairsDevices() ([]RepairsDevice, error) {
	rows, err := s.db.Query(`
		SELECT id, label, active, first_seen, last_seen
		FROM repairs_devices ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RepairsDevice{}
	for rows.Next() {
		var d RepairsDevice
		var active int
		if err := rows.Scan(&d.ID, &d.Label, &active, &d.FirstSeen, &d.LastSeen); err != nil {
			return nil, err
		}
		d.Active = active != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RevokeRepairsDevice(id string) error {
	_, err := s.db.Exec(`UPDATE repairs_devices SET active = 0 WHERE id = ?`, id)
	return err
}

// ── upload throttle ──────────────────────────────────────────────────────
//
// The bulk vehicle-data upload tool is more consequential than logging one
// visit — a stolen or left-unlocked device could otherwise silently rewrite
// a lot of vehicle records — so on top of the normal remembered-device
// login it re-asks for the PIN every uploadMaxUpdates updates or
// uploadWindow, whichever comes first.

const (
	uploadMaxUpdates = 10
	uploadWindow     = 25 * time.Minute
)

// RepairsUploadNeedsVerify reports whether this device has to re-enter the
// PIN before its next upload — either it has never been unlocked for
// uploads at all, used up its budget of updates, or the window since it
// last proved the PIN has run out.
func (s *Store) RepairsUploadNeedsVerify(deviceID string) (bool, error) {
	var count int
	var unlockedAt string
	err := s.db.QueryRow(`SELECT upload_count, upload_unlocked_at FROM repairs_devices WHERE id = ?`, deviceID).
		Scan(&count, &unlockedAt)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if unlockedAt == "" || count >= uploadMaxUpdates {
		return true, nil
	}
	t, err := time.Parse(time.RFC3339, unlockedAt)
	if err != nil {
		return true, nil // an unreadable timestamp is no proof of a recent unlock
	}
	return time.Since(t) >= uploadWindow, nil
}

// RecordRepairsUpload counts one update against the device's budget —
// called after each successful upload, never before, so a rejected or
// failed attempt doesn't spend it.
func (s *Store) RecordRepairsUpload(deviceID string) error {
	_, err := s.db.Exec(`UPDATE repairs_devices SET upload_count = upload_count + 1 WHERE id = ?`, deviceID)
	return err
}

// VerifyRepairsUpload resets the upload budget after the PIN is re-entered
// specifically for the upload tool — independent of the device's main
// 365-day login, which is untouched by this.
func (s *Store) VerifyRepairsUpload(deviceID string) error {
	_, err := s.db.Exec(`UPDATE repairs_devices SET upload_count = 0, upload_unlocked_at = ? WHERE id = ?`, now(), deviceID)
	return err
}
