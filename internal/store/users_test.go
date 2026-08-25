package store

import "testing"

func TestCreateUserRoundTripsThroughGetByIDAndIdentity(t *testing.T) {
	db := open(t)
	id, err := db.CreateUser("Klon", "klon@example.com", "argon2id$hash", RoleAdmin, true, false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	byID, err := db.GetUserByID(id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if byID.Username != "klon" {
		t.Errorf("Username = %q, want normalized lowercase %q", byID.Username, "klon")
	}
	if byID.Role != RoleAdmin || byID.PasswordHash != "argon2id$hash" || !byID.MustChangePassword {
		t.Errorf("unexpected row: %+v", byID)
	}

	// Either the username or the email, case-insensitively, finds the same
	// account — the same "either one plus the password" convenience the
	// login form offers.
	for _, who := range []string{"klon", "KLON", "klon@example.com", "Klon@Example.com"} {
		u, err := db.GetUserByIdentity(who)
		if err != nil {
			t.Errorf("GetUserByIdentity(%q): %v", who, err)
			continue
		}
		if u.ID != id {
			t.Errorf("GetUserByIdentity(%q) = id %d, want %d", who, u.ID, id)
		}
	}
}

func TestGetUserByIdentityReportsNotFound(t *testing.T) {
	db := open(t)
	if _, err := db.GetUserByIdentity("nobody"); err != ErrUserNotFound {
		t.Errorf("err = %v, want ErrUserNotFound", err)
	}
	if _, err := db.GetUserByIdentity(""); err != ErrUserNotFound {
		t.Errorf("empty identity: err = %v, want ErrUserNotFound", err)
	}
	if _, err := db.GetUserByID(99999); err != ErrUserNotFound {
		t.Errorf("unknown id: err = %v, want ErrUserNotFound", err)
	}
}

func TestCreateUserRejectsDuplicateUsername(t *testing.T) {
	db := open(t)
	if _, err := db.CreateUser("faz", "", "hash1", RoleAdmin, false, false); err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	// Case-insensitively too — "Faz" and "faz" are the same account name.
	if _, err := db.CreateUser("Faz", "", "hash2", RoleFleet, false, false); err == nil {
		t.Fatalf("a duplicate (case-insensitive) username should be rejected")
	}
}

func TestCreateUserValidatesRole(t *testing.T) {
	db := open(t)
	if _, err := db.CreateUser("someone", "", "hash", "superuser", false, false); err == nil {
		t.Fatalf("an unrecognised role should be rejected, not silently stored")
	}
}

func TestSetUserPasswordHashClearsMustChangeFlag(t *testing.T) {
	db := open(t)
	id, err := db.CreateUser("klon", "", "old-hash", RoleAdmin, true, false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.SetUserPasswordHash(id, "new-hash"); err != nil {
		t.Fatalf("SetUserPasswordHash: %v", err)
	}
	u, err := db.GetUserByID(id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if u.PasswordHash != "new-hash" {
		t.Errorf("PasswordHash = %q, want %q", u.PasswordHash, "new-hash")
	}
	if u.MustChangePassword {
		t.Errorf("MustChangePassword should be cleared once a new password is set")
	}
}

func TestSetUserTOTPSecretAndRole(t *testing.T) {
	db := open(t)
	id, err := db.CreateUser("faz", "", "hash", RoleAdmin, false, false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := db.SetUserTOTPSecret(id, "  JBSWY3DPEHPK3PXP  "); err != nil {
		t.Fatalf("SetUserTOTPSecret: %v", err)
	}
	u, err := db.GetUserByID(id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if u.TOTPSecret != "JBSWY3DPEHPK3PXP" {
		t.Errorf("TOTPSecret = %q, want trimmed %q", u.TOTPSecret, "JBSWY3DPEHPK3PXP")
	}

	// This is the actual request behind this whole feature: Faz's account
	// moves from full access to the restricted "fleet" role without
	// touching her password or her already-confirmed 2FA secret.
	if err := db.SetUserRole(id, RoleFleet); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	u, err = db.GetUserByID(id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if u.Role != RoleFleet {
		t.Errorf("Role = %q, want %q", u.Role, RoleFleet)
	}
	if u.TOTPSecret != "JBSWY3DPEHPK3PXP" || u.PasswordHash != "hash" {
		t.Errorf("changing role must not touch password or 2FA: %+v", u)
	}

	if err := db.SetUserRole(id, "owner"); err == nil {
		t.Fatalf("an unrecognised role should be rejected")
	}
}

func TestCreateUserWithTOTPExemptAndSetUserTOTPExempt(t *testing.T) {
	db := open(t)
	id, err := db.CreateUser("temporary", "", "hash", RoleFleet, false, true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := db.GetUserByID(id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if !u.TOTPExempt {
		t.Errorf("TOTPExempt should be true when CreateUser is called with it")
	}
	if u.MustChangePassword {
		t.Errorf("MustChangePassword should be false — CreateUser was called with false")
	}

	if err := db.SetUserTOTPExempt(id, false); err != nil {
		t.Fatalf("SetUserTOTPExempt: %v", err)
	}
	u, err = db.GetUserByID(id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if u.TOTPExempt {
		t.Errorf("TOTPExempt should be false after SetUserTOTPExempt(id, false)")
	}
}

func TestUserCountAndListUsers(t *testing.T) {
	db := open(t)
	if n, err := db.UserCount(); err != nil || n != 0 {
		t.Fatalf("UserCount on a fresh database = (%d, %v), want (0, nil)", n, err)
	}

	if _, err := db.CreateUser("zeta", "", "h", RoleAdmin, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateUser("alpha", "", "h", RoleFleet, false, false); err != nil {
		t.Fatal(err)
	}

	if n, err := db.UserCount(); err != nil || n != 2 {
		t.Fatalf("UserCount = (%d, %v), want (2, nil)", n, err)
	}

	list, err := db.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(list) != 2 || list[0].Username != "alpha" || list[1].Username != "zeta" {
		t.Fatalf("ListUsers = %+v, want alpha then zeta (alphabetical)", list)
	}
}
