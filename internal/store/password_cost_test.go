package store

import (
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Cheap, real bcrypt fixture accounts must not change the work factor used by
// any production password-writing path. Keep this focused contract on Open.
func TestProductionPasswordWorkFactor(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "password-policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	check := func(id int64) string {
		t.Helper()
		var hash string
		if err := db.db.QueryRow("SELECT password_hash FROM users WHERE id=?", id).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		cost, err := bcrypt.Cost([]byte(hash))
		if err != nil || cost != bcrypt.DefaultCost {
			t.Fatalf("production password cost=%d, want %d: %v", cost, bcrypt.DefaultCost, err)
		}
		return hash
	}
	if err := db.InitializeAdministrator("owner", "owner-policy-password"); err != nil {
		t.Fatal(err)
	}
	ownerHash := check(1)
	var legacyHash string
	if err := db.db.QueryRow("SELECT password_hash FROM administrators WHERE id=1").Scan(&legacyHash); err != nil || legacyHash != ownerHash {
		t.Fatalf("legacy administrator password compatibility: %v", err)
	}
	user, err := db.CreateUser("created", "created-policy-password", false)
	if err != nil {
		t.Fatal(err)
	}
	check(user.ID)
	if err := db.SetUserPassword(user.ID, "reset-policy-password"); err != nil {
		t.Fatal(err)
	}
	check(user.ID)
	if err := db.SaveAccessTokenForUser("policy-owner-proof", 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	proof, err := db.RequestCredentialUser("policy-owner-proof", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := db.AdminCreateUser(1, "managed", "managed-policy-password", false, nil, proof)
	if err != nil {
		t.Fatal(err)
	}
	check(managed.ID)
	password := "updated-policy-password"
	if err := db.AdminUpdateUser(1, managed.ID, AdminUserUpdate{Password: &password}, proof); err != nil {
		t.Fatal(err)
	}
	check(managed.ID)
	if _, ok, err := db.AuthenticateUser(managed.Name, password); err != nil || !ok {
		t.Fatalf("production password roundtrip: valid=%v err=%v", ok, err)
	}
	if _, ok, err := db.AuthenticateUser(managed.Name, "wrong-password"); err != nil || ok {
		t.Fatalf("production wrong password: valid=%v err=%v", ok, err)
	}
}
