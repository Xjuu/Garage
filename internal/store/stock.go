package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// Why a stock movement happened. Free text would make the history
// unfilterable within a week; these three cover what actually occurs at a
// parts counter.
const (
	StockReceived   = "received"   // a delivery came in
	StockUsed       = "used"       // fitted to a car
	StockCorrection = "correction" // a stocktake found the shelf disagreeing
)

// StockPart is one line of shelf stock, identified by the barcode on the box.
type StockPart struct {
	ID          int64   `json:"id"`
	Barcode     string  `json:"barcode"`
	PartNumber  string  `json:"part_number"`
	Description string  `json:"description"`
	Quantity    float64 `json:"quantity"`
	MinQuantity float64 `json:"min_quantity"`
	Location    string  `json:"location"`
	UnitCost    float64 `json:"unit_cost"`
	// FitsMake empty means the part goes on anything. FitsMake set with
	// FitsModel empty means any car of that make. See AdjustStock, which is
	// the only place this is enforced.
	FitsMake  string `json:"fits_make"`
	FitsModel string `json:"fits_model"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
	// Low is Quantity at or below MinQuantity, with a MinQuantity actually
	// set — computed here rather than in three separate screens.
	Low bool `json:"low"`
}

const stockPartCols = `id, barcode, part_number, description, quantity, min_quantity,
	location, unit_cost, fits_make, fits_model, notes, created_at`

func scanStockPart(scan func(...any) error) (*StockPart, error) {
	var p StockPart
	if err := scan(&p.ID, &p.Barcode, &p.PartNumber, &p.Description, &p.Quantity,
		&p.MinQuantity, &p.Location, &p.UnitCost, &p.FitsMake, &p.FitsModel,
		&p.Notes, &p.CreatedAt); err != nil {
		return nil, err
	}
	p.Low = p.MinQuantity > 0 && p.Quantity <= p.MinQuantity
	return &p, nil
}

// normalizeBarcode trims and upper-cases. Scanners are keyboards: the same
// label read twice must produce the same key, and a stray space from a
// hand-typed entry should not create a second row for the same box.
func normalizeBarcode(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// AddStockPart puts a new line on the shelf. The barcode is the identity —
// everything else can be filled in later, but a part nothing can scan is a
// part this system cannot do anything with.
func (s *Store) AddStockPart(p StockPart) (int64, error) {
	code := normalizeBarcode(p.Barcode)
	if code == "" {
		return 0, fmt.Errorf("a barcode is required — scan the box")
	}
	if p.Quantity < 0 || p.MinQuantity < 0 {
		return 0, fmt.Errorf("quantities cannot be negative")
	}
	// A model without a make cannot be checked against anything: the
	// registry is keyed on both, and "fits a Corolla, any manufacturer" is
	// not a rule that means anything.
	if strings.TrimSpace(p.FitsMake) == "" && strings.TrimSpace(p.FitsModel) != "" {
		return 0, fmt.Errorf("a model restriction needs a make as well")
	}

	res, err := s.db.Exec(`INSERT INTO stock_parts
		(barcode, part_number, description, quantity, min_quantity, location,
		 unit_cost, fits_make, fits_model, notes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		code, strings.TrimSpace(p.PartNumber), strings.TrimSpace(p.Description),
		p.Quantity, p.MinQuantity, strings.TrimSpace(p.Location), p.UnitCost,
		strings.TrimSpace(p.FitsMake), strings.TrimSpace(p.FitsModel), p.Notes)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, fmt.Errorf("barcode %s is already on the shelf — scan it to add stock instead", code)
		}
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// An opening quantity is itself a movement, so the history explains the
	// whole of the number on the shelf rather than starting from a figure
	// that appeared from nowhere.
	if p.Quantity != 0 {
		if _, err := s.db.Exec(`INSERT INTO stock_movements
			(part_id, delta, reason, note, created_at)
			VALUES (?, ?, ?, 'opening stock', datetime('now'))`,
			id, p.Quantity, StockCorrection); err != nil {
			return id, err
		}
	}
	return id, nil
}

// UpdateStockPart edits the details, never the quantity — that only ever
// moves through AdjustStock, so every change to a shelf figure leaves a
// movement behind it.
func (s *Store) UpdateStockPart(id int64, p StockPart) error {
	code := normalizeBarcode(p.Barcode)
	if code == "" {
		return fmt.Errorf("a barcode is required")
	}
	if p.MinQuantity < 0 {
		return fmt.Errorf("quantities cannot be negative")
	}
	if strings.TrimSpace(p.FitsMake) == "" && strings.TrimSpace(p.FitsModel) != "" {
		return fmt.Errorf("a model restriction needs a make as well")
	}
	_, err := s.db.Exec(`UPDATE stock_parts
		SET barcode = ?, part_number = ?, description = ?, min_quantity = ?,
		    location = ?, unit_cost = ?, fits_make = ?, fits_model = ?, notes = ?
		WHERE id = ?`,
		code, strings.TrimSpace(p.PartNumber), strings.TrimSpace(p.Description),
		p.MinQuantity, strings.TrimSpace(p.Location), p.UnitCost,
		strings.TrimSpace(p.FitsMake), strings.TrimSpace(p.FitsModel), p.Notes, id)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("barcode %s is already used by another part", code)
	}
	return err
}

