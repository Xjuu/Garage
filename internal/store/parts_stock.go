package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// ── stock takes ──────────────────────────────────────────────────────────

// StockTake is one worker logging "I took N of this part for this vehicle".
type StockTake struct {
	ID         int64   `json:"id"`
	PartNumber string  `json:"part_number"`
	VehicleReg string  `json:"vehicle_reg"`
	Quantity   float64 `json:"quantity"`
	DeviceID   string  `json:"device_id"`
	DeviceName string  `json:"device_name"`
	TakenAt    string  `json:"taken_at"`
}

// LogStockTake records a part being taken for a vehicle. Registration is
// normalised the same way the rest of the fleet registry is, so it joins
// against it correctly; the part number is trusted as given — it is chosen
// from a search over parts that have actually appeared on an invoice, not
// typed freehand, so there is nothing further to normalise.
func (s *Store) LogStockTake(partNumber, vehicleReg string, quantity float64, deviceID string) error {
	partNumber = strings.TrimSpace(partNumber)
	if partNumber == "" {
		return fmt.Errorf("part number is required")
	}
	if quantity <= 0 {
		return fmt.Errorf("quantity must be greater than zero")
	}
	reg := NormalizeReg(vehicleReg)
	if reg == "" {
		return fmt.Errorf("vehicle registration is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO stock_takes (part_number, vehicle_reg, quantity, device_id, taken_at)
		VALUES (?, ?, ?, ?, ?)`,
		partNumber, reg, quantity, deviceID, now())
	return err
}

// RecentStockTakes lists what has been taken, newest first, for the admin
// audit view — who took what, for which car, and when.
func (s *Store) RecentStockTakes(limit int) ([]StockTake, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT st.id, st.part_number, st.vehicle_reg, st.quantity, st.device_id,
		       COALESCE(pd.label, ''), st.taken_at
		FROM stock_takes st
		LEFT JOIN parts_devices pd ON pd.id = st.device_id
		ORDER BY st.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []StockTake{}
	for rows.Next() {
		var t StockTake
		if err := rows.Scan(&t.ID, &t.PartNumber, &t.VehicleReg, &t.Quantity, &t.DeviceID, &t.DeviceName, &t.TakenAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PartStockLookup is the small, fast answer the parts counter needs the
// instant a part is picked: what it's called, and how many are left.
type PartStockLookup struct {
	PartNumber string  `json:"part_number"`
	Desc       string  `json:"description"`
	Stock      float64 `json:"stock"`
}

// SearchStockParts finds part numbers that have actually appeared on an
// invoice — the worker picks from what is real, rather than typing a number
// freehand that might not exist or might have a typo splitting its history
// in two, the same reasoning behind confusable-plate correction elsewhere.
func (s *Store) SearchStockParts(q string, limit int) ([]PartStockLookup, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	q = strings.TrimSpace(q)
	like := "%" + q + "%"
	rows, err := s.db.Query(`
		SELECT it.part_number, MAX(it.description),
		       COALESCE(SUM(it.quantity),0) - COALESCE((
		           SELECT SUM(st.quantity) FROM stock_takes st WHERE st.part_number = it.part_number
		       ), 0)
		FROM invoice_items it
		WHERE it.part_number <> '' AND (it.part_number LIKE ? OR it.description LIKE ?)
		GROUP BY it.part_number
		ORDER BY it.part_number
		LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PartStockLookup{}
	for rows.Next() {
		var p PartStockLookup
		var desc sql.NullString
		if err := rows.Scan(&p.PartNumber, &desc, &p.Stock); err != nil {
			return nil, err
		}
		p.Desc = desc.String
		out = append(out, p)
	}
	return out, rows.Err()
}

// StockPartByNumber is the same lookup for exactly one part number — used to
// refresh the on-screen stock count right after logging a take, without a
// full search round trip.
func (s *Store) StockPartByNumber(partNumber string) (*PartStockLookup, error) {
	row := s.db.QueryRow(`
		SELECT it.part_number, MAX(it.description),
		       COALESCE(SUM(it.quantity),0) - COALESCE((
		           SELECT SUM(st.quantity) FROM stock_takes st WHERE st.part_number = it.part_number
		       ), 0)
		FROM invoice_items it
		WHERE it.part_number = ?
		GROUP BY it.part_number`, partNumber)
	var p PartStockLookup
	var desc sql.NullString
	if err := row.Scan(&p.PartNumber, &desc, &p.Stock); err != nil {
		return nil, err
	}
	p.Desc = desc.String
	return &p, nil
}

// SearchStockVehicles finds registrations from the fleet registry — the
// worker picks the car the same considered way the part is picked, rather
// than typing a plate that might not match the registry's normalised form.
func (s *Store) SearchStockVehicles(q string, limit int) ([]string, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
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

// ── devices ───────────────────────────────────────────────────────────────

type PartsDevice struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Active    bool   `json:"active"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// RegisterPartsDevice records a device the moment it types the PIN correctly
// for the first time. Called again on every later visit to keep last_seen
// current — an admin deciding whether to revoke a device wants to know if
// it's still actually in use, not just when it first appeared.
func (s *Store) RegisterPartsDevice(id, label string) error {
	_, err := s.db.Exec(`
		INSERT INTO parts_devices (id, label, active, first_seen, last_seen)
		VALUES (?, ?, 1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET last_seen = excluded.last_seen`,
		id, label, now(), now())
	return err
}

// PartsDeviceActive reports whether a device is both known and not revoked —
// the only two things a valid-looking, correctly-signed cookie is not
// enough to prove on its own.
func (s *Store) PartsDeviceActive(id string) (bool, error) {
	var active int
	err := s.db.QueryRow(`SELECT active FROM parts_devices WHERE id = ?`, id).Scan(&active)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active != 0, nil
}

func (s *Store) ListPartsDevices() ([]PartsDevice, error) {
	rows, err := s.db.Query(`
		SELECT id, label, active, first_seen, last_seen
		FROM parts_devices ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PartsDevice{}
	for rows.Next() {
		var d PartsDevice
		var active int
		if err := rows.Scan(&d.ID, &d.Label, &active, &d.FirstSeen, &d.LastSeen); err != nil {
			return nil, err
		}
		d.Active = active != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokePartsDevice deactivates a device rather than deleting it — the audit
// trail in stock_takes still names it by id, and a deleted row would leave
// that history pointing at nothing.
func (s *Store) RevokePartsDevice(id string) error {
	_, err := s.db.Exec(`UPDATE parts_devices SET active = 0 WHERE id = ?`, id)
	return err
}

// ── allowed IPs ──────────────────────────────────────────────────────────

type AllowedIP struct {
	IP        string `json:"ip"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) AddAllowedIP(ip, label string) error {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return fmt.Errorf("an IP address is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO parts_allowed_ips (ip, label, created_at) VALUES (?, ?, ?)
		ON CONFLICT(ip) DO UPDATE SET label = excluded.label`,
		ip, strings.TrimSpace(label), now())
	return err
}

func (s *Store) RemoveAllowedIP(ip string) error {
	_, err := s.db.Exec(`DELETE FROM parts_allowed_ips WHERE ip = ?`, ip)
	return err
}

func (s *Store) ListAllowedIPs() ([]AllowedIP, error) {
	rows, err := s.db.Query(`SELECT ip, label, created_at FROM parts_allowed_ips ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AllowedIP{}
	for rows.Next() {
		var a AllowedIP
		if err := rows.Scan(&a.IP, &a.Label, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// IPAllowed reports whether ip may reach the parts site. An empty allow-list
// denies everything — the deliberate fail-closed default, not an oversight:
// the parts site is useless (and worse, an open door) with nobody added yet.
func (s *Store) IPAllowed(ip string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM parts_allowed_ips WHERE ip = ?`, ip).Scan(&n)
	return n > 0, err
}
