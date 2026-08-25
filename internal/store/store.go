// Package store persists invoices in a local, encrypted-at-rest SQLite file.
// It uses github.com/mutecomm/go-sqlcipher/v4 — SQLCipher compiled with its
// own bundled libtomcrypt, not system OpenSSL, so the resulting binary still
// has no runtime dependency on anything the host doesn't already have
// (verified: `ldd` on the built binary shows nothing beyond libc). The one
// real cost is cgo itself: the build needs a C compiler now, unlike the
// previous pure-Go modernc.org/sqlite driver, which is why deploy/update.sh
// sets CGO_ENABLED=1.
package store

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
)

type Store struct{ db *sql.DB }

// Invoice is one invoice document, normally one PDF attachment.
type Invoice struct {
	ID          int64
	FileSHA256  string
	SourceFile  string
	MailUID     int64
	MailSubject string
	MailFrom    string
	MailDate    string

	Supplier      string
	InvoiceNumber string
	InvoiceDate   string // ISO YYYY-MM-DD, the date of purchase
	VehicleReg    string

	Currency  string
	Netto     float64
	VATAmount float64
	VATRate   float64
	Brutto    float64

	NeedsReview bool
	// IsGeneral marks a purchase that is workshop stock — oil, WD-40,
	// consumables — rather than work on one vehicle. Without it, every such
	// invoice would be flagged for a missing registration.
	IsGeneral bool
	// Returned marks an invoice a later credit note was automatically linked
	// to — set on the ORIGINAL purchase, purely a display flag. It plays no
	// part in any spend total: the credit note that caused it keeps its own
	// row with its own (usually negative) amounts, which is what actually
	// nets the total, exactly as it always has for any credit note whether
	// or not it could be linked to an original.
	Returned bool
	// CreditOf is set on a credit note's own row: the id of the invoice it
	// credits, when that could be matched automatically. Nil on every
	// ordinary invoice, and on a credit note with no confident match.
	CreditOf  *int64
	Notes     string
	RawJSON   string
	CreatedAt string

	Items []Item
}