func (s *Store) StockPart(id int64) (*StockPart, error) {
	row := s.db.QueryRow(`SELECT `+stockPartCols+` FROM stock_parts WHERE id = ?`, id)
	return scanStockPart(row.Scan)
}

// StockPartByBarcode is the scan: one read, keyed on exactly what the
// scanner typed. Returns sql.ErrNoRows for an unknown label, which the
// counter screen turns into "add this part".
func (s *Store) StockPartByBarcode(barcode string) (*StockPart, error) {
	code := normalizeBarcode(barcode)
	if code == "" {
		return nil, sql.ErrNoRows
	}
	row := s.db.QueryRow(`SELECT `+stockPartCols+` FROM stock_parts WHERE barcode = ?`, code)
	return scanStockPart(row.Scan)
}

// StockParts lists the shelf, optionally narrowed by barcode, part number
// or description — the three things someone holding a box can read off it.
func (s *Store) StockParts(q string) ([]StockPart, error) {
	sqlStr := `SELECT ` + stockPartCols + ` FROM stock_parts`
	var args []any
	if q = strings.TrimSpace(q); q != "" {
		sqlStr += ` WHERE barcode LIKE ? OR part_number LIKE ? OR description LIKE ?
		            OR fits_make LIKE ? OR fits_model LIKE ?`
		like := "%" + q + "%"
		args = append(args, like, like, like, like, like)
	}
	// Low stock first: the list is most useful when what needs ordering is
	// at the top of it.
	sqlStr += ` ORDER BY (min_quantity > 0 AND quantity <= min_quantity) DESC,
	            part_number COLLATE NOCASE, barcode`

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []StockPart{}
	for rows.Next() {
		p, err := scanStockPart(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteStockPart(id int64) error {
	// Movements cascade with the part: the history of a line nobody stocks
	// any more is not worth keeping on its own, and ON DELETE CASCADE on
	// stock_movements already says so.
	_, err := s.db.Exec(`DELETE FROM stock_parts WHERE id = ?`, id)
	return err
}

// StockMovement is one change to a shelf quantity.
type StockMovement struct {
	ID         int64   `json:"id"`
	PartID     int64   `json:"part_id"`
	Delta      float64 `json:"delta"`
	Reason     string  `json:"reason"`
	VehicleReg string  `json:"vehicle_reg"`
	ByUser     string  `json:"by_user"`
	Note       string  `json:"note"`
	CreatedAt  string  `json:"created_at"`
}

// FitmentError is returned when a part is refused for a car it does not fit.
// Its own type so the HTTP layer can answer 409 rather than a flat 400:
// nothing about the request was malformed, the answer is simply no.
type FitmentError struct{ Reason string }

func (e FitmentError) Error() string { return e.Reason }

// checkFitment enforces the rule the whole feature exists for: a part
// restricted to a make (and optionally a model) may only be taken out for a
// car that matches. The car's make and model come from the fleet registry,
// which is the same table the dashboard and repairs site already maintain —
// so a part is checked against what is actually known about that vehicle,
// not against something retyped at the counter.
func checkFitment(tx *sql.Tx, p *StockPart, reg string) error {
	if strings.TrimSpace(p.FitsMake) == "" {
		return nil // fits anything
	}
	if reg == "" {
		return nil // not being taken out for a particular car
	}

	var make, model string
	err := tx.QueryRow(`SELECT make, model FROM vehicles WHERE registration = ?`, reg).
		Scan(&make, &model)
	if err == sql.ErrNoRows {
		return FitmentError{Reason: fmt.Sprintf(
			"%s is not in the vehicle registry, so there is no make or model to check %s against",
			reg, fitmentLabel(p))}
	}
	if err != nil {
		return err
	}
	if make == "" {
		return FitmentError{Reason: fmt.Sprintf(
			"%s has no make recorded in the registry, so it cannot be checked against %s",
			reg, fitmentLabel(p))}
	}
	if !strings.EqualFold(strings.TrimSpace(make), strings.TrimSpace(p.FitsMake)) {
		return FitmentError{Reason: fmt.Sprintf("this part only fits %s — %s is a %s",
			fitmentLabel(p), reg, strings.TrimSpace(make+" "+model))}
	}
	if m := strings.TrimSpace(p.FitsModel); m != "" &&
		!strings.EqualFold(strings.TrimSpace(model), m) {
		return FitmentError{Reason: fmt.Sprintf("this part only fits %s — %s is a %s",
			fitmentLabel(p), reg, strings.TrimSpace(make+" "+model))}
	}
	return nil
}

func fitmentLabel(p *StockPart) string {
	return strings.TrimSpace(strings.TrimSpace(p.FitsMake) + " " + strings.TrimSpace(p.FitsModel))
}

// AdjustStock moves a quantity on or off the shelf and records why, who and
// for which car. The read, the fitment check, the update and the movement
// row all share one transaction: two people scanning the last part at once
// must not both be told they have it.
func (s *Store) AdjustStock(partID int64, delta float64, reason, vehicleReg, byUser, note string) (*StockPart, error) {
	if delta == 0 {
		return nil, fmt.Errorf("nothing to add or take off")
	}
	switch reason {
	case StockReceived, StockUsed, StockCorrection:
	default:
		return nil, fmt.Errorf("reason must be received, used or correction, got %q", reason)
	}
	reg := NormalizeReg(vehicleReg)

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	row := tx.QueryRow(`SELECT `+stockPartCols+` FROM stock_parts WHERE id = ?`, partID)
	p, err := scanStockPart(row.Scan)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no such part")
		}
		return nil, err
	}

	// Only taking a part OUT is a fitment question. Putting stock back on
	// the shelf, or correcting a count, says nothing about what it fits.
	if delta < 0 {
		if err := checkFitment(tx, p, reg); err != nil {
			return nil, err
		}
	}

	if p.Quantity+delta < 0 {
		return nil, fmt.Errorf("only %g on the shelf — cannot take %g",
			p.Quantity, -delta)
	}

	if _, err := tx.Exec(`UPDATE stock_parts SET quantity = quantity + ? WHERE id = ?`,
		delta, partID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO stock_movements
		(part_id, delta, reason, vehicle_reg, by_user, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'))`,
		partID, delta, reason, reg, byUser, strings.TrimSpace(note)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.StockPart(partID)
}

// StockMovements is the history for one part, newest first.
func (s *Store) StockMovements(partID int64, limit int) ([]StockMovement, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, part_id, delta, reason, vehicle_reg, by_user, note, created_at
		FROM stock_movements WHERE part_id = ? ORDER BY id DESC LIMIT ?`, partID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []StockMovement{}
	for rows.Next() {
		var m StockMovement
		if err := rows.Scan(&m.ID, &m.PartID, &m.Delta, &m.Reason, &m.VehicleReg,
			&m.ByUser, &m.Note, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecentStockMovements is the same across every part — what the store has
// been doing today, which is what the counter screen opens on.
func (s *Store) RecentStockMovements(limit int) ([]StockMovementView, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT m.id, m.part_id, m.delta, m.reason, m.vehicle_reg, m.by_user, m.note, m.created_at,
		       p.barcode, p.part_number, p.description
		FROM stock_movements m
		JOIN stock_parts p ON p.id = m.part_id
		ORDER BY m.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []StockMovementView{}
	for rows.Next() {
		var m StockMovementView
		if err := rows.Scan(&m.ID, &m.PartID, &m.Delta, &m.Reason, &m.VehicleReg,
			&m.ByUser, &m.Note, &m.CreatedAt,
			&m.Barcode, &m.PartNumber, &m.Description); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// StockMovementView is a movement with enough of its part attached to be
// readable on its own line.
type StockMovementView struct {
	StockMovement
	Barcode     string `json:"barcode"`
	PartNumber  string `json:"part_number"`
	Description string `json:"description"`
}

// StockOverview is the counter screen's header.
type StockOverview struct {
	Lines      int     `json:"lines"`
	Units      float64 `json:"units"`
	LowLines   int     `json:"low_lines"`
	Value      float64 `json:"value"`
	MovedToday int     `json:"moved_today"`
}

func (s *Store) StockOverview() (*StockOverview, error) {
	o := &StockOverview{}
	err := s.db.QueryRow(`
		SELECT
		  (SELECT COUNT(1) FROM stock_parts),
		  (SELECT COALESCE(SUM(quantity), 0) FROM stock_parts),
		  (SELECT COUNT(1) FROM stock_parts WHERE min_quantity > 0 AND quantity <= min_quantity),
		  (SELECT COALESCE(SUM(quantity * unit_cost), 0) FROM stock_parts),
		  (SELECT COUNT(1) FROM stock_movements WHERE date(created_at) = date('now'))`).
		Scan(&o.Lines, &o.Units, &o.LowLines, &o.Value, &o.MovedToday)
	if err != nil {
		return nil, err
	}
	return o, nil
}
