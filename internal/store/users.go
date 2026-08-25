package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Roles a dashboard account can hold. RoleAdmin reaches every page and API
// route; RoleFleet is everything except Training and Admin — see
// internal/web's requireAdmin, which is the actual enforcement point. The
// nav hiding these on the front end is a convenience, not the guard.
const (
	RoleAdmin = "admin"
	RoleFleet = "fleet"
)

// ErrUserNotFound is returned by the lookups below when no account matches —
// callers that only care whether a lookup succeeded can compare against this
// directly instead of parsing sql.ErrNoRows themselves.
var ErrUserNotFound = errors.New("no such user")

// User is one dashboard account. Distinct from repairs.<domain>'s shared PIN
// — this is a named, individually-held login with its own password and its
// own 2FA enrollment.
type User struct {
	ID                 int64
	Username           string
	Email              string
	PasswordHash       string
	Role               string
	TOTPSecret         string
	MustChangePassword bool
	// TOTPExempt marks an account that never goes through 2FA at all — set
	// only via CreateUser's skipSetup or SetUserTOTPExempt, for a
	// deliberately shared login. See the users table's own schema comment
	// in store.go for why this exists as a narrow, explicit exception
	// rather than a general "disable 2FA" switch.
	TOTPExempt bool
	CreatedAt  string
}

const userColumns = `id, username, email, password_hash, role, totp_secret, must_change_password, totp_exempt, created_at`

func scanUser(scan func(...any) error) (*User, error) {
	var u User
	var mustChange, totpExempt int
	if err := scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role,
		&u.TOTPSecret, &mustChange, &totpExempt, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	u.MustChangePassword = mustChange != 0
	u.TOTPExempt = totpExempt != 0
	return &u, nil
}

func normalizeUsername(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

// CreateUser adds a new dashboard account. role must be RoleAdmin or
// RoleFleet — anything else is almost certainly a typo, and one that would
// otherwise fail silently: it wouldn't reject here, it would just leave an
// account nothing in the nav-or-route gate recognises as either role.
// totpExempt should be false for every ordinary account — see SetUserTOTPExempt.
func (s *Store) CreateUser(username, email, passwordHash, role string, mustChangePassword, totpExempt bool) (int64, error) {
	username = normalizeUsername(username)
	if username == "" {
		return 0, fmt.Errorf("username is required")
	}
	if passwordHash == "" {
		return 0, fmt.Errorf("password hash is required")
	}
	if role != RoleAdmin && role != RoleFleet {
		return 0, fmt.Errorf("role must be %q or %q, got %q", RoleAdmin, RoleFleet, role)
	}
	res, err := s.db.Exec(`INSERT INTO users (username, email, password_hash, role, must_change_password, totp_exempt, created_at)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'))`,
		username, strings.ToLower(strings.TrimSpace(email)), passwordHash, role,
		boolToInt(mustChangePassword), boolToInt(totpExempt))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, fmt.Errorf("the username %q is already taken", username)
		}
		return 0, err
	}
	return res.LastInsertId()
}

// GetUserByID loads an account by its row id — what a session or pending
// cookie actually carries, once its signature and expiry check out.
func (s *Store) GetUserByID(id int64) (*User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	return scanUser(row.Scan)
}

// GetUserByIdentity looks a user up by username or email, case-insensitively
// — either one plus the password signs in, the same convenience the old
// single-account login accepted.
func (s *Store) GetUserByIdentity(who string) (*User, error) {
	who = normalizeUsername(who)
	if who == "" {
		return nil, ErrUserNotFound
	}
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users
		WHERE username = ? OR (email <> '' AND email = ?)`, who, who)
	return scanUser(row.Scan)
}

// ListUsers backs `goldstar user-list` — ordered by username so the output
// reads the same way twice in a row.
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UserCount is how Auth.Configured() tells "no accounts exist yet, this
// install is wide open" apart from the normal case — checked fresh on every
// call rather than cached, so an account added with `goldstar user-add`
// while the server keeps running is usable immediately, no restart needed.
func (s *Store) UserCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&n)
	return n, err
}

// SetUserPasswordHash installs a new password and always clears
// must_change_password — the entire point of the forced-change flow is that
// typing any fresh password satisfies it, whatever the account required
// before.
func (s *Store) SetUserPasswordHash(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash = ?, must_change_password = 0 WHERE id = ?`, hash, id)
	return err
}

// SetUserTOTPSecret records a confirmed 2FA enrollment.
func (s *Store) SetUserTOTPSecret(id int64, secret string) error {
	_, err := s.db.Exec(`UPDATE users SET totp_secret = ? WHERE id = ?`, strings.TrimSpace(secret), id)
	return err
}

// SetUserTOTPExempt marks (or unmarks) an account as never needing 2FA at
// all — Login issues a session the moment the password checks out, no
// pending step in between. Meant for exactly one situation: a deliberately
// shared login handed out with a fixed password, where requiring 2FA would
// mean whoever enrolls first silently "claims" the account for their own
// phone. Everywhere else 2FA is mandatory on purpose; this exists so that
// exception is a visible, explicit flag on one account rather than a hole in
// the login flow itself.
func (s *Store) SetUserTOTPExempt(id int64, exempt bool) error {
	_, err := s.db.Exec(`UPDATE users SET totp_exempt = ? WHERE id = ?`, boolToInt(exempt), id)
	return err
}

// SetUserRole changes which pages and routes an account can reach.
func (s *Store) SetUserRole(id int64, role string) error {
	if role != RoleAdmin && role != RoleFleet {
		return fmt.Errorf("role must be %q or %q, got %q", RoleAdmin, RoleFleet, role)
	}
	_, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
	return err
}