// Item is one line on an invoice. Part numbers live here because a single
// invoice routinely lists many parts.
type Item struct {
	ID         int64
	InvoiceID  int64
	LineNo     int
	PartNumber string
	Desc       string
	VehicleReg string
	Quantity   float64
	UnitPrice  float64
	Netto      float64
	VATAmount  float64
	VATRate    float64
	Brutto     float64
}

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS invoices (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  file_sha256    TEXT NOT NULL UNIQUE,
  source_file    TEXT NOT NULL,
  mail_uid       INTEGER,
  mail_subject   TEXT,
  mail_from      TEXT,
  mail_date      TEXT,
  supplier       TEXT,
  invoice_number TEXT,
  invoice_date   TEXT,
  vehicle_reg    TEXT,
  currency       TEXT,
  netto          REAL,
  vat_amount     REAL,
  vat_rate       REAL,
  brutto         REAL,
  needs_review   INTEGER NOT NULL DEFAULT 0,
  is_general     INTEGER NOT NULL DEFAULT 0,
  returned       INTEGER NOT NULL DEFAULT 0,
  credit_of      INTEGER REFERENCES invoices(id),
  notes          TEXT,
  raw_json       TEXT,
  created_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS invoice_items (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  invoice_id  INTEGER NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
  line_no     INTEGER,
  part_number TEXT,
  description TEXT,
  vehicle_reg TEXT,
  quantity    REAL,
  unit_price  REAL,
  netto       REAL,
  vat_amount  REAL,
  vat_rate    REAL,
  brutto      REAL
);

CREATE TABLE IF NOT EXISTS seen_messages (
  mailbox      TEXT NOT NULL,
  uid          INTEGER NOT NULL,
  processed_at TEXT NOT NULL,
  PRIMARY KEY (mailbox, uid)
);

CREATE INDEX IF NOT EXISTS idx_invoices_date ON invoices(invoice_date);
CREATE INDEX IF NOT EXISTS idx_invoices_reg  ON invoices(vehicle_reg);
CREATE INDEX IF NOT EXISTS idx_items_part    ON invoice_items(part_number);
CREATE INDEX IF NOT EXISTS idx_items_invoice ON invoice_items(invoice_id);

-- Fleet ---------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS companies (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL UNIQUE,
  is_default INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);

-- Keyed on the normalised registration (upper case, no spaces), which is the
-- same form invoices.vehicle_reg is stored in, so the two always join.
CREATE TABLE IF NOT EXISTS vehicles (
  registration      TEXT PRIMARY KEY,
  company_id        INTEGER REFERENCES companies(id) ON DELETE SET NULL,
  make              TEXT NOT NULL DEFAULT '',
  model             TEXT NOT NULL DEFAULT '',
  year              TEXT NOT NULL DEFAULT '',
  driver            TEXT NOT NULL DEFAULT '',
  notes             TEXT NOT NULL DEFAULT '',
  active            INTEGER NOT NULL DEFAULT 1,
  -- The rest are spec facts a repair entry fills in as it learns them
  -- (see UpdateVehicleSpec) — kept here too so the registry always shows
  -- the latest known values, not just whatever the most recent repair row
  -- happened to record.
  vin               TEXT NOT NULL DEFAULT '',
  colour            TEXT NOT NULL DEFAULT '',
  cylinder_capacity TEXT NOT NULL DEFAULT '',
  fuel_type         TEXT NOT NULL DEFAULT '',
  engine_size       TEXT NOT NULL DEFAULT '',
  engine_number     TEXT NOT NULL DEFAULT '',
  tyre_size         TEXT NOT NULL DEFAULT '',
  radio_code        TEXT NOT NULL DEFAULT '',
  spare_keys        TEXT NOT NULL DEFAULT '',
  -- Unlike the spec facts above, this isn't something a repair visit ever
  -- fills in — it's a fleet-level classification set from the dashboard
  -- (or a bulk import) for what the vehicle itself is licensed/equipped
  -- for, e.g. "F".
  capabilities      TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_vehicles_company ON vehicles(company_id);

-- Repairs ---------------------------------------------------------------

-- One row per service/repair visit, logged from repairs.<domain>. Vehicle
-- spec fields are captured here too (not just on vehicles) so a repair row
-- is a complete, self-contained record of what was true and entered at
-- that visit — important for the historical CSV import this is meant to
-- receive, which will arrive as one row per visit, not a separate vehicle
-- master sheet.
CREATE TABLE IF NOT EXISTS repairs (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  vehicle_reg         TEXT NOT NULL,
  service_date        TEXT NOT NULL,
  service_type        TEXT NOT NULL DEFAULT '', -- "full", "mini", or "other"
  service_type_other  TEXT NOT NULL DEFAULT '', -- free text when service_type = "other"
  mileage             REAL,
  -- Its own yes/no, deliberately not folded into description — but it
  -- shares service_date, the one date both a "last repair" and a "last
  -- timing belt change" reading are computed from.
  timing_belt_changed INTEGER NOT NULL DEFAULT 0,
  description         TEXT NOT NULL DEFAULT '',
  vin                 TEXT NOT NULL DEFAULT '',
  make                TEXT NOT NULL DEFAULT '',
  model               TEXT NOT NULL DEFAULT '',
  colour              TEXT NOT NULL DEFAULT '',
  cylinder_capacity   TEXT NOT NULL DEFAULT '',
  spare_keys          TEXT NOT NULL DEFAULT '',
  fuel_type           TEXT NOT NULL DEFAULT '',
  engine_size         TEXT NOT NULL DEFAULT '',
  engine_number       TEXT NOT NULL DEFAULT '',
  tyre_size           TEXT NOT NULL DEFAULT '',
  radio_code          TEXT NOT NULL DEFAULT '',
  oil_amount          TEXT NOT NULL DEFAULT '',
  device_id           TEXT NOT NULL DEFAULT '',
  created_at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_repairs_reg ON repairs(vehicle_reg);

-- A device that has typed the correct repairs PIN once — same shape and
-- same reasoning as parts_devices before it: revoking access means
-- deactivating the row here, not rotating a shared key that would sign
-- every device out at once instead of one.
CREATE TABLE IF NOT EXISTS repairs_devices (
  id          TEXT PRIMARY KEY,
  label       TEXT NOT NULL DEFAULT '',
  active      INTEGER NOT NULL DEFAULT 1,
  first_seen  TEXT NOT NULL,
  last_seen   TEXT NOT NULL
);

-- Training ------------------------------------------------------------------

-- One example invoice plus the corrected values a human typed in. These feed
-- both the extraction prompt and the eval regression run.
CREATE TABLE IF NOT EXISTS examples (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  file_sha256 TEXT NOT NULL UNIQUE,
  filename    TEXT NOT NULL,
  source_file TEXT NOT NULL,
  mime_type   TEXT NOT NULL DEFAULT 'application/pdf',
  supplier    TEXT NOT NULL DEFAULT '',
  truth_json  TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'pending',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- Free-text guidance injected only when that supplier is recognised, so the
-- prompt stays small no matter how many suppliers accumulate.
CREATE TABLE IF NOT EXISTS supplier_hints (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  supplier   TEXT NOT NULL UNIQUE,
  hint       TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS eval_runs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at  TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT '',
  model       TEXT NOT NULL DEFAULT '',
  examples    INTEGER NOT NULL DEFAULT 0,
  fields_ok   INTEGER NOT NULL DEFAULT 0,
  fields_all  INTEGER NOT NULL DEFAULT 0,
  detail_json TEXT NOT NULL DEFAULT ''
);

-- Dashboard accounts ---------------------------------------------------------

-- Every account that can sign in to the main dashboard — distinct from
-- repairs.<domain>, which the whole crew shares one PIN for (see
-- repairs_devices above). "role" is what the nav and the API route gate on:
-- "admin" reaches everything; "fleet" cannot reach Training or Admin (see
-- internal/web's requireAdmin). must_change_password forces a fresh
-- password before anything else on the very next login — how a
-- freshly-created account with a temporary password gets upgraded to a real
-- one without an operator ever knowing what the owner picked.
CREATE TABLE IF NOT EXISTS users (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  username             TEXT NOT NULL UNIQUE,
  email                TEXT NOT NULL DEFAULT '',
  password_hash        TEXT NOT NULL,
  role                 TEXT NOT NULL DEFAULT 'admin',
  totp_secret          TEXT NOT NULL DEFAULT '',
  must_change_password INTEGER NOT NULL DEFAULT 0,
  -- Set only via 'goldstar user-add --skip-setup' (or SetUserTOTPExempt) for
  -- a deliberately shared login — a "Temporary" account handed out with a
  -- fixed password, meant to be usable by more than one person without any
  -- one of them "claiming" it by being first to enroll 2FA. Everywhere else
  -- 2FA is genuinely mandatory; this is the one narrow, explicit exception.
  totp_exempt          INTEGER NOT NULL DEFAULT 0,
  -- Blocks every mutating request (POST/PUT/PATCH/DELETE) this account
  -- makes, enforced centrally in auth.Protect — not a role, and not tied to
  -- totp_exempt: an ordinary admin or fleet account can be read-only too.
  -- For a shared "Temporary" login, browsing is the whole point and nothing
  -- it does should ever change or delete real data.
  read_only            INTEGER NOT NULL DEFAULT 0,
  created_at           TEXT NOT NULL
);
`

// seed inserts the fleet the user actually operates. Overall Clients is the
// catch-all a newly seen registration lands in until someone assigns it.
const seed = `
INSERT OR IGNORE INTO companies (name, is_default, created_at) VALUES
  ('GOLDSTAR DIAMOND CARS', 0, datetime('now')),
  ('MFS MOTORGROUP',        0, datetime('now')),
  ('Overall Clients',       1, datetime('now'));
`

// backfillOrphanVehicles catches any plate that already has invoices but
// predates ensureVehicleRegistered (or a restored backup, or a row deleted by
// hand) — registered here under the default company so its cost joins every
// company's total on this and every future startup, not just for invoices
// stored from now on. INSERT OR IGNORE makes re-running it on every restart
// free: nothing happens once every plate already has a row.
const backfillOrphanVehicles = `
INSERT OR IGNORE INTO vehicles (registration, company_id, make, model, year, driver, notes, active, created_at)
SELECT DISTINCT i.vehicle_reg, (SELECT id FROM companies WHERE is_default = 1), '', '', '', '', '', 1, datetime('now')
FROM invoices i
WHERE i.vehicle_reg <> ''
  AND NOT EXISTS (SELECT 1 FROM vehicles v WHERE v.registration = i.vehicle_reg);
`

// Open opens (or creates) the database at path. key, if non-empty, must be
// 64 hex characters (32 raw bytes) — SQLCipher's "raw key" form, which skips
// its own PBKDF2 derivation since a machine-generated key is already high
// entropy, unlike a human password. An empty key opens the file
// unencrypted, which every production deployment must never do (see
// realMain's own check in main.go) — tests are the one legitimate use,
// where exercising encryption itself is redundant with store_test.go's
// dedicated SQLCipher-path tests.
func Open(path, key string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)"
	if key != "" {
		if len(key) != 64 {
			return nil, fmt.Errorf("db key must be 64 hex characters (32 bytes), got %d", len(key))
		}
		if _, err := hex.DecodeString(key); err != nil {
			return nil, fmt.Errorf("db key must be hex: %w", err)
		}
		dsn += "&_pragma_key=x'" + key + "'"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if _, err := db.Exec(seed); err != nil {
		db.Close()
		return nil, fmt.Errorf("seed companies: %w", err)
	}
	if _, err := db.Exec(backfillOrphanVehicles); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfill orphan vehicles: %w", err)
	}
	return &Store{db: db}, nil
}

// migrate adds columns introduced after a database was first created.
// CREATE TABLE IF NOT EXISTS silently leaves an existing table alone, so new
// columns have to be added explicitly or upgrades break on the old schema.
func migrate(db *sql.DB) error {
	tables := map[string]map[string]string{
		"invoices": {
			"is_general": "ALTER TABLE invoices ADD COLUMN is_general INTEGER NOT NULL DEFAULT 0",
			"returned":   "ALTER TABLE invoices ADD COLUMN returned INTEGER NOT NULL DEFAULT 0",
			"credit_of":  "ALTER TABLE invoices ADD COLUMN credit_of INTEGER REFERENCES invoices(id)",
		},
		"vehicles": {
			"vin":               "ALTER TABLE vehicles ADD COLUMN vin TEXT NOT NULL DEFAULT ''",
			"colour":            "ALTER TABLE vehicles ADD COLUMN colour TEXT NOT NULL DEFAULT ''",
			"cylinder_capacity": "ALTER TABLE vehicles ADD COLUMN cylinder_capacity TEXT NOT NULL DEFAULT ''",
			"fuel_type":         "ALTER TABLE vehicles ADD COLUMN fuel_type TEXT NOT NULL DEFAULT ''",
			"engine_size":       "ALTER TABLE vehicles ADD COLUMN engine_size TEXT NOT NULL DEFAULT ''",
			"engine_number":     "ALTER TABLE vehicles ADD COLUMN engine_number TEXT NOT NULL DEFAULT ''",
			"tyre_size":         "ALTER TABLE vehicles ADD COLUMN tyre_size TEXT NOT NULL DEFAULT ''",
			"radio_code":        "ALTER TABLE vehicles ADD COLUMN radio_code TEXT NOT NULL DEFAULT ''",
			"spare_keys":        "ALTER TABLE vehicles ADD COLUMN spare_keys TEXT NOT NULL DEFAULT ''",
			"capabilities":      "ALTER TABLE vehicles ADD COLUMN capabilities TEXT NOT NULL DEFAULT ''",
		},
		"repairs": {
			"engine_number": "ALTER TABLE repairs ADD COLUMN engine_number TEXT NOT NULL DEFAULT ''",
		},
		"users": {
			"totp_exempt": "ALTER TABLE users ADD COLUMN totp_exempt INTEGER NOT NULL DEFAULT 0",
			"read_only":   "ALTER TABLE users ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0",
		},
	}
	for table, added := range tables {
		have, err := columns(db, table)
		if err != nil {
			return err
		}
		for col, stmt := range added {
			if have[col] {
				continue
			}
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("add %s.%s: %w", table, col, err)
			}
		}
	}
	return nil
}

func columns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

// Vacuum compacts the database file, reclaiming space from deleted rows.
func (s *Store) Vacuum() error {
	_, err := s.db.Exec(`VACUUM`)
	return err
}

// BackupTo writes a consistent snapshot to path. VACUUM INTO is safe on a live
// WAL-mode database, which a plain file copy would not be.
func (s *Store) BackupTo(path string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	return err
}

// HasFile reports whether this exact attachment was already ingested. Hashing
// the bytes means a forwarded or resent invoice is never double-counted.
func (s *Store) HasFile(sha string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM invoices WHERE file_sha256 = ?`, sha).Scan(&n)
	return n > 0, err
}

func (s *Store) IsMessageSeen(mailbox string, uid int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM seen_messages WHERE mailbox = ? AND uid = ?`, mailbox, uid).Scan(&n)
	return n > 0, err
}

func (s *Store) MarkMessageSeen(mailbox string, uid int64) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO seen_messages (mailbox, uid, processed_at) VALUES (?, ?, ?)`,
		mailbox, uid, time.Now().UTC().Format(time.RFC3339))
	return err
}

// InsertInvoice writes the invoice and its line items in one transaction.
func (s *Store) InsertInvoice(inv *Invoice) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO invoices (
			file_sha256, source_file, mail_uid, mail_subject, mail_from, mail_date,
			supplier, invoice_number, invoice_date, vehicle_reg,
			currency, netto, vat_amount, vat_rate, brutto,
			needs_review, is_general, returned, credit_of, notes, raw_json, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		inv.FileSHA256, inv.SourceFile, inv.MailUID, inv.MailSubject, inv.MailFrom, inv.MailDate,
		inv.Supplier, inv.InvoiceNumber, inv.InvoiceDate, inv.VehicleReg,
		inv.Currency, inv.Netto, inv.VATAmount, inv.VATRate, inv.Brutto,
		boolToInt(inv.NeedsReview), boolToInt(inv.IsGeneral), boolToInt(inv.Returned), inv.CreditOf,
		inv.Notes, inv.RawJSON, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("insert invoice: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	// Give the vehicle a registry row now, in the same transaction, so its
	// cost is never left out of every company's total between "first
	// invoice seen" and "someone gets around to triaging it" — see
	// ensureVehicleRegistered.
	if err := ensureVehicleRegistered(tx, inv.VehicleReg); err != nil {
		return 0, fmt.Errorf("register vehicle: %w", err)
	}

	for _, it := range inv.Items {
		if _, err := tx.Exec(`
			INSERT INTO invoice_items (
				invoice_id, line_no, part_number, description, vehicle_reg,
				quantity, unit_price, netto, vat_amount, vat_rate, brutto
			) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			id, it.LineNo, it.PartNumber, it.Desc, it.VehicleReg,
			it.Quantity, it.UnitPrice, it.Netto, it.VATAmount, it.VATRate, it.Brutto,
		); err != nil {
			return 0, fmt.Errorf("insert item: %w", err)
		}
	}
	return id, tx.Commit()
}

// FindInvoiceByReference looks for exactly one existing invoice from the
// given supplier with this invoice number — the one a credit note's stated
// reference is presumably crediting. Ambiguous (more than one candidate) or
// absent matches report found=false rather than guessing: acting on the
// wrong invoice would corrupt a financial record automatically, with nobody
// reviewing the choice. A credit note is never itself treated as the
// original (credit_of IS NULL) — crediting a credit note makes no sense and
// would only ever indicate a supplier reusing a number.
func (s *Store) FindInvoiceByReference(supplier, invoiceNumber string) (id int64, found bool, err error) {
	supplier = strings.TrimSpace(supplier)
	invoiceNumber = strings.TrimSpace(invoiceNumber)
	if supplier == "" || invoiceNumber == "" {
		return 0, false, nil
	}
	rows, err := s.db.Query(`
		SELECT id FROM invoices
		WHERE supplier = ? AND invoice_number = ? AND credit_of IS NULL
		LIMIT 2`, supplier, invoiceNumber)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var i int64
		if err := rows.Scan(&i); err != nil {
			return 0, false, err
		}
		ids = append(ids, i)
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if len(ids) != 1 {
		return 0, false, nil
	}
	return ids[0], true, nil
}

// MarkReturned flags an invoice as returned — set on the original purchase
// once a credit note has been automatically linked to it. Purely a display
// flag: it plays no part in any spend total, which is why this is a single
// targeted UPDATE rather than something every aggregate query needs to know
// about — the credit note's own (usually negative) amounts are what actually
// net the total, exactly as they always have.
func (s *Store) MarkReturned(id int64) error {
	_, err := s.db.Exec(`UPDATE invoices SET returned = 1 WHERE id = ?`, id)
	return err
}

// ListInvoices returns invoices with their items, newest purchase first.
// An empty since/until pair means "everything".
func (s *Store) ListInvoices(since, until string) ([]Invoice, error) {
	q := `SELECT id, file_sha256, source_file, mail_uid, mail_subject, mail_from, mail_date,
	             supplier, invoice_number, invoice_date, vehicle_reg,
	             currency, netto, vat_amount, vat_rate, brutto,
	             needs_review, is_general, returned, credit_of, notes, created_at
	      FROM invoices`
	var args []any
	if since != "" && until != "" {
		q += ` WHERE invoice_date >= ? AND invoice_date <= ?`
		args = append(args, since, until)
	}
	q += ` ORDER BY COALESCE(NULLIF(invoice_date,''), created_at) DESC, id DESC`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Invoice
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

func (s *Store) itemsFor(invoiceID int64) ([]Item, error) {
	rows, err := s.db.Query(`
		SELECT id, invoice_id, line_no, part_number, description, vehicle_reg,
		       quantity, unit_price, netto, vat_amount, vat_rate, brutto
		FROM invoice_items WHERE invoice_id = ? ORDER BY line_no, id`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.InvoiceID, &it.LineNo, &it.PartNumber, &it.Desc,
			&it.VehicleReg, &it.Quantity, &it.UnitPrice, &it.Netto, &it.VATAmount,
			&it.VATRate, &it.Brutto); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) Counts() (invoices, items int, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(1) FROM invoices`).Scan(&invoices); err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(1) FROM invoice_items`).Scan(&items)
	return
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Checkpoint flushes the write-ahead log into the main database file. Called
// on a clean shutdown so the .db on disk stands alone, which matters when the
// next thing to touch it is a backup or a file copy rather than SQLite itself.
func (s *Store) Checkpoint() error {
	_, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}
